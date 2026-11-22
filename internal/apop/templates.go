package apop

import (
	"encoding/json"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callTemplates 处理 deploy.templates：返回白名单模板元信息（骨架口径不真下载模板）。
// params 允许为空对象或空载荷；root 仅用于与 create 一致的路径口径，本方法不访问文件系统。
func callTemplates(raw json.RawMessage, root string, exec Executor) (json.RawMessage, *pluginapi.Error) {
	if len(raw) > 0 && string(raw) != "null" {
		var p templatesParams
		if perr := decodeParams(raw, &p); perr != nil {
			return nil, perr
		}
	}
	templates, err := exec.Templates()
	if err != nil {
		return nil, classify(err)
	}
	if templates == nil {
		templates = []Template{}
	}
	return encode(TemplatesResult{Templates: templates, Count: len(templates)})
}
