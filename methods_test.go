package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestDispatchMethods 两个 app-deploy 方法经 dispatch 返回结构化结果。
func TestDispatchMethods(t *testing.T) {
	cases := []struct {
		method string
		params string
		want   string
	}{
		{"deploy.templates", `{}`, `"count":3`},
		{"deploy.create", `{"template":"wordpress","target":"sites/blog"}`, `"applied":false`},
	}
	for _, c := range cases {
		req := &pluginapi.Request{
			Type: pluginapi.TypeRequest, ID: "id", ReqID: "r",
			Method: c.method, Params: json.RawMessage(c.params),
		}
		resp, shutdown := dispatch(req, testConfig(), testLogger())
		if shutdown {
			t.Fatalf("%s 不应触发 shutdown", c.method)
		}
		if resp.Error != nil {
			t.Fatalf("%s 意外错误: %v", c.method, resp.Error)
		}
		if !strings.Contains(string(resp.Result), c.want) {
			t.Fatalf("%s 结果=%s，未含 %q", c.method, resp.Result, c.want)
		}
	}
}

// TestDispatchReject 白名单外模板、路径穿越与未知方法经 dispatch 返回协议错误。
func TestDispatchReject(t *testing.T) {
	cases := []struct {
		method string
		params string
		code   string
	}{
		{"deploy.create", `{"template":"drupal","target":"sites/blog"}`, pluginapi.E_INVALID},
		{"deploy.create", `{"template":"wordpress","target":"../../etc/passwd"}`, pluginapi.E_INVALID},
		{"deploy.exec", `{}`, pluginapi.E_NOTFOUND},
	}
	for _, c := range cases {
		req := &pluginapi.Request{
			Type: pluginapi.TypeRequest, ID: "id", ReqID: "r",
			Method: c.method, Params: json.RawMessage(c.params),
		}
		resp, _ := dispatch(req, testConfig(), testLogger())
		if resp.Error == nil || resp.Error.Code != c.code {
			t.Fatalf("%s 错误=%v，期望 %q", c.method, resp.Error, c.code)
		}
		if resp.Result != nil {
			t.Fatalf("%s 出错时不应带结果", c.method)
		}
	}
}

// TestPingResult kore.ping 返回 ready 状态。
func TestPingResult(t *testing.T) {
	req := &pluginapi.Request{Type: pluginapi.TypeRequest, ID: "id", ReqID: "r", Method: "kore.ping"}
	resp, _ := dispatch(req, testConfig(), testLogger())
	if !strings.Contains(string(resp.Result), `"status":"ready"`) {
		t.Fatalf("ping 结果=%s", resp.Result)
	}
}
