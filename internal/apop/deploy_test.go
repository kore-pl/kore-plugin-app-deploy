package apop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestDeployNginxHappyPath 覆盖单容器模板的完整部署：结果、argv、记录、权限、挂载种子页。
func TestDeployNginxHappyPath(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	raw := callOK(t, rt, "app-deploy.deploy", `{"template":"nginx-static","name":"kore-app-test"}`)
	var res DeployResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("解析结果失败: %v（%s）", err, raw)
	}
	if !res.Deployed || res.Template != "nginx-static" || res.Name != "kore-app-test" {
		t.Fatalf("结果基础字段不符: %+v", res)
	}
	if len(res.ContainerID) < 12 {
		t.Fatalf("container_id 不是完整容器 ID: %q", res.ContainerID)
	}
	if res.AccessURL != "http://127.0.0.1:18090/" {
		t.Fatalf("access_url=%q，期望 http://127.0.0.1:18090/", res.AccessURL)
	}
	if len(res.Ports) != 1 || res.Ports[0].HostPort != 18090 || res.Ports[0].ContainerPort != 80 {
		t.Fatalf("ports 不符: %+v", res.Ports)
	}
	if len(res.Containers) != 1 || res.Containers[0].Name != "kore-app-test" || res.Containers[0].Service != "web" {
		t.Fatalf("containers 不符: %+v", res.Containers)
	}

	if len(exec.runSpecs) != 1 {
		t.Fatalf("docker run 调用 %d 次，期望 1", len(exec.runSpecs))
	}
	spec := exec.runSpecs[0]
	if spec.Image != "nginx:alpine" || spec.MemoryMB != 128 {
		t.Fatalf("run 规格镜像/内存不符: %+v", spec)
	}
	if len(spec.Volumes) != 1 || !strings.HasPrefix(spec.Volumes[0].Source, rt.opts.StateDir) {
		t.Fatalf("挂载必须落在状态目录内: %+v", spec.Volumes)
	}
	if len(spec.Ports) != 1 || spec.Ports[0].HostPort != 18090 {
		t.Fatalf("端口绑定不符: %+v", spec.Ports)
	}

	// 记录文件权限：目录 0700、文件 0600。
	dirInfo, err := os.Stat(rt.opts.StateDir)
	if err != nil {
		t.Fatalf("状态目录不存在: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("状态目录权限=%o，期望 700", perm)
	}
	info, err := os.Stat(statePath(rt.opts.StateDir))
	if err != nil {
		t.Fatalf("部署记录不存在: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("部署记录权限=%o，期望 600", perm)
	}

	st := readState(t, rt)
	if len(st.Deployments) != 1 {
		t.Fatalf("记录条数=%d，期望 1", len(st.Deployments))
	}
	rec := st.Deployments[0]
	if rec.Template != "nginx-static" || rec.Name != "kore-app-test" || rec.Status != StatusRunning {
		t.Fatalf("记录字段不符: %+v", rec)
	}
	if rec.ID == "" || rec.CreatedAt == "" {
		t.Fatalf("记录缺少 id/created_at: %+v", rec)
	}
	// 协议要求的字段必须都落盘。
	rawState, err := os.ReadFile(statePath(rt.opts.StateDir))
	if err != nil {
		t.Fatalf("读取记录失败: %v", err)
	}
	for _, key := range []string{`"id"`, `"template"`, `"name"`, `"container_id"`, `"ports"`, `"created_at"`, `"status"`} {
		if !strings.Contains(string(rawState), key) {
			t.Fatalf("记录缺少字段 %s: %s", key, rawState)
		}
	}

	// 静态目录必须被建出来，并放入占位首页（空目录会让 nginx 403）。
	index := filepath.Join(rt.opts.StateDir, VolumesDir, "kore-app-test", "html", "index.html")
	page, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("占位首页未生成: %v", err)
	}
	if !strings.Contains(string(page), "kore-app-test") {
		t.Fatalf("占位首页内容不符: %s", page)
	}
}

// TestDeployWordpressDualContainers 覆盖双容器模板：网络、启动顺序、内部 env 打通与端口。
func TestDeployWordpressDualContainers(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	raw := callOK(t, rt, "app-deploy.deploy", `{"template":"wordpress","name":"kore-app-wp"}`)
	var res DeployResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}
	if len(res.Containers) != 2 {
		t.Fatalf("容器数=%d，期望 2（%+v）", len(res.Containers), res.Containers)
	}
	names := map[string]bool{}
	for _, ref := range res.Containers {
		names[ref.Name] = true
	}
	if !names["kore-app-wp"] || !names["kore-app-wp-db"] {
		t.Fatalf("容器名不符: %+v", res.Containers)
	}
	if res.AccessURL != "http://127.0.0.1:18091/" {
		t.Fatalf("access_url=%q", res.AccessURL)
	}
	if len(exec.createdNetworks) != 1 || exec.createdNetworks[0] != "kore-app-wp-net" {
		t.Fatalf("网络创建不符: %+v", exec.createdNetworks)
	}
	// 依赖先起：db 在 web 之前。
	runOrder := make([]string, 0, 2)
	for _, spec := range exec.runSpecs {
		runOrder = append(runOrder, spec.Name)
	}
	if len(runOrder) != 2 || runOrder[0] != "kore-app-wp-db" || runOrder[1] != "kore-app-wp" {
		t.Fatalf("启动顺序不符: %v（依赖容器必须先起）", runOrder)
	}
	for _, spec := range exec.runSpecs {
		if spec.Network != "kore-app-wp-net" {
			t.Fatalf("容器 %s 未加入部署网络: %+v", spec.Name, spec)
		}
	}

	dbEnv := exec.envContents["kore-app-wp-db"]
	if !strings.Contains(dbEnv, "MYSQL_ROOT_PASSWORD=") || !strings.Contains(dbEnv, "MYSQL_PASSWORD=") {
		t.Fatalf("db env 缺少口令项: %q", dbEnv)
	}
	webEnv := exec.envContents["kore-app-wp"]
	if !strings.Contains(webEnv, "WORDPRESS_DB_HOST=kore-app-wp-db:3306") {
		t.Fatalf("web 未指向 db 容器: %q", webEnv)
	}
	if !strings.Contains(webEnv, "WORDPRESS_DB_NAME=wordpress") {
		t.Fatalf("web 未继承数据库名: %q", webEnv)
	}
	for name, mode := range exec.envModes {
		if mode != 0o600 {
			t.Fatalf("容器 %s 的 env-file 权限=%o，期望 600", name, mode)
		}
	}
	// 口令必须两边一致，否则 WordPress 连不上数据库。
	dbPassword := envValue(dbEnv, "MYSQL_PASSWORD")
	webPassword := envValue(webEnv, "WORDPRESS_DB_PASSWORD")
	if dbPassword == "" || dbPassword != webPassword {
		t.Fatalf("db/web 口令不一致: db=%q web=%q", dbPassword, webPassword)
	}
	// 临时 env 文件必须被清掉。
	entries, err := os.ReadDir(rt.opts.StateDir)
	if err != nil {
		t.Fatalf("读取状态目录失败: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".env-") {
			t.Fatalf("env-file 未清理: %s", entry.Name())
		}
	}
}

// TestDeploySecretsNeverInArgvOrState 证明生成的口令既不进 argv，也不进部署记录。
func TestDeploySecretsNeverInArgvOrState(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy", `{"template":"wordpress","name":"kore-app-wp"}`)

	dbEnv := exec.envContents["kore-app-wp-db"]
	rootPassword := envValue(dbEnv, "MYSQL_ROOT_PASSWORD")
	if len(rootPassword) < 16 {
		t.Fatalf("生成口令过短: %q", rootPassword)
	}
	argv := exec.argvJoined()
	if strings.Contains(argv, rootPassword) {
		t.Fatalf("口令出现在 docker argv 中: %s", argv)
	}
	stateRaw, err := os.ReadFile(statePath(rt.opts.StateDir))
	if err != nil {
		t.Fatalf("读取记录失败: %v", err)
	}
	if strings.Contains(string(stateRaw), rootPassword) {
		t.Fatalf("口令落盘到部署记录: %s", stateRaw)
	}
}

// TestDeployIgnoresCustomImage 证明 deploy 不接受自定义镜像：额外字段被忽略，镜像仍来自模板。
func TestDeployIgnoresCustomImage(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy",
		`{"template":"nginx-static","name":"kore-app-x","image":"evil/image:1","cmd":["sh","-c","bad"]}`)
	if len(exec.runSpecs) != 1 {
		t.Fatalf("run 调用次数=%d", len(exec.runSpecs))
	}
	if exec.runSpecs[0].Image != "nginx:alpine" {
		t.Fatalf("镜像被调用方覆盖为 %q", exec.runSpecs[0].Image)
	}
	if len(exec.runSpecs[0].Cmd) != 0 {
		t.Fatalf("命令被调用方覆盖: %v", exec.runSpecs[0].Cmd)
	}
}

// TestDeployExplicitPortAndVolumeOverride 覆盖显式端口与挂载子目录。
func TestDeployExplicitPortAndVolumeOverride(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	raw := callOK(t, rt, "app-deploy.deploy",
		`{"template":"nginx-static","name":"kore-app-x","ports":[{"container_port":80,"host_port":20000}],"volumes":[{"name":"html","subdir":"site-1"}]}`)
	var res DeployResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if res.Ports[0].HostPort != 20000 || res.AccessURL != "http://127.0.0.1:20000/" {
		t.Fatalf("显式端口未生效: %+v / %q", res.Ports, res.AccessURL)
	}
	host := filepath.Join(rt.opts.StateDir, VolumesDir, "kore-app-x", "site-1")
	if _, err := os.Stat(host); err != nil {
		t.Fatalf("自定义挂载目录未创建: %v", err)
	}
}

// TestDeployPortConflict 覆盖显式端口被占用与自动顺延。
func TestDeployPortConflict(t *testing.T) {
	exec := newFakeExecutor()
	exec.addState(ContainerState{Name: "other-web", State: StatusRunning, Ports: "127.0.0.1:20000->80/tcp, 127.0.0.1:18090->80/tcp"})
	rt := testRuntime(t, exec)
	callErr(t, rt, "app-deploy.deploy",
		`{"template":"nginx-static","name":"kore-app-x","ports":[{"container_port":80,"host_port":20000}]}`,
		pluginapi.E_CONFLICT)
	// 显式冲突不应留下任何容器。
	if len(exec.runSpecs) != 0 {
		t.Fatalf("端口冲突后仍执行了 docker run: %+v", exec.runSpecs)
	}
	// 未指定端口 → 自动顺延到下一个可用端口。
	raw := callOK(t, rt, "app-deploy.deploy", `{"template":"nginx-static","name":"kore-app-y"}`)
	var res DeployResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if res.Ports[0].HostPort != 18091 {
		t.Fatalf("自动顺延端口=%d，期望 18091", res.Ports[0].HostPort)
	}
}

// TestDeployNameConflicts 覆盖同名部署与同名容器。
func TestDeployNameConflicts(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy", `{"template":"nginx-static","name":"kore-app-x"}`)
	callErr(t, rt, "app-deploy.deploy", `{"template":"redis","name":"kore-app-x"}`, pluginapi.E_CONFLICT)

	other := newFakeExecutor()
	other.addState(ContainerState{Name: "kore-app-y", State: StatusRunning})
	rt2 := testRuntime(t, other)
	callErr(t, rt2, "app-deploy.deploy", `{"template":"nginx-static","name":"kore-app-y"}`, pluginapi.E_CONFLICT)
}

// TestDeployRejects 覆盖模板/名称/env 的拒绝路径。
func TestDeployRejects(t *testing.T) {
	cases := []struct{ name, params, code string }{
		{"未知模板", `{"template":"evil/image","name":"kore-app-x"}`, pluginapi.E_INVALID},
		{"空模板", `{"template":"","name":"kore-app-x"}`, pluginapi.E_INVALID},
		{"缺 name", `{"template":"nginx-static"}`, pluginapi.E_INVALID},
		{"name 无前缀", `{"template":"nginx-static","name":"blog"}`, pluginapi.E_INVALID},
		{"name 含注入", `{"template":"nginx-static","name":"kore-app-x;rm -rf /"}`, pluginapi.E_INVALID},
		{"未声明的 env", `{"template":"nginx-static","name":"kore-app-x","env":{"FOO":"bar"}}`, pluginapi.E_INVALID},
		{"env 元字符", `{"template":"postgres","name":"kore-app-x","env":{"POSTGRES_PASSWORD":"a;b"}}`, pluginapi.E_INVALID},
		{"缺必填 env", `{"template":"postgres","name":"kore-app-x"}`, pluginapi.E_INVALID},
		{"特权端口", `{"template":"nginx-static","name":"kore-app-x","ports":[{"container_port":80,"host_port":80}]}`, pluginapi.E_INVALID},
		{"未声明的卷", `{"template":"nginx-static","name":"kore-app-x","volumes":[{"name":"data","subdir":"d"}]}`, pluginapi.E_INVALID},
		{"params 非对象", `["nginx"]`, pluginapi.E_INVALID},
	}
	for _, c := range cases {
		exec := newFakeExecutor()
		rt := testRuntime(t, exec)
		callErr(t, rt, "app-deploy.deploy", c.params, c.code)
		if len(exec.runSpecs) != 0 {
			t.Fatalf("%s：校验失败后不得执行 docker run", c.name)
		}
	}
}

// TestDeployPostgresRequiresPassword 覆盖必填 env 的完整路径。
func TestDeployPostgresRequiresPassword(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callErr(t, rt, "app-deploy.deploy", `{"template":"postgres","name":"kore-app-pg"}`, pluginapi.E_INVALID)
	raw := callOK(t, rt, "app-deploy.deploy",
		`{"template":"postgres","name":"kore-app-pg","env":{"POSTGRES_PASSWORD":"PgPass-2026"}}`)
	var res DeployResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if res.AccessURL != "postgres://127.0.0.1:15432/" {
		t.Fatalf("access_url=%q", res.AccessURL)
	}
	if !strings.Contains(exec.envContents["kore-app-pg"], "POSTGRES_PASSWORD=PgPass-2026") {
		t.Fatalf("必填口令未注入: %q", exec.envContents["kore-app-pg"])
	}
}

// TestDeployRollbackOnRunFailure 覆盖部署中途失败的回滚（不留半拉部署）。
func TestDeployRollbackOnRunFailure(t *testing.T) {
	exec := newFakeExecutor()
	exec.failOn["kore-app-wp"] = NewFaultError(FaultKill, "web 容器启动被信号 9 终止")
	rt := testRuntime(t, exec)
	_, perr := rt.Call("app-deploy.deploy", json.RawMessage(`{"template":"wordpress","name":"kore-app-wp"}`))
	if perr == nil || perr.Code != pluginapi.E_UPSTREAM {
		t.Fatalf("期望 E_UPSTREAM，实际 %+v", perr)
	}
	if len(exec.removedContainers) != 1 {
		t.Fatalf("已起的 db 容器未被回滚: %+v", exec.removedContainers)
	}
	if len(exec.removedNetworks) != 1 || exec.removedNetworks[0] != "kore-app-wp-net" {
		t.Fatalf("部署网络未被回滚: %+v", exec.removedNetworks)
	}
	st := readState(t, rt)
	if len(st.Deployments) != 0 {
		t.Fatalf("失败后不应写记录: %+v", st.Deployments)
	}
}

// TestDeployCorruptStateRefused 记录损坏时拒绝部署（不静默重置，避免孤儿容器）。
func TestDeployCorruptStateRefused(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	if err := ensureStateDir(rt.opts.StateDir); err != nil {
		t.Fatalf("建状态目录失败: %v", err)
	}
	if err := os.WriteFile(statePath(rt.opts.StateDir), []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("写坏记录失败: %v", err)
	}
	callErr(t, rt, "app-deploy.deploy", `{"template":"nginx-static","name":"kore-app-x"}`, pluginapi.E_INTERNAL)
	if len(exec.runSpecs) != 0 {
		t.Fatal("记录损坏时不得部署")
	}
}

// envValue 从 KEY=VALUE 行里取值。
func envValue(env, key string) string {
	for _, line := range strings.Split(env, "\n") {
		if value, ok := strings.CutPrefix(line, key+"="); ok {
			return value
		}
	}
	return ""
}
