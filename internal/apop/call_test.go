package apop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestTemplatesWhitelist deploy.templates 返回且仅返回三个白名单模板。
func TestTemplatesWhitelist(t *testing.T) {
	result, perr := Call("deploy.templates", json.RawMessage(`{}`), "/var/lib/kore/app-deploy", nil)
	if perr != nil {
		t.Fatalf("deploy.templates 意外错误: %v", perr)
	}
	for _, name := range []string{"wordpress", "laravel", "nextjs"} {
		if !strings.Contains(string(result), `"`+name+`"`) {
			t.Fatalf("模板列表缺少 %s: %s", name, result)
		}
	}
}

// TestCreateAppliedFalse deploy.create 返回 applied=false 结构化占位。
func TestCreateAppliedFalse(t *testing.T) {
	for _, name := range []string{"wordpress", "laravel", "nextjs"} {
		params := `{"template":"` + name + `","target":"sites/demo"}`
		result, perr := Call("deploy.create", json.RawMessage(params), "/var/lib/kore/app-deploy", nil)
		if perr != nil {
			t.Fatalf("%s 意外错误: %v", name, perr)
		}
		if !strings.Contains(string(result), `"applied":false`) {
			t.Fatalf("%s 结果未标记 applied=false: %s", name, result)
		}
	}
}

// TestCreateRejects 非白名单模板与穿越路径返回 E_INVALID。
func TestCreateRejects(t *testing.T) {
	cases := []string{
		`{"template":"drupal","target":"sites/demo"}`,
		`{"template":"wordpress","target":"../escape"}`,
		`{"template":"wordpress","target":"/abs/path"}`,
	}
	for _, params := range cases {
		result, perr := Call("deploy.create", json.RawMessage(params), "/var/lib/kore/app-deploy", nil)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("params=%s 错误=%v，期望 E_INVALID", params, perr)
		}
		if result != nil {
			t.Fatalf("params=%s 出错时不应带结果", params)
		}
	}
}
