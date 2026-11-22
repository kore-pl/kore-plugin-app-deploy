package fault

import (
	"testing"

	"github.com/pengtang1024/kore-plugin-app-deploy/internal/apop"
)

// TestKillFault 注入 kill -9（docker 进程被信号终止），期望三个方法返回 E_UPSTREAM。
func TestKillFault(t *testing.T) {
	assertFault(t, apop.FaultKill, "E_UPSTREAM", "信号 9")
}
