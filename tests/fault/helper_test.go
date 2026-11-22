// Package fault 对 app-deploy 插件做四类故障注入，验证方法级错误返回。
// 每个用例向运行时注入一种故障执行面，断言 deploy / list / remove 返回结构化错误码。
package fault

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-app-deploy/internal/apop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// faultExecutor 按注入类别让每个 docker 操作都失败，模拟四类运行时故障。
type faultExecutor struct {
	kind apop.FaultKind
}

func (f faultExecutor) RunContainer(apop.ContainerSpec) (string, error) {
	return "", apop.NewFaultError(f.kind, "docker run 执行面故障")
}

func (f faultExecutor) ListContainers() ([]apop.ContainerState, error) {
	return nil, apop.NewFaultError(f.kind, "docker ps 执行面故障")
}

func (f faultExecutor) RemoveContainers([]string) error {
	return apop.NewFaultError(f.kind, "docker rm 执行面故障")
}

func (f faultExecutor) RemoveVolumes([]string) error {
	return apop.NewFaultError(f.kind, "docker volume rm 执行面故障")
}

func (f faultExecutor) CreateNetwork(string) error {
	return apop.NewFaultError(f.kind, "docker network create 执行面故障")
}

func (f faultExecutor) RemoveNetwork(string) error {
	return apop.NewFaultError(f.kind, "docker network rm 执行面故障")
}

// okExecutor 是无故障执行面，用于证明故障断言不是「永远失败」的假通过。
type okExecutor struct{ seq int }

func (o *okExecutor) RunContainer(spec apop.ContainerSpec) (string, error) {
	o.seq++
	return "abcdefabcdef" + strings.Repeat("0", 52), nil
}

func (o *okExecutor) ListContainers() ([]apop.ContainerState, error) { return nil, nil }

func (o *okExecutor) RemoveContainers([]string) error { return nil }

func (o *okExecutor) RemoveVolumes([]string) error { return nil }

func (o *okExecutor) CreateNetwork(string) error { return nil }

func (o *okExecutor) RemoveNetwork(string) error { return nil }

// methods 是故障注入覆盖的三个方法及其合法参数（templates 不触达执行面，单独做健全性验证）。
var methods = []struct {
	name   string
	method string
	params string
}{
	{"deploy", "app-deploy.deploy", `{"template":"nginx-static","name":"kore-app-fault"}`},
	{"list", "app-deploy.list", `{}`},
	{"remove", "app-deploy.remove", `{"name":"kore-app-fault","confirm":true}`},
}

// newRuntime 构造指向临时目录的运行时。
func newRuntime(t *testing.T, exec apop.Executor) *apop.Runtime {
	t.Helper()
	dir := t.TempDir()
	return apop.NewRuntime(apop.Options{
		Root:      dir,
		StateDir:  filepath.Join(dir, "state"),
		GOOS:      "linux",
		PortProbe: func(int) bool { return true },
	}, exec)
}

// assertFault 对每个方法注入故障，断言返回 nil 结果 + 预期的协议错误码与类别关键词。
func assertFault(t *testing.T, kind apop.FaultKind, wantCode, wantHint string) {
	t.Helper()
	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			rt := newRuntime(t, faultExecutor{kind: kind})
			result, perr := rt.Call(m.method, json.RawMessage(m.params))
			if perr == nil {
				t.Fatalf("%s 注入 %s 后未返回错误（结果=%s）", m.method, kind, result)
			}
			if result != nil {
				t.Fatalf("%s 出错时不应带结果: %s", m.method, result)
			}
			if perr.Code != wantCode {
				t.Fatalf("%s 错误码=%q，期望 %q（message=%q）", m.method, perr.Code, wantCode, perr.Message)
			}
			if !strings.Contains(perr.Message, wantHint) {
				t.Fatalf("%s 错误信息 %q 未含类别关键词 %q", m.method, perr.Message, wantHint)
			}
			verifyErrorContract(t, perr)
		})
	}
}

// sanityNoFault 验证无故障时各方法正常工作（templates 也一并覆盖）。
func sanityNoFault(t *testing.T) {
	t.Helper()
	rt := newRuntime(t, &okExecutor{})
	templates, perr := rt.Call("app-deploy.templates", json.RawMessage(`{}`))
	if perr != nil || len(templates) == 0 {
		t.Fatalf("无故障时 templates 失败: %v", perr)
	}
	deployed, perr := rt.Call("app-deploy.deploy", json.RawMessage(`{"template":"nginx-static","name":"kore-app-ok"}`))
	if perr != nil {
		t.Fatalf("无故障时 deploy 失败: %+v", perr)
	}
	if !strings.Contains(string(deployed), `"deployed":true`) {
		t.Fatalf("deploy 结果未标记 deployed=true: %s", deployed)
	}
	listed, perr := rt.Call("app-deploy.list", json.RawMessage(`{}`))
	if perr != nil || !strings.Contains(string(listed), `"count":1`) {
		t.Fatalf("无故障时 list 失败: %v / %s", perr, listed)
	}
	removed, perr := rt.Call("app-deploy.remove", json.RawMessage(`{"name":"kore-app-ok","confirm":true}`))
	if perr != nil || !strings.Contains(string(removed), `"removed":true`) {
		t.Fatalf("无故障时 remove 失败: %v / %s", perr, removed)
	}
}

// verifyErrorContract 断言错误体是标准协议结构（含 code 与 message）。
func verifyErrorContract(t *testing.T, perr *pluginapi.Error) {
	t.Helper()
	if perr == nil {
		t.Fatal("错误体为 nil")
	}
	if perr.Code == "" || perr.Message == "" {
		t.Fatalf("错误体字段不完整: %+v", perr)
	}
}
