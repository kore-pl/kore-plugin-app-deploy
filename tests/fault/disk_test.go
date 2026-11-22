package fault

import (
	"testing"

	"github.com/pengtang1024/kore-plugin-app-deploy/internal/apop"
)

// TestDiskFullFault 注入磁盘满（部署记录/卷写入失败），期望三个方法返回 E_INTERNAL。
func TestDiskFullFault(t *testing.T) {
	assertFault(t, apop.FaultDisk, "E_INTERNAL", "磁盘满")
}
