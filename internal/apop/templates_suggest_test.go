package apop

import (
	"encoding/json"
	"testing"
)

// findTemplate 在结果里按 id 找模板。
func findTemplate(t *testing.T, list []Template, id string) Template {
	t.Helper()
	for _, tpl := range list {
		if tpl.ID == id {
			return tpl
		}
	}
	t.Fatalf("结果里缺少模板 %s", id)
	return Template{}
}

// TestTemplatesPortSuggestion 校验「端口自动探测」的两个来源：
// ① 运行中容器已发布的端口（docker ps 快照）② 本机绑定失败的端口（真实探测）。
// 建议值必须是「从推荐端口向上第一个真正可用」的端口，且不污染全局模板常量。
func TestTemplatesPortSuggestion(t *testing.T) {
	exec := newFakeExecutor()
	// 已有容器占了 nginx-static 的推荐端口 18090。
	exec.addState(ContainerState{Name: "kore-app-other", State: StatusRunning,
		Ports: "127.0.0.1:18090->80/tcp"})
	rt := NewRuntime(Options{
		Root: t.TempDir(), StateDir: t.TempDir() + "/state", GOOS: "linux",
		PortProbe: func(port int) bool { return port != 18091 }, // 18091 绑不上
	}, exec)

	var got TemplatesResult
	if err := json.Unmarshal([]byte(callOK(t, rt, "app-deploy.templates", "{}")), &got); err != nil {
		t.Fatalf("解析模板目录失败: %v", err)
	}
	nginx := findTemplate(t, got.Templates, "nginx-static")
	if nginx.Ports[0].DefaultHost != 18090 {
		t.Fatalf("模板常量的推荐端口被改写了: %d", nginx.Ports[0].DefaultHost)
	}
	if nginx.Ports[0].SuggestedHostPort != 18092 {
		t.Fatalf("建议端口=%d，期望 18092（18090 被容器占用、18091 绑不上）", nginx.Ports[0].SuggestedHostPort)
	}
	// 未被占用的模板建议值就是推荐值。
	if php := findTemplate(t, got.Templates, "php-site"); php.Ports[0].SuggestedHostPort != 18094 {
		t.Fatalf("php-site 建议端口=%d，期望 18094", php.Ports[0].SuggestedHostPort)
	}
	// 双端口模板两条端口各自给建议，且互不重复。
	mysql := findTemplate(t, got.Templates, "mysql-phpmyadmin")
	if len(mysql.Ports) != 2 || mysql.Ports[0].SuggestedHostPort != 18095 || mysql.Ports[1].SuggestedHostPort != 13306 {
		t.Fatalf("mysql-phpmyadmin 建议端口不符: %+v", mysql.Ports)
	}

	// 副本隔离：建议值是写在副本上的，全局常量必须保持 0。
	if global := LookupTemplate("nginx-static"); global.Ports[0].SuggestedHostPort != 0 {
		t.Fatalf("全局模板常量被建议值污染: %+v", global.Ports[0])
	}
}

// TestTemplatesSuggestionSurvivesDockerDown docker 不可用时模板目录照常返回，
// 建议端口退化为「只按本机探测」的结果——不能因为 docker 没装就打不开页面。
func TestTemplatesSuggestionSurvivesDockerDown(t *testing.T) {
	exec := newFakeExecutor()
	exec.listErr = NewFaultError(FaultKill, "docker ps 不可用")
	rt := testRuntime(t, exec)
	var got TemplatesResult
	if err := json.Unmarshal([]byte(callOK(t, rt, "app-deploy.templates", "{}")), &got); err != nil {
		t.Fatalf("docker 不可用时模板目录不应失败: %v", err)
	}
	if got.Count != len(builtinTemplates) {
		t.Fatalf("模板数量=%d，期望 %d", got.Count, len(builtinTemplates))
	}
	if nginx := findTemplate(t, got.Templates, "nginx-static"); nginx.Ports[0].SuggestedHostPort != 18090 {
		t.Fatalf("docker 不可用时应退回推荐端口，实际 %d", nginx.Ports[0].SuggestedHostPort)
	}
}
