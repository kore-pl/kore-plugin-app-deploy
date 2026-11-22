package apop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callCreate 处理 deploy.create：校验模板白名单与目标路径后返回结构化占位结果。
// 骨架口径不真拉取/部署，applied 恒为 false。
func callCreate(raw json.RawMessage, root string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	var p createParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if perr := validateTemplate(p.Template); perr != nil {
		return nil, perr
	}
	abs, perr := ValidateTarget(root, p.Target)
	if perr != nil {
		return nil, perr
	}
	if _, err := exec.Create(p.Template, abs); err != nil {
		return nil, classify(err)
	}
	// 骨架口径：不真部署，applied=false 是结构化占位。
	return encode(CreateResult{Template: p.Template, Target: p.Target, Applied: false})
}

// validateTemplate 拒绝白名单外的模板名。
func validateTemplate(name string) *pluginapi.Error {
	if name == "" {
		return invalid("template 不能为空")
	}
	if !allowedTemplates[name] {
		return invalid("模板不在白名单内: " + name + "（允许 wordpress/laravel/nextjs）")
	}
	return nil
}
