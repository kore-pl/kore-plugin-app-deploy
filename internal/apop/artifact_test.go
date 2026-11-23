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
