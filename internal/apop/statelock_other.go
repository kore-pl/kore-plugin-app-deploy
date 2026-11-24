//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package apop

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// stateLockFile 与 unix 版同名：锁固定在旁路文件上，不锁会被 rename 的状态文件。
const stateLockFile = ".state.lock"

// fallbackStateLocks 保存非 unix 平台的进程内锁：这些平台没有 flock 语义，
// 同一进程内的并发写仍然串行；跨进程并发写不在降级范围内
// （app-deploy 的真实部署能力本身只在 Linux 提供，见 Call 的 E_UNSUPPORTED）。
var fallbackStateLocks sync.Map // stateDir -> *sync.Mutex

// lockState 在非 unix 平台上退化为进程内排他锁，返回释放函数。
func lockState(stateDir string) (func(), *pluginapi.Error) {
	if err := ensureStateDir(stateDir); err != nil {
		return nil, internalErr("创建状态目录失败: " + err.Error())
	}
	// 仍创建旁路锁文件：保持状态目录布局一致，也让测试能观察到锁存在。
	file, err := os.OpenFile(filepath.Join(stateDir, stateLockFile), os.O_CREATE|os.O_RDWR, stateFileMode)
	if err != nil {
		return nil, internalErr("打开状态锁失败: " + err.Error())
	}
	_ = file.Close()
	value, _ := fallbackStateLocks.LoadOrStore(stateDir, &sync.Mutex{})
	mutex, ok := value.(*sync.Mutex)
	if !ok {
		return nil, internalErr("状态锁类型异常")
	}
	mutex.Lock()
	return mutex.Unlock, nil
}
