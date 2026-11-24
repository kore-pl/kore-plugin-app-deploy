package boundary

import (
	"os"
	"path/filepath"
	"testing"
)

// 本文件是「权限不足」边界的可移植部分：目标目录 chmod 000（属主自己也失去
// 读写执行位）时，升级必须明确失败且不动旧版本。真实只读挂载点的注入见
// mount_helper_test.go（需要 Linux 用户命名空间，单独文件承载）。
//
// 已有覆盖（tests/fault）：数据根 0555 只读 ×2；本用例补的是升级目标目录 000。

// TestUpgradeTargetDirMode000Denied 升级目标目录权限被收回：
// 快照阶段读目录必须失败（E_INTERNAL + permission denied），旧版本内容原样保留，
// 且不残留升级暂存目录。
func TestUpgradeTargetDirMode000Denied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("以 root 运行时权限位被绕过，chmod 000 不构成拒绝（该场景在非 root CI 上生效）")
	}
	exec := &fakeExecutor{}
	rt, root, _ := newRuntime(t, exec)
	const name = "kore-app-bd-perm0"
	mustOK(t, rt, "app-deploy.upload", uploadParams(t, name, zipArtifact(t, map[string]string{
		"v.txt": "v1", "index.html": "old",
	})))

	target := filepath.Join(root, name)
	if err := os.Chmod(target, 0o000); err != nil {
		t.Fatalf("收回目标目录权限失败: %v", err)
	}
	defer func() { _ = os.Chmod(target, 0o755) }()

	result, perr := rt.Call("app-deploy.upgrade", uploadParams(t, name, zipArtifact(t, map[string]string{
		"v.txt": "v2", "index.html": "new",
	})))
	requireError(t, result, perr, "E_INTERNAL", "permission denied")

	// 恢复权限后核对旧版本未被改动。
	if err := os.Chmod(target, 0o755); err != nil {
		t.Fatalf("恢复目标目录权限失败: %v", err)
	}
	if got := string(readFile(t, filepath.Join(target, "v.txt"))); got != "v1" {
		t.Fatalf("升级失败后旧版本被破坏: v.txt=%q，期望 v1", got)
	}
	assertNoStagingResidue(t, root)
}
