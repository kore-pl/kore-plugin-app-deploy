package boundary

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pengtang1024/kore-plugin-app-deploy/internal/apop"
)

// 本文件是「异常输入」边界：复用仓库已有的两个场景（非法包路径、格式错误的
// 部署记录），在方法面（app-deploy.upload / list / deploy）上验收入口拒绝 +
// 零越界副作用，而不是只测未导出的内部函数。

// TestInvalidArtifactEntryPathRejected 非法包路径：zip 条目试图用相对穿越 /
// 绝对路径 / 反斜杠逃出数据根。每条都必须回 E_INVALID，且数据根外不出现文件。
func TestInvalidArtifactEntryPathRejected(t *testing.T) {
	cases := []struct {
		name    string
		entry   string
		keyword string
		outside string // 若越界会落在数据根父目录下的文件名
	}{
		{"dotdot", "../bd-escape.txt", "路径穿越", "bd-escape.txt"},
		{"nested-dotdot", "a/../../bd-escape.txt", "路径穿越", "bd-escape.txt"},
		{"absolute", "/tmp/bd-escape.txt", "绝对路径", ""},
		{"backslash", `..\bd-escape.txt`, "反斜杠", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exec := &fakeExecutor{}
			rt, root, _ := newRuntime(t, exec)
			// 数据根父目录 = t.TempDir()，用于断言没有文件被写到根外。
			outsideDir := filepath.Dir(root)

			result, perr := rt.Call("app-deploy.upload",
				uploadParams(t, "kore-app-bd-bad", zipArtifact(t, map[string]string{tc.entry: "pwn"})))
			requireError(t, result, perr, "E_INVALID", tc.keyword)

			if _, err := os.Stat(filepath.Join(root, "kore-app-bd-bad")); !os.IsNotExist(err) {
				t.Fatalf("非法包仍留下应用目录: err=%v", err)
			}
			if tc.outside != "" {
				if _, err := os.Stat(filepath.Join(outsideDir, tc.outside)); !os.IsNotExist(err) {
					t.Fatalf("条目越界写到数据根之外: err=%v", err)
				}
			}
			if got := exec.runCount(); got != 0 {
				t.Fatalf("非法包不应触达执行面，docker run=%d", got)
			}
		})
	}
}

// TestMalformedConfigRejectedAndPreserved 格式错误的部署记录：必须回 E_INTERNAL，
// 且**原样保留**损坏文件（拒绝自动重置，否则已有容器失去归属记录）。
// list 与 deploy 两条读路径都要命中，deploy 还不得触达执行面。
func TestMalformedConfigRejectedAndPreserved(t *testing.T) {
	exec := &fakeExecutor{}
	rt, _, state := newRuntime(t, exec)

	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("创建状态目录失败: %v", err)
	}
	statePath := filepath.Join(state, apop.DeploymentsFile)
	corrupt := []byte("{ this is not json")
	if err := os.WriteFile(statePath, corrupt, 0o600); err != nil {
		t.Fatalf("写入损坏部署记录失败: %v", err)
	}

	listResult, listErr := rt.Call("app-deploy.list", json.RawMessage(`{}`))
	requireError(t, listResult, listErr, "E_INTERNAL", "损坏")

	deployParams := json.RawMessage(`{"template":"nginx-static","name":"kore-app-bd-x"}`)
	deployResult, deployErr := rt.Call("app-deploy.deploy", deployParams)
	requireError(t, deployResult, deployErr, "E_INTERNAL", "损坏")
	if got := exec.runCount(); got != 0 {
		t.Fatalf("部署记录损坏时仍触达执行面，docker run=%d", got)
	}

	if got, err := os.ReadFile(statePath); err != nil || !bytes.Equal(got, corrupt) {
		t.Fatalf("损坏的部署记录被改写: err=%v got=%q", err, got)
	}
}
