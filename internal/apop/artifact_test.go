package apop

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildZip 用给定条目构造 zip（name→内容）；目录条目以 / 结尾。
func buildZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, content := range entries {
		if strings.HasSuffix(name, "/") {
			if _, err := writer.Create(name); err != nil {
				t.Fatal(err)
			}
			continue
		}
		w, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// buildTarGz 用给定条目构造 tar.gz。
func buildTarGz(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
		if strings.HasSuffix(name, "/") {
			header.Typeflag = tar.TypeDir
			header.Size = 0
		} else {
			header.Typeflag = tar.TypeReg
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tw.Write([]byte(content)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// newArtifactRuntime 构造以 t.TempDir 为根的执行面。
func newArtifactRuntime(t *testing.T) *Runtime {
	t.Helper()
	dir := t.TempDir()
	return NewRuntime(Options{
		Root:      filepath.Join(dir, "apps"),
		StateDir:  filepath.Join(dir, "snapshots"),
		DockerBin: "docker",
	}, &fakeExecutor{})
}

// uploadParams 构造 base64 入参。
func uploadParams(name, format string, data []byte) json.RawMessage {
	raw, _ := json.Marshal(artifactParams{
		Name: name, Format: format, ContentBase64: base64.StdEncoding.EncodeToString(data),
	})
	return raw
}

// TestSafeEntryNameRejectsTraversal 逐条覆盖穿越/绝对路径/盘符/NUL。
func TestSafeEntryNameRejectsTraversal(t *testing.T) {
	bad := []string{"../etc/passwd", "../../x", "/etc/passwd", `\\evil`, `a\b`, "C:/x", "a/../../b", "a\x00b", "."}
	for _, name := range bad {
		if _, err := safeEntryName(name); err == nil {
			t.Errorf("条目 %q 应被拒绝", name)
		}
	}
	for _, name := range []string{"index.html", "assets/app.js", "dir/sub/file.txt"} {
		if _, err := safeEntryName(name); err != nil {
			t.Errorf("条目 %q 应被接受: %v", name, err)
		}
	}
}

// TestUploadZipAndTarTraversalRejected 端到端：穿越条目必须整体拒绝且不落盘到目标外。
func TestUploadZipAndTarTraversalRejected(t *testing.T) {
	runtime := newArtifactRuntime(t)
	cases := []struct {
		name   string
		format string
		data   []byte
	}{
		{"zip-traversal", ArtifactFormatZip, buildZip(t, map[string]string{"../escape.txt": "x"})},
		{"tar-traversal", ArtifactFormatTarGz, buildTarGz(t, map[string]string{"../escape.txt": "x"})},
		{"zip-absolute", ArtifactFormatZip, buildZip(t, map[string]string{"/tmp/escape.txt": "x"})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, perr := runtime.callUpload(uploadParams("evil-app", tc.format, tc.data))
			if perr == nil {
				t.Fatal("穿越条目必须被拒绝")
			}
			if !strings.Contains(perr.Message, "穿越") && !strings.Contains(perr.Message, "绝对路径") {
				t.Fatalf("错误信息应点名穿越/绝对路径：%s", perr.Message)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(runtime.opts.Root), "escape.txt")); err == nil {
				t.Fatal("穿越文件不得落盘")
			}
		})
	}
}

// TestUploadRejectsBadArchiveAndMissingContent 坏包与缺包。
func TestUploadRejectsBadArchiveAndMissingContent(t *testing.T) {
	runtime := newArtifactRuntime(t)
	if _, perr := runtime.callUpload(json.RawMessage(`{"name":"a","format":"zip","content_base64":""}`)); perr == nil {
		t.Fatal("缺 content_base64 应拒绝")
	}
	if _, perr := runtime.callUpload(uploadParams("a", ArtifactFormatTarGz, []byte("not a gzip"))); perr == nil {
		t.Fatal("坏包应拒绝")
	}
	if _, perr := runtime.callUpload(uploadParams("a", ArtifactFormatZip, []byte("not a zip"))); perr == nil {
		t.Fatal("坏 zip 应拒绝")
	}
}

// TestUploadThenDeployFromArtifact 上传 → 部署登记 → 站点/建库提示（跨插件边界不越权）。
func TestUploadThenDeployFromArtifact(t *testing.T) {
	runtime := newArtifactRuntime(t)
	zipData := buildZip(t, map[string]string{"index.html": "<h1>hi</h1>", "assets/app.js": "x"})
	resp, perr := runtime.callUpload(uploadParams("myapp", ArtifactFormatZip, zipData))
	if perr != nil {
		t.Fatalf("upload 失败: %+v", perr)
	}
	var up UploadResult
	if err := json.Unmarshal(resp, &up); err != nil {
		t.Fatal(err)
	}
	if up.Files != 2 || up.Path != filepath.Join(runtime.opts.Root, "myapp") {
		t.Fatalf("upload 结果不符: %+v", up)
	}
	if _, err := os.Stat(filepath.Join(up.Path, "index.html")); err != nil {
		t.Fatalf("解压产物缺失: %v", err)
	}
	params, _ := json.Marshal(artifactParams{Name: "myapp", Databases: []DatabaseDecl{{Engine: "mysql", Name: "myapp_db"}}, SiteDomain: "app.example.com"})
	raw, perr := runtime.callDeployFromArtifact(params)
	if perr != nil {
		t.Fatalf("deployFromArtifact 失败: %+v", perr)
	}
	var res ArtifactDeployResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if !res.Deployed || res.SiteHint.Root != up.Path || res.SiteHint.Domain != "app.example.com" {
		t.Fatalf("部署结果不符: %+v", res)
	}
	joined := strings.Join(res.Hints, "|")
	if !strings.Contains(joined, "site") || !strings.Contains(joined, "db-mysql") {
		t.Fatalf("hint 必须点名 site / db-mysql 边界：%s", joined)
	}
	// 记录已登记且落在安装目录内
	list, perr := runtime.callList(json.RawMessage(`{}`))
	if perr != nil {
		t.Fatal(perr)
	}
	var lr ListResult
	if err := json.Unmarshal(list, &lr); err != nil {
		t.Fatal(err)
	}
	if lr.Count != 1 || lr.Deployments[0].OutsideInstall {
		t.Fatalf("artifact 部署记录不符: %+v", lr)
	}
	// 不越权：不得创建数据库 / 不得写站点配置（本仓无任何此类副作用即通过）
	for _, forbidden := range []string{"nginx", "sites-enabled"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(runtime.opts.Root), forbidden)); err == nil {
			t.Fatalf("不得写入 %s（跨插件边界）", forbidden)
		}
	}
}

// TestUpgradeSnapshotAndRollback 升级留快照；换入失败必须回滚到旧目录。
func TestUpgradeSnapshotAndRollback(t *testing.T) {
	runtime := newArtifactRuntime(t)
	if _, perr := runtime.callUpload(uploadParams("app", ArtifactFormatZip, buildZip(t, map[string]string{"v.txt": "v1"}))); perr != nil {
		t.Fatalf("upload v1 失败: %+v", perr)
	}
	raw, perr := runtime.callUpgrade(uploadParams("app", ArtifactFormatZip, buildZip(t, map[string]string{"v.txt": "v2"})))
	if perr != nil {
		t.Fatalf("upgrade 失败: %+v", perr)
	}
	var up UpgradeResult
	if err := json.Unmarshal(raw, &up); err != nil {
		t.Fatal(err)
	}
	if !up.Upgraded || up.Snapshot == "" {
		t.Fatalf("升级结果不符: %+v", up)
	}
	if got, _ := os.ReadFile(filepath.Join(runtime.opts.Root, "app", "v.txt")); string(got) != "v2" {
		t.Fatalf("升级后应为 v2，实际 %q", got)
	}
	if got, err := os.ReadFile(filepath.Join(up.Snapshot, "v.txt")); err != nil || string(got) != "v1" {
		t.Fatalf("快照应保留 v1: %v %q", err, got)
	}

	// 注入换入失败：第二次 rename 报错 → 必须回滚到旧目录（v2 保持完好）
	orig := renameFn
	defer func() { renameFn = orig }()
	calls := 0
	renameFn = func(from, to string) error {
		calls++
		if calls == 2 {
			return errors.New("injected rename failure")
		}
		return orig(from, to)
	}
	if _, perr := runtime.callUpgrade(uploadParams("app", ArtifactFormatZip, buildZip(t, map[string]string{"v.txt": "v3"}))); perr == nil {
		t.Fatal("换入失败应返回错误")
	}
	renameFn = orig
	if got, err := os.ReadFile(filepath.Join(runtime.opts.Root, "app", "v.txt")); err != nil || string(got) != "v2" {
		t.Fatalf("换入失败必须回滚到旧目录 v2: %v %q", err, got)
	}
}

// TestJobsProgressStateMachine 作业状态机：成功/失败都落盘可轮询，且无后台 ticker。
func TestJobsProgressStateMachine(t *testing.T) {
	runtime := newArtifactRuntime(t)
	resp, perr := runtime.callUpload(uploadParams("ok", ArtifactFormatZip, buildZip(t, map[string]string{"a.txt": "x"})))
	if perr != nil {
		t.Fatal(perr)
	}
	var up UploadResult
	if err := json.Unmarshal(resp, &up); err != nil {
		t.Fatal(err)
	}
	raw, perr := runtime.callJobs(json.RawMessage(`{"id":"` + up.Job + `"}`))
	if perr != nil {
		t.Fatal(perr)
	}
	var jr JobsResult
	if err := json.Unmarshal(raw, &jr); err != nil {
		t.Fatal(err)
	}
	if jr.Count != 1 || jr.Jobs[0].Status != JobSucceeded || len(jr.Jobs[0].Steps) < 2 {
		t.Fatalf("成功作业状态机不符: %+v", jr)
	}
	if _, perr := runtime.callUpload(uploadParams("bad", ArtifactFormatZip, []byte("junk"))); perr == nil {
		t.Fatal("坏包应失败")
	}
	all, perr := runtime.callJobs(json.RawMessage(`{}`))
	if perr != nil {
		t.Fatal(perr)
	}
	var allJobs JobsResult
	if err := json.Unmarshal(all, &allJobs); err != nil {
		t.Fatal(err)
	}
	foundFailed := false
	for _, job := range allJobs.Jobs {
		if job.Status == JobFailed && job.Error != "" {
			foundFailed = true
		}
	}
	if !foundFailed {
		t.Fatalf("失败作业必须记录 failed + error: %+v", allJobs)
	}
}

// TestUpgradeSameSecondSnapshotsIsolated 是同秒升级快照互相覆盖竞态的缺陷回归（P2）。
//
// 缺陷本体：快照目录名只有秒级时间戳，同一应用同一秒两次 upgrade 撞同一路径；
// 旧代码用 os.MkdirAll 建快照，对已存在目录返回 nil（不报错），于是第二次
// copyTree 把内容写进第一次的快照、互相覆盖。
//
// 本用例固定 snapshotStampFn，把「同秒」从概率事件变成确定性条件：
// 两次 upgrade 串行执行但共享同一时间前缀。断言三次——① 两个快照路径不同；
// ② 第一次快照的 v1 不被第二次的 v2 覆盖；③ 第二次快照保留升级前的 v2。
// 修复前 ① 必然 FAIL（同路径），修复后三条全 PASS。
func TestUpgradeSameSecondSnapshotsIsolated(t *testing.T) {
	runtime := newArtifactRuntime(t)
	origStamp := snapshotStampFn
	snapshotStampFn = func() string { return "20261124T120000Z" }
	defer func() { snapshotStampFn = origStamp }()

	if _, perr := runtime.callUpload(uploadParams("app", ArtifactFormatZip, buildZip(t, map[string]string{"v.txt": "v1"}))); perr != nil {
		t.Fatalf("upload v1 失败: %+v", perr)
	}
	firstRaw, perr := runtime.callUpgrade(uploadParams("app", ArtifactFormatZip, buildZip(t, map[string]string{"v.txt": "v2"})))
	if perr != nil {
		t.Fatalf("第一次 upgrade 失败: %+v", perr)
	}
	var first UpgradeResult
	if err := json.Unmarshal(firstRaw, &first); err != nil {
		t.Fatal(err)
	}
	secondRaw, perr := runtime.callUpgrade(uploadParams("app", ArtifactFormatZip, buildZip(t, map[string]string{"v.txt": "v3"})))
	if perr != nil {
		t.Fatalf("第二次 upgrade 失败: %+v", perr)
	}
	var second UpgradeResult
	if err := json.Unmarshal(secondRaw, &second); err != nil {
		t.Fatal(err)
	}

	// ① 同秒两次 upgrade 必须落到不同快照目录。
	if first.Snapshot == "" || second.Snapshot == "" {
		t.Fatalf("快照路径为空: first=%q second=%q", first.Snapshot, second.Snapshot)
	}
	if first.Snapshot == second.Snapshot {
		t.Fatalf("同秒两次 upgrade 撞同一快照路径 %q：第二次 copyTree 覆盖了第一次", first.Snapshot)
	}
	// ② 第一次快照内容必须是升级前的 v1，不得被第二次的 v2 覆盖。
	if got, err := os.ReadFile(filepath.Join(first.Snapshot, "v.txt")); err != nil || string(got) != "v1" {
		t.Fatalf("第一次快照应为 v1（未被覆盖），实际 %q err=%v（路径 %s）", got, err, first.Snapshot)
	}
	// ③ 第二次快照保留它升级前的 v2。
	if got, err := os.ReadFile(filepath.Join(second.Snapshot, "v.txt")); err != nil || string(got) != "v2" {
		t.Fatalf("第二次快照应为 v2，实际 %q err=%v（路径 %s）", got, err, second.Snapshot)
	}
	// ④ 快照根目录下恰好两个独立子目录（不是靠改返回值骗过断言）。
	entries, err := os.ReadDir(filepath.Join(runtime.opts.StateDir, snapshotsSubdir, "app"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("快照目录应恰好 2 个，实际 %d: %v", len(entries), entries)
	}
}
