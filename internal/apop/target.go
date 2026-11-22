package apop

import (
	"path"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// ValidateTarget 把目标路径限制在配置根目录内，返回归一化后的绝对路径。
// 拒绝：空路径、绝对路径、反斜杠、NUL、".." 段、以及 Clean 后越出根目录。
// 只做字符串校验，不访问文件系统。
func ValidateTarget(root, raw string) (string, *pluginapi.Error) {
	if raw == "" {
		return "", invalid("目标路径不能为空")
	}
	if strings.ContainsRune(raw, 0) {
		return "", invalid("目标路径含 NUL 字节")
	}
	if strings.Contains(raw, "\\") {
		return "", invalid("目标路径含反斜杠")
	}
	if path.IsAbs(raw) {
		return "", invalid("目标路径必须是根目录内的相对路径")
	}
	for _, seg := range strings.Split(raw, "/") {
		if seg == ".." {
			return "", invalid("目标路径含 .. 段，禁止穿越")
		}
	}
	rootClean := path.Clean(root)
	joined := path.Clean(path.Join(rootClean, raw))
	if joined != rootClean && !strings.HasPrefix(joined, rootClean+"/") {
		return "", invalid("目标路径越出配置根目录")
	}
	return joined, nil
}
