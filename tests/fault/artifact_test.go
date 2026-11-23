package fault

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-app-deploy/internal/apop"
)

// 本文件覆盖 G-20 审计指出的四个新方法（upload / deployFromArtifact / upgrade / jobs）
// 的故障路径。它们不触达 docker 执行面，故障注入点是**插件自有的持久化与数据根**：
//   1. 数据根不可写（磁盘/权限故障）→ 上报 E_INTERNAL，不留下半成品目录；
//   2. 状态目录不可用（挂载丢失）→ 上报 E_INTERNAL，宿主状态文件原样保留；
//   3. 状态文件损坏 → 上报 E_INTERNAL 且**拒绝自动重置/覆盖**（否则部署归属丢失）；
//   4. 升级暂存失败 → 旧版本目录与内容必须原样保留（回滚边界）。
// 断言的是错误码 + 落盘副作用，不是「没 panic」。

// zipArtifact 构造确定性 zip 包（按文件名排序写入）。
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
			t.Fatalf("构造 zip 条目失败: %v", err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatalf("写入 zip 条目失败: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	return buf.Bytes()
}

// artifactParamsJSON 组装 upload / upgrade 的 JSON 入参。
func artifactParamsJSON(t *testing.T, name string, data []byte) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]string{
		"name":           name,
		"format":         apop.ArtifactFormatZip,
		"content_base64": base64.StdEncoding.EncodeToString(data),
	})
	if err != nil {
		t.Fatalf("序列化入参失败: %v", err)
	}
	return raw
}

// newArtifactRuntime 构造数据根/状态目录均在临时目录内的运行时。
func newArtifactRuntime(t *testing.T) (rt *apop.Runtime, root, state string) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "root")
	state = filepath.Join(dir, "state")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("创建数据根失败: %v", err)
	}
	rt = apop.NewRuntime(apop.Options{
		Root:      root,
		StateDir:  state,
		GOOS:      "linux",
		PortProbe: func(int) bool { return true },
	}, &okExecutor{})
	return rt, root, state
}

// mustUpload 先上传一个应用包，作为后续故障用例的前置状态。
func mustUpload(t *testing.T, rt *apop.Runtime, name string, files map[string]string) {
	t.Helper()
	result, perr := rt.Call("app-deploy.upload", artifactParamsJSON(t, name, zipArtifact(t, files)))
	if perr != nil {
		t.Fatalf("前置 upload 失败: %+v", perr)
	}
	if !strings.Contains(string(result), `"name":"`+name+`"`) {
		t.Fatalf("upload 结果缺 name=%s: %s", name, result)
	}
}

// decodeJobs 读取作业记录；文件不存在返回空。
func decodeJobs(t *testing.T, path string) []apop.ArtifactJob {
	t.Helper()
	data := readFileOrEmpty(t, path)
	if data == nil {
		return nil
	}
	var jobs []apop.ArtifactJob
	if err := json.Unmarshal(data, &jobs); err != nil {
		t.Fatalf("作业记录不是合法 JSON: %v（%s）", err, data)
	}
	return jobs
}

// readFileOrEmpty 读取文件内容；不存在返回 nil。
func readFileOrEmpty(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("读取 %s 失败: %v", path, err)
	}
	return data
}

// TestUploadDataRootReadOnly 注入数据根不可写：upload 回 E_INTERNAL，
// 且不留下应用目录、不写状态目录（故障点发生在任何落盘之前）。
func TestUploadDataRootReadOnly(t *testing.T) {
	rt, root, state := newArtifactRuntime(t)
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatalf("设置数据根只读失败: %v", err)
	}
	defer func() { _ = os.Chmod(root, 0o755) }()

	result, perr := rt.Call("app-deploy.upload", artifactParamsJSON(t, "ro-app", zipArtifact(t, map[string]string{"index.html": "hi"})))
	if perr == nil {
		t.Fatalf("数据根只读时 upload 未报错: %s", result)
	}
	if result != nil {
		t.Fatalf("upload 失败时不应带结果: %s", result)
	}
	if perr.Code != "E_INTERNAL" {
		t.Fatalf("错误码=%q，期望 E_INTERNAL（message=%q）", perr.Code, perr.Message)
	}
	if !strings.Contains(perr.Message, "暂存目录") {
		t.Fatalf("错误信息未指向暂存目录故障: %q", perr.Message)
	}
	verifyErrorContract(t, perr)

	if _, err := os.Stat(filepath.Join(root, "ro-app")); !os.IsNotExist(err) {
		t.Fatalf("数据根只读时仍留下应用目录: err=%v", err)
	}
	// 失败阶段允许落一条 failed 作业记录（供 UI 轮询），但不得记为成功。
	jobs := decodeJobs(t, filepath.Join(state, "artifact-jobs.json"))
	for _, job := range jobs {
		if job.Name == "ro-app" && job.Status != apop.JobFailed {
			t.Fatalf("故障作业状态=%q，期望 failed: %+v", job.Status, job)
		}
	}
	if _, err := os.Stat(filepath.Join(state, apop.DeploymentsFile)); !os.IsNotExist(err) {
		t.Fatalf("故障时不应写部署记录: err=%v", err)
	}
}

// TestUploadStateStoreUnavailable 注入状态目录不可用（路径被普通文件占用）：
// upload 回 E_INTERNAL，宿主状态文件不被删除/覆盖。
func TestUploadStateStoreUnavailable(t *testing.T) {
	rt, root, state := newArtifactRuntime(t)
	sentinel := []byte(`不是目录`)
	if err := os.WriteFile(state, sentinel, 0o600); err != nil {
		t.Fatalf("预置状态路径失败: %v", err)
	}

	result, perr := rt.Call("app-deploy.upload", artifactParamsJSON(t, "nostate", zipArtifact(t, map[string]string{"a.txt": "x"})))
	if perr == nil {
		t.Fatalf("状态目录不可用时 upload 未报错: %s", result)
	}
	if perr.Code != "E_INTERNAL" {
		t.Fatalf("错误码=%q，期望 E_INTERNAL（message=%q）", perr.Code, perr.Message)
	}
	if !strings.Contains(perr.Message, "作业记录") {
		t.Fatalf("错误信息未指向状态存储故障: %q", perr.Message)
	}
	verifyErrorContract(t, perr)

	if got := readFileOrEmpty(t, state); !bytes.Equal(got, sentinel) {
		t.Fatalf("状态路径被改写: got=%q want=%q", got, sentinel)
	}
	// 数据根内的应用目录是解压阶段的正常产物，但不得出现在宿主其它位置；
	// 这里断言它确实落在配置的数据根内。
	if _, err := os.Stat(filepath.Join(root, "nostate")); err != nil {
		t.Fatalf("应用目录应落在数据根内: %v", err)
	}
}

// TestDeployFromArtifactCorruptState 注入部署记录损坏：deployFromArtifact 回 E_INTERNAL，
// 且损坏文件原样保留（拒绝自动重置），作业记录不被追加。
func TestDeployFromArtifactCorruptState(t *testing.T) {
	rt, _, state := newArtifactRuntime(t)
	mustUpload(t, rt, "corrupt-app", map[string]string{"index.html": "v1"})

	statePath := filepath.Join(state, apop.DeploymentsFile)
	corrupt := []byte("{ this is not json")
	if err := os.WriteFile(statePath, corrupt, 0o600); err != nil {
		t.Fatalf("写入损坏部署记录失败: %v", err)
	}
	jobsBefore := readFileOrEmpty(t, filepath.Join(state, "artifact-jobs.json"))

	result, perr := rt.Call("app-deploy.deployFromArtifact", json.RawMessage(`{"name":"corrupt-app"}`))
	if perr == nil {
		t.Fatalf("部署记录损坏时 deployFromArtifact 未报错: %s", result)
	}
	if perr.Code != "E_INTERNAL" {
		t.Fatalf("错误码=%q，期望 E_INTERNAL（message=%q）", perr.Code, perr.Message)
	}
	if !strings.Contains(perr.Message, "损坏") || !strings.Contains(perr.Message, "拒绝自动重置") {
		t.Fatalf("错误信息未声明拒绝自动重置: %q", perr.Message)
	}
	verifyErrorContract(t, perr)

	if got := readFileOrEmpty(t, statePath); !bytes.Equal(got, corrupt) {
		t.Fatalf("损坏的部署记录被覆盖: got=%q want=%q", got, corrupt)
	}
	jobsAfter := readFileOrEmpty(t, filepath.Join(state, "artifact-jobs.json"))
	if jobsAfter == nil {
		t.Fatalf("失败路径不应删除既有作业记录")
	}
	var jobs []apop.ArtifactJob
	if err := json.Unmarshal(jobsAfter, &jobs); err != nil {
		t.Fatalf("作业记录被写坏: %v（%s）", err, jobsAfter)
	}
	for _, job := range jobs {
		if job.Name == "corrupt-app" && job.Action == "deployFromArtifact" && job.Status == apop.JobSucceeded {
			t.Fatalf("失败的 deployFromArtifact 被记为 succeeded: %+v", job)
		}
	}
	if len(jobsBefore) == 0 {
		t.Fatalf("前置 upload 应留下作业记录")
	}
}

// TestUpgradeDataRootReadOnly 注入升级时数据根不可写：回 E_INTERNAL，
// 旧版本目录与内容原样保留，且不残留 .staging-* 临时目录。
func TestUpgradeDataRootReadOnly(t *testing.T) {
	rt, root, _ := newArtifactRuntime(t)
	mustUpload(t, rt, "keep-app", map[string]string{"v.txt": "v1", "index.html": "old"})

	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatalf("设置数据根只读失败: %v", err)
	}
	defer func() { _ = os.Chmod(root, 0o755) }()

	result, perr := rt.Call("app-deploy.upgrade", artifactParamsJSON(t, "keep-app", zipArtifact(t, map[string]string{"v.txt": "v2"})))
	if perr == nil {
		t.Fatalf("数据根只读时 upgrade 未报错: %s", result)
	}
	if perr.Code != "E_INTERNAL" {
		t.Fatalf("错误码=%q，期望 E_INTERNAL（message=%q）", perr.Code, perr.Message)
	}
	if !strings.Contains(perr.Message, "升级暂存目录") {
		t.Fatalf("错误信息未指向升级暂存故障: %q", perr.Message)
	}
	verifyErrorContract(t, perr)

	if got := string(readFileOrEmpty(t, filepath.Join(root, "keep-app", "v.txt"))); got != "v1" {
		t.Fatalf("升级失败后旧版本被破坏: v.txt=%q，期望 v1", got)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("读取数据根失败: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "staging") {
			t.Fatalf("升级失败后残留暂存目录: %s", entry.Name())
		}
	}
}

// TestJobsCorruptStore 注入作业记录损坏：jobs 回 E_INTERNAL 且文件原样保留。
func TestJobsCorruptStore(t *testing.T) {
	rt, _, state := newArtifactRuntime(t)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("创建状态目录失败: %v", err)
	}
	corrupt := []byte(`{"not":"an array"}`)
	jobsPath := filepath.Join(state, "artifact-jobs.json")
	if err := os.WriteFile(jobsPath, corrupt, 0o600); err != nil {
		t.Fatalf("写入损坏作业记录失败: %v", err)
	}

	result, perr := rt.Call("app-deploy.jobs", json.RawMessage(`{}`))
	if perr == nil {
		t.Fatalf("作业记录损坏时 jobs 未报错: %s", result)
	}
	if perr.Code != "E_INTERNAL" {
		t.Fatalf("错误码=%q，期望 E_INTERNAL（message=%q）", perr.Code, perr.Message)
	}
	if !strings.Contains(perr.Message, "作业记录损坏") {
		t.Fatalf("错误信息未指向作业记录损坏: %q", perr.Message)
	}
	verifyErrorContract(t, perr)

	if got := readFileOrEmpty(t, jobsPath); !bytes.Equal(got, corrupt) {
		t.Fatalf("损坏的作业记录被覆盖: got=%q want=%q", got, corrupt)
	}
}

// TestJobsNoStoreIsEmpty 是反面对照：作业记录不存在是空状态而非故障，
// 防止上面的损坏用例靠「永远报错」假通过。
func TestJobsNoStoreIsEmpty(t *testing.T) {
	rt, _, _ := newArtifactRuntime(t)
	result, perr := rt.Call("app-deploy.jobs", json.RawMessage(`{}`))
	if perr != nil {
		t.Fatalf("空作业记录应正常返回: %+v", perr)
	}
	if !strings.Contains(string(result), `"count":0`) {
		t.Fatalf("空作业记录 count 应为 0: %s", result)
	}
}

// TestArtifactMethodsSeeDataRootFault 汇总断言四个方法在数据根不可写时
// 都不会 panic，且 upload/upgrade 明确回错误（jobs 只读仍可用）。
func TestArtifactMethodsSeeDataRootFault(t *testing.T) {
	rt, root, _ := newArtifactRuntime(t)
	mustUpload(t, rt, "f2", map[string]string{"a": "old"})
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatalf("设置数据根只读失败: %v", err)
	}
	defer func() { _ = os.Chmod(root, 0o755) }()

	for _, call := range []struct {
		method string
		params json.RawMessage
		code   string
	}{
		{"app-deploy.upload", artifactParamsJSON(t, "f1", zipArtifact(t, map[string]string{"a": "b"})), "E_INTERNAL"},
		{"app-deploy.upgrade", artifactParamsJSON(t, "f2", zipArtifact(t, map[string]string{"a": "b"})), "E_INTERNAL"},
		{"app-deploy.deployFromArtifact", json.RawMessage(`{"name":"f3"}`), "E_NOTFOUND"},
	} {
		result, perr := rt.Call(call.method, call.params)
		if perr == nil {
			t.Fatalf("%s 在数据根只读时应失败: %s", call.method, result)
		}
		if perr.Code != call.code {
			t.Fatalf("%s 错误码=%q，期望 %q（message=%q）", call.method, perr.Code, call.code, perr.Message)
		}
	}
	if _, perr := rt.Call("app-deploy.jobs", json.RawMessage(`{}`)); perr != nil {
		t.Fatalf("只读 jobs 在数据根只读时不应失败: %+v", perr)
	}
}
