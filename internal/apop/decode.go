package apop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// noopExecutor 是骨架默认执行面：返回白名单模板元信息，但不拉取/部署任何应用。
// 四类故障注入测试提供自己的 exec，让 Call 走错误映射路径。
type noopExecutor struct{}

func (noopExecutor) Templates() ([]Template, error) {
	return []Template{
		{Name: "wordpress", Description: "WordPress 一键部署（骨架占位）", Available: true},
		{Name: "laravel", Description: "Laravel 一键部署（骨架占位）", Available: true},
		{Name: "nextjs", Description: "Next.js 一键部署（骨架占位）", Available: true},
	}, nil
}

func (noopExecutor) Create(template, target string) (bool, error) { return false, nil }

// decodeParams 校验 params 是 JSON 对象并反序列化到目标形状。
func decodeParams(raw json.RawMessage, target any) *pluginapi.Error {
	if len(raw) == 0 || string(raw) == "null" {
		return invalid("缺少必填参数对象")
	}
	if raw[0] != '{' {
		return invalid("params 必须是 JSON 对象")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return invalid("params 解析失败: " + err.Error())
	}
	return nil
}

// encode 把结果编码为 json.RawMessage；序列化失败归 E_INTERNAL。
func encode(v any) (json.RawMessage, *pluginapi.Error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, internalErr("序列化结果失败: " + err.Error())
	}
	return data, nil
}
