package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/pengtang1024/kore-plugin-app-deploy/internal/apop"
	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// stubExecutor 是内存执行面：只验证 dispatch → runtime 的接线，不触达真实 docker。
type stubExecutor struct{ seq int }

func (s *stubExecutor) RunContainer(spec apop.ContainerSpec) (string, error) {
	s.seq++
	return fmt.Sprintf("%012x", s.seq) + strings.Repeat("b", 52), nil
}

func (s *stubExecutor) ListContainers() ([]apop.ContainerState, error) { return nil, nil }

func (s *stubExecutor) RemoveContainers([]string) error { return nil }

func (s *stubExecutor) RemoveVolumes([]string) error { return nil }

func (s *stubExecutor) CreateNetwork(string) error { return nil }

func (s *stubExecutor) RemoveNetwork(string) error { return nil }

// testRuntime 构造指向临时目录、端口全可用的运行时。
func testRuntime(t *testing.T) *apop.Runtime {
	t.Helper()
	dir := t.TempDir()
	return apop.NewRuntime(apop.Options{
		Root:      dir,
		StateDir:  dir + "/state",
		GOOS:      "linux",
		PortProbe: func(int) bool { return true },
	}, &stubExecutor{})
}

// TestDispatchMethods 四个 app-deploy 方法经 dispatch 返回结构化结果。
func TestDispatchMethods(t *testing.T) {
	rt := testRuntime(t)
	cases := []struct {
		method string
		params string
		want   string
	}{
		{"app-deploy.templates", `{}`, `"count":4`},
		{"app-deploy.deploy", `{"template":"nginx-static","name":"kore-app-demo"}`, `"deployed":true`},
		{"app-deploy.list", `{}`, `"count":1`},
		{"app-deploy.remove", `{"name":"kore-app-demo","confirm":true}`, `"removed":true`},
	}
	for _, c := range cases {
		req := &pluginapi.Request{
			Type: pluginapi.TypeRequest, ID: "id", ReqID: "r",
			Method: c.method, Params: json.RawMessage(c.params),
		}
		resp, shutdown := dispatch(req, testConfig(), rt, testLogger())
		if shutdown {
			t.Fatalf("%s 不应触发 shutdown", c.method)
		}
		if resp.Error != nil {
			t.Fatalf("%s 意外错误: %+v", c.method, resp.Error)
		}
		if !strings.Contains(string(resp.Result), c.want) {
			t.Fatalf("%s 结果=%s，未含 %q", c.method, resp.Result, c.want)
		}
	}
}

// TestDispatchReject 非法参数与未知方法经 dispatch 返回协议错误。
func TestDispatchReject(t *testing.T) {
	rt := testRuntime(t)
	cases := []struct {
		method string
		params string
		code   string
	}{
		{"app-deploy.deploy", `{"template":"evil/image","name":"kore-app-x"}`, pluginapi.E_INVALID},
		{"app-deploy.deploy", `{"template":"nginx-static","name":"../../etc/passwd"}`, pluginapi.E_INVALID},
		{"app-deploy.deploy", `{"template":"nginx-static","name":"kore-app-x","ports":[{"container_port":80,"host_port":22}]}`, pluginapi.E_INVALID},
		{"app-deploy.remove", `{"name":"kore-app-x"}`, pluginapi.E_INVALID},
		{"app-deploy.remove", `{"name":"kore-app-x","confirm":false}`, pluginapi.E_INVALID},
		{"app-deploy.exec", `{}`, pluginapi.E_NOTFOUND},
		{"app-deploy.create", `{}`, pluginapi.E_NOTFOUND}, // 0.2.0 起由 deploy 取代
		{"deploy.templates", `{}`, pluginapi.E_NOTFOUND},  // 旧域
		{"templates", `{}`, pluginapi.E_NOTFOUND},         // 裸动词
	}
	for _, c := range cases {
		req := &pluginapi.Request{
			Type: pluginapi.TypeRequest, ID: "id", ReqID: "r",
			Method: c.method, Params: json.RawMessage(c.params),
		}
		resp, _ := dispatch(req, testConfig(), rt, testLogger())
		if resp.Error == nil || resp.Error.Code != c.code {
			t.Fatalf("%s %s 错误=%+v，期望 %q", c.method, c.params, resp.Error, c.code)
		}
		if resp.Result != nil {
			t.Fatalf("%s 出错时不应带结果", c.method)
		}
	}
}

// TestPingResult kore.ping 返回 ready 状态。
func TestPingResult(t *testing.T) {
	req := &pluginapi.Request{Type: pluginapi.TypeRequest, ID: "id", ReqID: "r", Method: "kore.ping"}
	resp, _ := dispatch(req, testConfig(), testRuntime(t), testLogger())
	if !strings.Contains(string(resp.Result), `"status":"ready"`) {
		t.Fatalf("ping 结果=%s", resp.Result)
	}
}
