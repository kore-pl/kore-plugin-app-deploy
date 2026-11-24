// Package boundary 是 app-deploy 的边界用例套件（G-20 判据①：异常输入 / 并发 /
// 权限不足 / 资源耗尽 各 ≥1）。
//
// 与 tests/fault 的分工：fault 注入的是「执行面返回什么错误」，本套件验证的是
// 真实边界——真并发（-race 下跑）、真实权限位 / 只读挂载、真实资源耗尽
// （RLIMIT_FSIZE / tmpfs 满）。断言错误码 + 落盘副作用，不做「没 panic」式空断言。
package boundary

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/pengtang1024/kore-plugin-app-deploy/internal/apop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// testGOOS 固定注入 linux：边界用例只验证 Linux 真实部署路径，
// 非 Linux 的 E_UNSUPPORTED 已由 internal/apop 单测覆盖。
const testGOOS = "linux"

// zipArtifact 构造确定性 zip（条目名排序），内容来自 files。
func zipArtifact(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("构造 zip 条目 %s 失败: %v", name, err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatalf("写入 zip 条目 %s 失败: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

// zipArtifactSized 构造只含一个大文件的 zip（内容为 0），用于资源耗尽注入。
// 注意上限按 base64 解码后的 zip 字节数判定：0 高度可压缩，包体很小，
// 但解压后写盘字节数是 size，足以触发文件系统限制。
func zipArtifactSized(t *testing.T, name string, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatalf("构造大条目失败: %v", err)
	}
	if _, err := w.Write(make([]byte, size)); err != nil {
		t.Fatalf("写入大条目失败: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

// uploadParams 组装 upload / upgrade 的参数（base64 包内容）。
func uploadParams(t *testing.T, name string, data []byte) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"name":           name,
		"format":         apop.ArtifactFormatZip,
		"content_base64": base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		t.Fatalf("序列化 upload 参数失败: %v", err)
	}
	return raw
}

// newRuntime 构造数据根 / 状态目录都落在 t.TempDir() 的运行时；
// PortProbe 恒可用，避免测试依赖宿主机端口状态。
func newRuntime(t *testing.T, exec apop.Executor) (rt *apop.Runtime, root, state string) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "root")
	state = filepath.Join(dir, "state")
	rt = apop.NewRuntime(apop.Options{
		Root:      root,
		StateDir:  state,
		GOOS:      testGOOS,
		PortProbe: func(int) bool { return true },
	}, exec)
	return rt, root, state
}

// fakeExecutor 是并发安全的内存执行面：并发用例会同时调用它，
// 未加锁的记录切片本身就会成为数据竞争（不能把测试替身变成竞态源）。
type fakeExecutor struct {
	mu     sync.Mutex
	runs   []apop.ContainerSpec
	states []apop.ContainerState
	nextID int
}

// RunContainer 记录规格并返回 64 位十六进制容器 ID。
func (f *fakeExecutor) RunContainer(spec apop.ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	id := fmt.Sprintf("%064x", f.nextID)
	f.runs = append(f.runs, spec)
	f.states = append(f.states, apop.ContainerState{
		ID: id, Name: spec.Name, Image: spec.Image, State: apop.StatusRunning, Status: "Up",
	})
	return id, nil
}

// ListContainers 返回快照副本（调用方不得持有内部切片）。
func (f *fakeExecutor) ListContainers() ([]apop.ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]apop.ContainerState, len(f.states))
	copy(out, f.states)
	return out, nil
}

// RemoveContainers 从快照移除指定 ID。
func (f *fakeExecutor) RemoveContainers(ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	drop := make(map[string]bool, len(ids))
	for _, id := range ids {
		drop[id] = true
	}
	kept := f.states[:0]
	for _, state := range f.states {
		if !drop[state.ID] {
			kept = append(kept, state)
		}
	}
	f.states = kept
	return nil
}

// RemoveVolumes / CreateNetwork / RemoveNetwork 对本套件无副作用。
func (f *fakeExecutor) RemoveVolumes([]string) error { return nil }
func (f *fakeExecutor) CreateNetwork(string) error   { return nil }
func (f *fakeExecutor) RemoveNetwork(string) error   { return nil }

// runCount 返回 docker run 调用次数。
func (f *fakeExecutor) runCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.runs)
}

// containerNames 返回按调用顺序记录的容器名。
func (f *fakeExecutor) containerNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	names := make([]string, 0, len(f.runs))
	for _, spec := range f.runs {
		names = append(names, spec.Name)
	}
	return names
}

// mustOK 调用方法并要求成功返回。
func mustOK(t *testing.T, rt *apop.Runtime, method string, params json.RawMessage) json.RawMessage {
	t.Helper()
	result, perr := rt.Call(method, params)
	if perr != nil {
		t.Fatalf("%s 期望成功，实际错误 %s: %s", method, perr.Code, perr.Message)
	}
	return result
}

// requireError 断言调用返回协议错误：结果必须为 nil、错误码精确匹配、
// message 含指定关键词（关键词用于证明错误指向真实故障面而非笼统失败）。
func requireError(t *testing.T, result json.RawMessage, perr *pluginapi.Error, code, keyword string) {
	t.Helper()
	if perr == nil {
		t.Fatalf("期望错误码 %s，实际成功: %s", code, result)
	}
	if result != nil {
		t.Fatalf("失败时不应带结果: %s", result)
	}
	if perr.Code != code {
		t.Fatalf("错误码=%q，期望 %q（message=%q）", perr.Code, code, perr.Message)
	}
	if perr.Message == "" {
		t.Fatal("错误 message 为空")
	}
	if keyword != "" && !strings.Contains(perr.Message, keyword) {
		t.Fatalf("错误信息 %q 未含关键词 %q", perr.Message, keyword)
	}
}

// deploymentNames 读取 deployments.json 并返回记录名；文件不存在返回空。
// 解析失败即判失败（并发的原子替换保证读到的永远是完整文件）。
func deploymentNames(t *testing.T, stateDir string) []string {
	t.Helper()
	path := filepath.Join(stateDir, apop.DeploymentsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("读取部署记录失败: %v", err)
	}
	var doc struct {
		Version     int `json:"version"`
		Deployments []struct {
			Name string `json:"name"`
		} `json:"deployments"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("部署记录不是完整 JSON（读到半截文件？）: %v（%s）", err, data)
	}
	names := make([]string, 0, len(doc.Deployments))
	for _, rec := range doc.Deployments {
		names = append(names, rec.Name)
	}
	return names
}

// readFile 读取文件内容；不存在返回 nil。
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return data
}

// assertNoStagingResidue 断言数据根下没有解压/升级临时目录残留。
func assertNoStagingResidue(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		t.Fatalf("读取数据根失败: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "staging") {
			t.Fatalf("数据根残留暂存目录: %s", entry.Name())
		}
	}
}
