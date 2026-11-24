//go:build linux

package boundary

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/pengtang1024/kore-plugin-app-deploy/internal/apop"
)

// 本文件是「权限不足（只读挂载点）」与「资源耗尽（文件大小限制 / 文件系统写满）」
// 的真实注入面。注入不靠伪造错误：子进程里真的 setrlimit(RLIMIT_FSIZE)、
// 真的 mount tmpfs 并 remount 成只读，再调用插件方法。
//
// 为什么要子进程：RLIMIT_FSIZE 与 mount 都是进程级/命名空间级副作用，
// 不能让主测试进程承担（会把 go test 自己的写盘一起限制住）。

const (
	helperModeEnv  = "BOUNDARY_HELPER_MODE"
	helperRootEnv  = "BOUNDARY_HELPER_ROOT"
	helperStateEnv = "BOUNDARY_HELPER_STATE"
	helperSizeEnv  = "BOUNDARY_HELPER_SIZE"
	helperLimitEnv = "BOUNDARY_HELPER_LIMIT"

	helperModeFSIZE   = "fsize"
	helperModeTmpfs   = "tmpfs-full"
	helperModeROMount = "ro-mount"

	helperResultPrefix = "BOUNDARY_RESULT\t"
	helperSkipPrefix   = "BOUNDARY_SKIP\t"
)

// TestBoundaryHelperProcess 是子进程入口：只有被父用例以环境变量唤起时才执行，
// 平时（父进程里的收集阶段）直接跳过，不计入用例数。
func TestBoundaryHelperProcess(t *testing.T) {
	mode := os.Getenv(helperModeEnv)
	if mode == "" {
		t.Skip("仅作为资源/权限注入的子进程 helper 运行")
	}
	root := os.Getenv(helperRootEnv)
	state := os.Getenv(helperStateEnv)
	if root == "" || state == "" {
		emitSkip("helper 缺少 root/state 环境变量")
	}

	switch mode {
	case helperModeFSIZE:
		limit := envInt(t, helperLimitEnv, 64<<10)
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{
			Cur: uint64(limit), Max: uint64(limit),
		}); err != nil {
			emitSkip("setrlimit(RLIMIT_FSIZE) 失败: " + err.Error())
		}
	case helperModeTmpfs, helperModeROMount:
		if err := syscall.Mount("tmpfs", root, "tmpfs", 0, "size=128k"); err != nil {
			emitSkip("mount tmpfs 失败: " + err.Error())
		}
		if mode == helperModeROMount {
			if err := syscall.Mount("", root, "", syscall.MS_REMOUNT|syscall.MS_RDONLY, ""); err != nil {
				emitSkip("remount 只读失败: " + err.Error())
			}
		}
	default:
		emitSkip("未知 helper 模式: " + mode)
	}

	size := envInt(t, helperSizeEnv, 1<<20)
	data := zipArtifactSized(t, "payload.bin", size)
	rt := apop.NewRuntime(apop.Options{
		Root:      root,
		StateDir:  state,
		GOOS:      "linux",
		PortProbe: func(int) bool { return true },
	}, &fakeExecutor{})
	result, perr := rt.Call("app-deploy.upload", uploadParams(t, "kore-app-bd-helper", data))
	if perr != nil {
		emitResult(perr.Code, perr.Message)
	}
	emitResult("OK", string(result))
}

// emitResult 输出机器可读结果行并结束子进程。
func emitResult(code, message string) {
	fmt.Printf("%s%s\t%s\n", helperResultPrefix, code, oneLine(message))
	os.Exit(0)
}

// emitSkip 输出跳过原因并结束子进程（父进程转成 t.Skip）。
func emitSkip(reason string) {
	fmt.Printf("%s%s\n", helperSkipPrefix, oneLine(reason))
	os.Exit(0)
}

// oneLine 压平错误信息（含换行会破坏单行协议）。
func oneLine(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(s)
}

// envInt 读取整数环境变量，非法或缺失时取默认值。
func envInt(t *testing.T, key string, def int) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		t.Fatalf("环境变量 %s=%q 不是正整数", key, raw)
	}
	return value
}

// helperEnv 组装子进程环境：先清掉同名键，避免父进程残留值干扰。
func helperEnv(pairs ...string) []string {
	overrides := make(map[string]bool, len(pairs))
	for _, pair := range pairs {
		key, _, _ := strings.Cut(pair, "=")
		overrides[key] = true
	}
	env := make([]string, 0, len(os.Environ())+len(pairs))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if !overrides[key] {
			env = append(env, item)
		}
	}
	return append(env, pairs...)
}

// runHelper 启动子进程执行注入并解析结果行。
// userns=true 时在用户命名空间 + 挂载命名空间里运行（非 root 也能真 mount）；
// 环境不支持时 skip，并给出明确原因，不伪造通过。
func runHelper(t *testing.T, mode string, userns bool, root, state string, extra ...string) (code, message string) {
	t.Helper()
	args := []string{"-test.run=^TestBoundaryHelperProcess$", "-test.timeout=120s"}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = helperEnv(append([]string{
		helperModeEnv + "=" + mode,
		helperRootEnv + "=" + root,
		helperStateEnv + "=" + state,
	}, extra...)...)
	if userns {
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags:                 syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
			UidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
			GidMappings:                []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
			GidMappingsEnableSetgroups: false,
		}
	}
	out, err := cmd.CombinedOutput()
	text := string(out)
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, helperSkipPrefix) {
			t.Skipf("注入面在当前环境不可用: %s", strings.TrimPrefix(line, helperSkipPrefix))
		}
		if strings.HasPrefix(line, helperResultPrefix) {
			fields := strings.Split(strings.TrimPrefix(line, helperResultPrefix), "\t")
			if len(fields) != 2 || fields[0] == "" {
				t.Fatalf("helper 结果行格式非法: %q", line)
			}
			return fields[0], fields[1]
		}
	}
	if userns && err != nil && strings.Contains(strings.ToLower(err.Error()), "operation not permitted") {
		t.Skipf("当前环境禁止非特权用户命名空间，无法真实注入 mount: %v", err)
	}
	t.Fatalf("helper 未输出结果（err=%v）:\n%s", err, text)
	return "", ""
}

// TestUploadFileSizeLimitExhausted 资源耗尽①：解压写文件时撞上 RLIMIT_FSIZE
// （等价 ulimit -f）：必须回结构化错误，而不是静默产出截断文件。
func TestUploadFileSizeLimitExhausted(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	state := filepath.Join(dir, "state")

	code, message := runHelper(t, helperModeFSIZE, false, root, state,
		helperLimitEnv+"=65536", helperSizeEnv+"=262144")
	if code != "E_INVALID" {
		t.Fatalf("RLIMIT_FSIZE 触发后错误码=%q，期望 E_INVALID（message=%q）", code, message)
	}
	if !strings.Contains(message, "file too large") {
		t.Fatalf("错误信息未指向文件大小限制: %q", message)
	}
	if _, err := os.Stat(filepath.Join(root, "kore-app-bd-helper")); !os.IsNotExist(err) {
		t.Fatalf("写入超限后仍留下应用目录: err=%v", err)
	}
	assertNoStagingResidue(t, root)
}

// TestUploadTmpfsFull 资源耗尽②：数据根是容量很小的真实 tmpfs，解压写满后
// 必须回结构化错误（ENOSPC），不能假装成功。
func TestUploadTmpfsFull(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("创建挂载点目录失败: %v", err)
	}
	state := filepath.Join(dir, "state")

	code, message := runHelper(t, helperModeTmpfs, true, root, state, helperSizeEnv+"=1048576")
	if code != "E_INVALID" {
		t.Fatalf("tmpfs 写满后错误码=%q，期望 E_INVALID（message=%q）", code, message)
	}
	if !strings.Contains(message, "no space left on device") {
		t.Fatalf("错误信息未指向 ENOSPC: %q", message)
	}
}

// TestReadOnlyMountPointRejected 权限不足②：数据根是真实只读挂载点（tmpfs
// remount ro），解压暂存目录创建必须被内核拒绝（EROFS → E_INTERNAL）。
// 该用例在 root 下也成立（只读挂载对 root 同样拒绝写入）。
func TestReadOnlyMountPointRejected(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("创建挂载点目录失败: %v", err)
	}
	state := filepath.Join(dir, "state")

	code, message := runHelper(t, helperModeROMount, true, root, state, helperSizeEnv+"=4096")
	if code != "E_INTERNAL" {
		t.Fatalf("只读挂载点错误码=%q，期望 E_INTERNAL（message=%q）", code, message)
	}
	if !strings.Contains(message, "read-only file system") {
		t.Fatalf("错误信息未指向只读文件系统: %q", message)
	}
	// 子进程的挂载随命名空间销毁，父进程视角的挂载点仍是空目录。
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读取挂载点目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("只读挂载点下出现写入产物: %v", entries)
	}
}
