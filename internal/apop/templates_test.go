package apop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestBuiltinTemplatesComplete 校验四个内置模板的字段完整性与协议形状。
func TestBuiltinTemplatesComplete(t *testing.T) {
	rt := testRuntime(t, newFakeExecutor())
	raw := callOK(t, rt, "app-deploy.templates", `{}`)
	var got TemplatesResult
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("解析 templates 结果失败: %v（%s）", err, raw)
	}
	// 顺序固定：既有四个模板保持原位（老用户看到的顺序不变），新增模板追加在后。
	wantIDs := []string{"nginx-static", "wordpress", "redis", "postgres", "node-app", "python-app", "php-site", "mysql-phpmyadmin", "uptime-kuma"}
	if got.Count != len(wantIDs) || len(got.Templates) != len(wantIDs) {
		t.Fatalf("模板数量=%d/%d，期望 %d", got.Count, len(got.Templates), len(wantIDs))
	}
	for i, id := range wantIDs {
		if got.Templates[i].ID != id {
			t.Fatalf("第 %d 个模板 id=%q，期望 %q（顺序须稳定）", i, got.Templates[i].ID, id)
		}
	}
	for _, tpl := range got.Templates {
		if tpl.ID == "" || tpl.Name == "" || tpl.Description == "" || tpl.Image == "" {
			t.Fatalf("模板 %s 基础字段不全: %+v", tpl.ID, tpl)
		}
		if tpl.EstimatedMemoryMB <= 0 {
			t.Fatalf("模板 %s 缺少 estimated_memory_mb", tpl.ID)
		}
		if len(tpl.Services) == 0 {
			t.Fatalf("模板 %s 没有服务定义", tpl.ID)
		}
		primary, ok := tpl.PrimaryService()
		if !ok {
			t.Fatalf("模板 %s 缺少主服务", tpl.ID)
		}
		if primary.Image != tpl.Image {
			t.Fatalf("模板 %s 的 image=%q 与主服务镜像 %q 不一致", tpl.ID, tpl.Image, primary.Image)
		}
		for _, svc := range tpl.Services {
			if svc.Image == "" || svc.MemoryMB <= 0 {
				t.Fatalf("模板 %s 的服务 %s 缺少镜像或内存限制: %+v", tpl.ID, svc.Name, svc)
			}
		}
		for _, port := range tpl.Ports {
			if port.ContainerPort < 1 || port.ContainerPort > MaxPort {
				t.Fatalf("模板 %s 容器端口越界: %d", tpl.ID, port.ContainerPort)
			}
			if port.DefaultHost < MinPort || port.DefaultHost > MaxPort {
				t.Fatalf("模板 %s 默认宿主端口 %d 不在 %d..%d", tpl.ID, port.DefaultHost, MinPort, MaxPort)
			}
			if port.Protocol != "tcp" || port.AccessPrefix == "" || port.Label == "" {
				t.Fatalf("模板 %s 端口元信息不全: %+v", tpl.ID, port)
			}
			if _, ok := tpl.Service(port.Service); !ok {
				t.Fatalf("模板 %s 的端口指向不存在的服务 %q", tpl.ID, port.Service)
			}
		}
		for _, vol := range tpl.Volumes {
			if vol.Kind != VolumeBind && vol.Kind != VolumeNamed {
				t.Fatalf("模板 %s 卷类型非法: %q", tpl.ID, vol.Kind)
			}
			if !strings.HasPrefix(vol.ContainerPath, "/") || vol.Name == "" || vol.Description == "" {
				t.Fatalf("模板 %s 卷元信息不全: %+v", tpl.ID, vol)
			}
			if _, ok := tpl.Service(vol.Service); !ok {
				t.Fatalf("模板 %s 的卷指向不存在的服务 %q", tpl.ID, vol.Service)
			}
		}
		seen := map[string]bool{}
		for _, field := range tpl.Env {
			if !envKeyRE.MatchString(field.Key) {
				t.Fatalf("模板 %s 的 env 键非法: %q", tpl.ID, field.Key)
			}
			if field.Label == "" || field.Help == "" {
				t.Fatalf("模板 %s 的 env %s 缺少 label/help", tpl.ID, field.Key)
			}
			if seen[field.Key] {
				t.Fatalf("模板 %s 的 env %s 重复声明", tpl.ID, field.Key)
			}
			seen[field.Key] = true
			if _, ok := tpl.EnvService(field); !ok {
				t.Fatalf("模板 %s 的 env %s 指向不存在的服务", tpl.ID, field.Key)
			}
		}
		// 协议要求的字段名必须原样出现在 JSON 里（空数组也不能退化成 null）。
		data, err := json.Marshal(tpl)
		if err != nil {
			t.Fatalf("序列化模板 %s 失败: %v", tpl.ID, err)
		}
		for _, key := range []string{`"id"`, `"name"`, `"description"`, `"image"`, `"ports"`, `"volumes"`, `"env"`, `"estimated_memory_mb"`} {
			if !strings.Contains(string(data), key) {
				t.Fatalf("模板 %s 的 JSON 缺少字段 %s: %s", tpl.ID, key, data)
			}
		}
		if !strings.Contains(string(data), `"env":[`) {
			t.Fatalf("模板 %s 的 env 必须是数组而非 null: %s", tpl.ID, data)
		}
	}
}

// TestTemplateCatalogDetails 校验四个模板的具体内容（镜像/端口/卷）。
func TestTemplateCatalogDetails(t *testing.T) {
	type portWant struct{ host, container int }
	cases := []struct {
		id          string
		primary     string
		services    []string
		ports       []portWant
		volumePath  string
		wantService int
	}{
		{"nginx-static", "nginx:alpine", []string{"nginx:alpine"}, []portWant{{18090, 80}}, "/usr/share/nginx/html", 1},
		{"wordpress", "wordpress:6", []string{"wordpress:6", "mysql:8"}, []portWant{{18091, 80}}, "/var/www/html", 2},
		{"node-app", "node:20-alpine", []string{"node:20-alpine"}, []portWant{{18092, 3000}}, "/app", 1},
		{"python-app", "python:3.12-alpine", []string{"python:3.12-alpine"}, []portWant{{18093, 8000}}, "/app", 1},
		{"php-site", "php:8.3-apache", []string{"php:8.3-apache"}, []portWant{{18094, 80}}, "/var/www/html", 1},
		{"mysql-phpmyadmin", "phpmyadmin:5", []string{"mysql:8", "phpmyadmin:5"}, []portWant{{18095, 80}, {13306, 3306}}, "/var/lib/mysql", 2},
		{"redis", "redis:7-alpine", []string{"redis:7-alpine"}, []portWant{{16379, 6379}}, "/data", 1},
		{"postgres", "postgres:16-alpine", []string{"postgres:16-alpine"}, []portWant{{15432, 5432}}, "/var/lib/postgresql/data", 1},
		{"uptime-kuma", "louislam/uptime-kuma:1", []string{"louislam/uptime-kuma:1"}, []portWant{{18096, 3001}}, "/app/data", 1},
	}
	for _, c := range cases {
		tpl := LookupTemplate(c.id)
		if tpl == nil {
			t.Fatalf("缺少模板 %s", c.id)
		}
		if tpl.Image != c.primary {
			t.Fatalf("模板 %s image=%q，期望 %q", c.id, tpl.Image, c.primary)
		}
		if len(tpl.Services) != c.wantService {
			t.Fatalf("模板 %s 服务数=%d，期望 %d", c.id, len(tpl.Services), c.wantService)
		}
		for i, image := range c.services {
			if tpl.Services[i].Image != image {
				t.Fatalf("模板 %s 第 %d 个服务镜像=%q，期望 %q", c.id, i, tpl.Services[i].Image, image)
			}
		}
		if len(tpl.Ports) != len(c.ports) {
			t.Fatalf("模板 %s 端口数=%d，期望 %d", c.id, len(tpl.Ports), len(c.ports))
		}
		for i, want := range c.ports {
			if tpl.Ports[i].DefaultHost != want.host || tpl.Ports[i].ContainerPort != want.container {
				t.Fatalf("模板 %s 第 %d 条端口=%d->%d，期望 %d->%d", c.id, i,
					tpl.Ports[i].DefaultHost, tpl.Ports[i].ContainerPort, want.host, want.container)
			}
		}
		if len(tpl.Volumes) == 0 || tpl.Volumes[0].ContainerPath != c.volumePath {
			t.Fatalf("模板 %s 首个卷路径=%v，期望 %q", c.id, tpl.Volumes, c.volumePath)
		}
	}
}

// TestTemplateSecretsAndRequired 校验口令类 env 的 must-provide 语义。
func TestTemplateSecretsAndRequired(t *testing.T) {
	postgres := LookupTemplate("postgres")
	required := ""
	for _, field := range postgres.Env {
		if field.Required {
			if !field.Secret || field.Default != "" {
				t.Fatalf("postgres 必填项 %s 必须是 secret 且无默认值", field.Key)
			}
			required = field.Key
		}
	}
	if required != "POSTGRES_PASSWORD" {
		t.Fatalf("postgres 必填项=%q，期望 POSTGRES_PASSWORD", required)
	}
	pma := LookupTemplate("mysql-phpmyadmin")
	pmaRequired := ""
	for _, field := range pma.Env {
		if field.Required {
			pmaRequired = field.Key
			if !field.Secret || field.Default != "" {
				t.Fatalf("mysql-phpmyadmin 必填项 %s 必须是 secret 且无默认值（用户要能知道自己的口令）", field.Key)
			}
		}
	}
	if pmaRequired != "MYSQL_ROOT_PASSWORD" {
		t.Fatalf("mysql-phpmyadmin 必填项=%q，期望 MYSQL_ROOT_PASSWORD", pmaRequired)
	}
	for _, id := range []string{"wordpress"} {
		tpl := LookupTemplate(id)
		hasSecret := false
		for _, field := range tpl.Env {
			if field.Required && field.Secret {
				t.Fatalf("模板 %s 不应把 %s 设为必填：无 env 也必须能部署成功", id, field.Key)
			}
			if field.Secret {
				hasSecret = true
			}
		}
		if !hasSecret {
			t.Fatalf("模板 %s 缺少 secret 类型的口令项", id)
		}
	}
}

// TestTemplatesCopyIsolation 证明调用方改不到全局常量。
func TestTemplatesCopyIsolation(t *testing.T) {
	copied := Templates()
	copied[0].ID = "tampered"
	if LookupTemplate("tampered") != nil {
		t.Fatal("Templates() 返回的切片与全局目录共享底层数组")
	}
	if LookupTemplate("nginx-static") == nil {
		t.Fatal("Templates() 副本污染了全局目录")
	}
}

// TestMethodDomainAndUnknown 校验方法域与未知方法。
func TestMethodDomainAndUnknown(t *testing.T) {
	rt := testRuntime(t, newFakeExecutor())
	for _, method := range []string{
		"app-deploy.create", // 0.2.0 起由 deploy 取代，不做兼容层
		"app-deploy.exec",
		"deploy.templates",
		"templates",
		"",
	} {
		_, perr := rt.Call(method, json.RawMessage(`{}`))
		if perr == nil || perr.Code != pluginapi.E_NOTFOUND {
			t.Fatalf("方法 %q 错误=%v，期望 E_NOTFOUND", method, perr)
		}
	}
}

// TestNonLinuxUnsupported 非 Linux 显式拒绝，不做静默降级。
func TestNonLinuxUnsupported(t *testing.T) {
	rt := NewRuntime(Options{GOOS: "darwin", StateDir: t.TempDir()}, newFakeExecutor())
	_, perr := rt.Call("app-deploy.templates", json.RawMessage(`{}`))
	if perr == nil || perr.Code != pluginapi.E_UNSUPPORTED {
		t.Fatalf("darwin 错误=%v，期望 E_UNSUPPORTED", perr)
	}
}
