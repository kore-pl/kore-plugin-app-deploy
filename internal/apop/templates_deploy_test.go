package apop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestDeployNewTemplatesSeedEntry 覆盖新增模板的「一键部署后打开就有效果」：
// 空目录会让 node/python 容器直接退出、php/nginx 返回 404/403，所以必须落示例入口。
func TestDeployNewTemplatesSeedEntry(t *testing.T) {
	cases := []struct {
		id    string
		name  string
		vol   string
		file  string
		mark  string
		image string
	}{
		{"node-app", "kore-app-node", "app", "server.js", "node:http", "node:20-alpine"},
		{"python-app", "kore-app-py", "app", "app.py", "http.server", "python:3.12-alpine"},
		{"php-site", "kore-app-php", "html", "index.php", "<?php", "php:8.3-apache"},
	}
	for _, c := range cases {
		exec := newFakeExecutor()
		rt := testRuntime(t, exec)
		raw := callOK(t, rt, "app-deploy.deploy", `{"template":"`+c.id+`","name":"`+c.name+`"}`)
		var res DeployResult
		if err := json.Unmarshal([]byte(raw), &res); err != nil {
			t.Fatalf("%s 部署结果解析失败: %v", c.id, err)
		}
		if !res.Deployed || res.Template != c.id {
			t.Fatalf("%s 部署结果不符: %+v", c.id, res)
		}
		if len(exec.runSpecs) != 1 || exec.runSpecs[0].Image != c.image {
			t.Fatalf("%s 执行面镜像不符: %+v", c.id, exec.runSpecs)
		}
		body, err := os.ReadFile(filepath.Join(rt.opts.StateDir, VolumesDir, c.name, c.vol, c.file))
		if err != nil {
			t.Fatalf("%s 未落示例入口 %s: %v", c.id, c.file, err)
		}
		if !strings.Contains(string(body), c.mark) {
			t.Fatalf("%s 示例入口内容不含 %q: %s", c.id, c.mark, body)
		}
		if strings.Contains(string(body), c.name) == false {
			t.Fatalf("%s 示例入口应带上部署名（便于用户确认看到的是哪一个）: %s", c.id, body)
		}
	}
}

// TestSeedNeverOverwritesUserFile 用户已放了自己的文件时不得被示例覆盖。
func TestSeedNeverOverwritesUserFile(t *testing.T) {
	dir := t.TempDir()
	own := filepath.Join(dir, "server.js")
	if err := os.WriteFile(own, []byte("// 用户自己的入口\n"), 0o644); err != nil {
		t.Fatalf("预置文件失败: %v", err)
	}
	if err := seedBindVolume("node-app", "app", dir, "kore-app-node"); err != nil {
		t.Fatalf("seed 失败: %v", err)
	}
	body, err := os.ReadFile(own)
	if err != nil {
		t.Fatalf("读回失败: %v", err)
	}
	if strings.Contains(string(body), "node:http") {
		t.Fatalf("示例入口覆盖了用户文件: %s", body)
	}
	// 未登记的模板/卷不写任何东西。
	empty := t.TempDir()
	if err := seedBindVolume("redis", "data", empty, "kore-app-r"); err != nil {
		t.Fatalf("未登记组合不应报错: %v", err)
	}
	entries, err := os.ReadDir(empty)
	if err != nil {
		t.Fatalf("读目录失败: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("未登记组合不应写入文件: %v", entries)
	}
}

// TestDeployMySQLPhpMyAdmin 覆盖双容器 + 必填口令 + 内部连接变量（PMA_HOST 等）。
func TestDeployMySQLPhpMyAdmin(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)

	// 口令必填：缺了就拒绝，并点名缺哪个（不给"随便生成"的假成功）。
	perr := callErr(t, rt, "app-deploy.deploy",
		`{"template":"mysql-phpmyadmin","name":"kore-app-pma"}`, pluginapi.E_INVALID)
	if !strings.Contains(perr.Message, "MYSQL_ROOT_PASSWORD") {
		t.Fatalf("缺必填口令时应点名字段，实际 %q", perr.Message)
	}

	callOK(t, rt, "app-deploy.deploy", `{"template":"mysql-phpmyadmin","name":"kore-app-pma","env":{"MYSQL_ROOT_PASSWORD":"S3cret-pass","MYSQL_DATABASE":"shop"}}`)
	if len(exec.runSpecs) != 2 {
		t.Fatalf("双容器模板 docker run 次数=%d，期望 2", len(exec.runSpecs))
	}
	webEnv := ""
	for name, content := range exec.envContents {
		if name == "kore-app-pma" {
			webEnv = content
		}
	}
	for _, want := range []string{"PMA_HOST=kore-app-pma-db", "PMA_PORT=3306", "PMA_DB_NAME=shop", "PMA_BLOWFISH_SECRET="} {
		if !strings.Contains(webEnv, want) {
			t.Fatalf("phpMyAdmin 内部变量缺少 %q: %s", want, webEnv)
		}
	}
	// root 口令只进 env-file，绝不进 argv。
	if argv := exec.argvJoined(); strings.Contains(argv, "S3cret-pass") {
		t.Fatalf("口令出现在 argv 里: %s", argv)
	}
	// 两个端口都被解析出来，访问地址取主容器（web）。
	view := listOne(t, rt, "kore-app-pma")
	if len(view.Ports) != 2 || view.AccessURL != "http://127.0.0.1:18095/" {
		t.Fatalf("双端口视图不符: ports=%+v access=%q", view.Ports, view.AccessURL)
	}
}
