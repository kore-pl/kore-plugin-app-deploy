//go:build linux

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 最小结果形状：只解析 E2E 需要断言的字段（显式列出，不用 map）。
type e2eTemplates struct {
	Templates []struct {
		ID                string `json:"id"`
		Name              string `json:"name"`
		Description       string `json:"description"`
		Image             string `json:"image"`
		EstimatedMemoryMB int    `json:"estimated_memory_mb"`
		Ports             []struct {
			ContainerPort int `json:"container_port"`
			DefaultHost   int `json:"default_host_port"`
		} `json:"ports"`
		Volumes []struct {
			Name          string `json:"name"`
			ContainerPath string `json:"container_path"`
		} `json:"volumes"`
		Env []struct {
			Key      string `json:"key"`
			Label    string `json:"label"`
			Required bool   `json:"required"`
			Default  string `json:"default"`
			Help     string `json:"help"`
		} `json:"env"`
	} `json:"templates"`
	Count int `json:"count"`
}

type e2eDeploy struct {
	Deployed    bool   `json:"deployed"`
	ID          string `json:"id"`
	Template    string `json:"template"`
	Name        string `json:"name"`
	ContainerID string `json:"container_id"`
	AccessURL   string `json:"access_url"`
	Ports       []struct {
		Service       string `json:"service"`
		ContainerPort int    `json:"container_port"`
		HostPort      int    `json:"host_port"`
		AccessURL     string `json:"access_url"`
	} `json:"ports"`
	Containers []struct {
		Service string `json:"service"`
		Name    string `json:"name"`
		ID      string `json:"id"`
	} `json:"containers"`
	CreatedAt string `json:"created_at"`
}

type e2eList struct {
	Deployments []struct {
		Name       string `json:"name"`
		Template   string `json:"template"`
		Status     string `json:"status"`
		AccessURL  string `json:"access_url"`
		CreatedAt  string `json:"created_at"`
		Containers []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"containers"`
	} `json:"deployments"`
	Count int `json:"count"`
}

type e2eRemove struct {
	Removed    bool     `json:"removed"`
	Name       string   `json:"name"`
	Containers []string `json:"containers"`
	Volumes    []string `json:"volumes"`
	Network    string   `json:"network"`
	Warnings   []string `json:"warnings"`
}

// TestAppDeployDockerE2E 真实 docker 全流程：模板 → nginx 部署 → curl → list → wordpress 双容器
// → curl → 端口冲突 → 非本插件容器护栏 → remove confirm 护栏 → 清理。
func TestAppDeployDockerE2E(t *testing.T) {
	if os.Getenv("KORE_APP_E2E") != "1" {
		t.Skip("设置 KORE_APP_E2E=1 后运行真实 docker E2E")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("KORE_APP_E2E=1 但未找到 docker CLI：E2E 不允许静默降级为跳过")
	}

	// 前置清理 + 失败兜底清理（只动 E2E 自己的名字）。
	cleanTestResources(t)
	t.Cleanup(func() { cleanTestResources(t) })

	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	binary := buildPlugin(t)
	socket := filepath.Join(t.TempDir(), "app-deploy.sock")
	listener := listen(t, socket)
	process, stderr := startPlugin(t, binary, socket, root, stateDir)
	conn := accept(t, listener, stderr)
	handshake(t, conn)
	t.Cleanup(func() {
		shutdown(conn)
		_ = process.Wait()
	})

	// 1) templates：四个内置模板，字段齐全。
	templatesRaw := mustCall(t, conn, "e2e-templates", "app-deploy.templates", map[string]any{})
	t.Logf("E2E_TEMPLATES_JSON %s", templatesRaw)
	var templates e2eTemplates
	if err := json.Unmarshal([]byte(templatesRaw), &templates); err != nil {
		t.Fatalf("解析 templates 失败: %v", err)
	}
	if templates.Count != 4 || len(templates.Templates) != 4 {
		t.Fatalf("模板数量=%d/%d，期望 4", templates.Count, len(templates.Templates))
	}
	wantIDs := []string{"nginx-static", "wordpress", "redis", "postgres"}
	for i, id := range wantIDs {
		if templates.Templates[i].ID != id {
			t.Fatalf("模板 %d id=%q，期望 %q", i, templates.Templates[i].ID, id)
		}
		tpl := templates.Templates[i]
		if tpl.Name == "" || tpl.Description == "" || tpl.Image == "" || tpl.EstimatedMemoryMB <= 0 {
			t.Fatalf("模板 %s 字段不全: %+v", id, tpl)
		}
		if len(tpl.Ports) == 0 || len(tpl.Volumes) == 0 {
			t.Fatalf("模板 %s 缺 ports/volumes: %+v", id, tpl)
		}
	}
	// wordpress 的 env 必须带 label/help（UI 需要），postgres 必须有必填项。
	for _, field := range templates.Templates[1].Env {
		if field.Label == "" || field.Help == "" {
			t.Fatalf("wordpress env %s 缺 label/help", field.Key)
		}
	}
	postgresHasRequired := false
	for _, field := range templates.Templates[3].Env {
		if field.Required && field.Key == "POSTGRES_PASSWORD" {
			postgresHasRequired = true
		}
	}
	if !postgresHasRequired {
		t.Fatal("postgres 模板缺少必填 POSTGRES_PASSWORD")
	}

	// 2) deploy nginx-static → 容器起来 → curl 200。
	nginxRaw := mustCall(t, conn, "e2e-deploy-nginx", "app-deploy.deploy",
		map[string]any{"template": "nginx-static", "name": "kore-app-test"})
	t.Logf("E2E_DEPLOY_NGINX_JSON %s", nginxRaw)
	var nginx e2eDeploy
	if err := json.Unmarshal([]byte(nginxRaw), &nginx); err != nil {
		t.Fatalf("解析 deploy 失败: %v", err)
	}
	if !nginx.Deployed || nginx.Name != "kore-app-test" || nginx.Template != "nginx-static" {
		t.Fatalf("deploy 结果不符: %+v", nginx)
	}
	if len(nginx.ContainerID) != 64 {
		t.Fatalf("container_id 长度=%d，期望 64（%q）", len(nginx.ContainerID), nginx.ContainerID)
	}
	if len(nginx.Ports) != 1 || nginx.Ports[0].HostPort < 1024 {
		t.Fatalf("ports 不符（不得使用特权端口）: %+v", nginx.Ports)
	}
	if !strings.HasPrefix(nginx.AccessURL, "http://127.0.0.1:") {
		t.Fatalf("access_url=%q，期望回环地址", nginx.AccessURL)
	}
	waitDockerState(t, "kore-app-test", "running", 30*time.Second)

	// docker 侧交叉核对：内存限额、端口仅绑回环、管理标签。
	memory := strings.TrimSpace(dockerOutput(t, "inspect", "--format", "{{.HostConfig.Memory}}", "kore-app-test"))
	if memory != "134217728" { // 128 MiB
		t.Fatalf("容器内存限额=%s，期望 134217728", memory)
	}
	bindings := dockerOutput(t, "inspect", "--format", "{{json .HostConfig.PortBindings}}", "kore-app-test")
	if !strings.Contains(bindings, `"HostIp":"127.0.0.1"`) {
		t.Fatalf("端口绑定未限制在回环: %s", bindings)
	}
	if strings.Contains(bindings, "0.0.0.0") {
		t.Fatalf("端口绑定暴露到 0.0.0.0: %s", bindings)
	}
	label := strings.TrimSpace(dockerOutput(t, "inspect", "--format", `{{index .Config.Labels "kore.app-deploy.managed"}}`, "kore-app-test"))
	if label != "true" {
		t.Fatalf("容器缺少管理标签: %q", label)
	}

	body := waitHTTP200(t, nginx.AccessURL, "kore app-deploy", 60*time.Second)
	t.Logf("E2E_CURL_NGINX url=%s status=200 body=%q", nginx.AccessURL, truncate(body, 200))

	// 部署记录：文件存在、0600、内容含本次部署。
	recordPath := filepath.Join(stateDir, "deployments.json")
	info, err := os.Stat(recordPath)
	if err != nil {
		t.Fatalf("部署记录不存在: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("部署记录权限=%o，期望 600", perm)
	}
	recordRaw, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("读取部署记录失败: %v", err)
	}
	for _, key := range []string{`"kore-app-test"`, `"nginx-static"`, `"container_id"`, `"ports"`, `"created_at"`, `"status"`} {
		if !strings.Contains(string(recordRaw), key) {
			t.Fatalf("部署记录缺少 %s: %s", key, recordRaw)
		}
	}
	t.Logf("E2E_RECORD_JSON %s", strings.TrimSpace(string(recordRaw)))

	// 3) list 显示 running。
	listRaw := mustCall(t, conn, "e2e-list-1", "app-deploy.list", map[string]any{})
	t.Logf("E2E_LIST_JSON %s", listRaw)
	var listed e2eList
	if err := json.Unmarshal([]byte(listRaw), &listed); err != nil {
		t.Fatalf("解析 list 失败: %v", err)
	}
	if listed.Count != 1 || listed.Deployments[0].Status != "running" {
		t.Fatalf("list 状态不符: %+v", listed)
	}

	// 4) 端口冲突：用同一个宿主端口再部署一次 → E_CONFLICT。
	expectError(t, conn, "e2e-port-conflict", "app-deploy.deploy", map[string]any{
		"template": "nginx-static",
		"name":     "kore-app-test2",
		"ports":    []map[string]any{{"container_port": 80, "host_port": nginx.Ports[0].HostPort}},
	}, "E_CONFLICT")
	if hasDockerName(t, "kore-app-test2") {
		t.Fatal("端口冲突不应创建容器 kore-app-test2")
	}

	// 5) 非本插件容器：docker 里存在但无部署记录 → E_UNAUTHORIZED，且不被删除。
	if _, err := runDocker("run", "-d", "--name", "kore-app-foreign", "nginx:alpine"); err != nil {
		t.Fatalf("创建外部容器失败: %v", err)
	}
	expectError(t, conn, "e2e-foreign", "app-deploy.remove",
		map[string]any{"name": "kore-app-foreign", "confirm": true}, "E_UNAUTHORIZED")
	if dockerState(t, "kore-app-foreign") != "running" {
		t.Fatal("拒绝删除后外部容器应仍存在")
	}
	if _, err := runDocker("rm", "-f", "kore-app-foreign"); err != nil {
		t.Fatalf("清理外部容器失败: %v", err)
	}

	// 6) deploy wordpress → 双容器都起来 → curl 首页有内容。
	wpRaw := mustCall(t, conn, "e2e-deploy-wp", "app-deploy.deploy",
		map[string]any{"template": "wordpress", "name": "kore-app-wp"})
	t.Logf("E2E_DEPLOY_WP_JSON %s", wpRaw)
	var wp e2eDeploy
	if err := json.Unmarshal([]byte(wpRaw), &wp); err != nil {
		t.Fatalf("解析 wordpress deploy 失败: %v", err)
	}
	if !wp.Deployed || len(wp.Containers) != 2 {
		t.Fatalf("wordpress 应为双容器: %+v", wp)
	}
	names := map[string]bool{}
	for _, ref := range wp.Containers {
		names[ref.Name] = true
		if len(ref.ID) != 64 {
			t.Fatalf("容器 %s 的 ID 不是完整 64 位: %q", ref.Name, ref.ID)
		}
	}
	if !names["kore-app-wp"] || !names["kore-app-wp-db"] {
		t.Fatalf("wordpress 容器名不符: %+v", names)
	}
	waitDockerState(t, "kore-app-wp-db", "running", 60*time.Second)
	waitDockerState(t, "kore-app-wp", "running", 60*time.Second)
	wpBody := waitHTTP200(t, wp.AccessURL, "WordPress", 300*time.Second)
	if len(wpBody) < 500 {
		t.Fatalf("wordpress 首页内容过短（%d 字节）: %q", len(wpBody), truncate(wpBody, 200))
	}
	t.Logf("E2E_CURL_WP url=%s status=200 body_len=%d body_head=%q", wp.AccessURL, len(wpBody), truncate(wpBody, 300))

	// 7) remove 不带 confirm → E_INVALID，容器不受影响。
	expectError(t, conn, "e2e-remove-noconfirm", "app-deploy.remove",
		map[string]any{"name": "kore-app-test"}, "E_INVALID")
	if dockerState(t, "kore-app-test") != "running" {
		t.Fatal("未确认的 remove 不应停止容器")
	}

	// 8) remove 带 confirm → 容器与记录一起删除。
	removeRaw := mustCall(t, conn, "e2e-remove-nginx", "app-deploy.remove",
		map[string]any{"name": "kore-app-test", "confirm": true})
	t.Logf("E2E_REMOVE_NGINX_JSON %s", removeRaw)
	var removed e2eRemove
	if err := json.Unmarshal([]byte(removeRaw), &removed); err != nil {
		t.Fatalf("解析 remove 失败: %v", err)
	}
	if !removed.Removed || removed.Name != "kore-app-test" {
		t.Fatalf("remove 结果不符: %+v", removed)
	}
	waitDockerGone(t, "kore-app-test", 30*time.Second)

	// 9) wordpress remove：容器 + 网络 + 命名卷一起清理。
	wpRemoveRaw := mustCall(t, conn, "e2e-remove-wp", "app-deploy.remove",
		map[string]any{"name": "kore-app-wp", "confirm": true})
	t.Logf("E2E_REMOVE_WP_JSON %s", wpRemoveRaw)
	waitDockerGone(t, "kore-app-wp", 30*time.Second)
	waitDockerGone(t, "kore-app-wp-db", 30*time.Second)

	// 10) list 清空（记录已删除）。
	afterRaw := mustCall(t, conn, "e2e-list-2", "app-deploy.list", map[string]any{})
	if !strings.Contains(afterRaw, `"count":0`) {
		t.Fatalf("清理后 list 应为空: %s", afterRaw)
	}
	afterRecord, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("读取记录失败: %v", err)
	}
	if strings.Contains(string(afterRecord), "kore-app-test") || strings.Contains(string(afterRecord), "kore-app-wp") {
		t.Fatalf("记录未清理: %s", afterRecord)
	}

	// 11) 最终状态：没有任何 E2E 容器残留。
	for _, name := range testContainers {
		if hasDockerName(t, name) {
			t.Fatalf("容器 %s 未清理", name)
		}
	}
	networks := dockerOutput(t, "network", "ls", "--format", "{{.Name}}")
	if strings.Contains(networks, "kore-app-wp-net") {
		t.Fatalf("部署网络未清理: %s", networks)
	}
	volumes := dockerOutput(t, "volume", "ls", "--format", "{{.Name}}")
	if strings.Contains(volumes, "kore-app-wp-") {
		t.Fatalf("命名卷未清理: %s", volumes)
	}
	all := dockerOutput(t, "ps", "-a", "--filter", "name=kore-app-", "--format", "{{.Names}}")
	if strings.TrimSpace(all) != "" {
		t.Fatalf("仍有 kore-app-* 容器残留: %s", all)
	}
	t.Logf("E2E_DONE stderr=%q", truncate(stderr.String(), 400))
}
