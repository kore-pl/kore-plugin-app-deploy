//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package apop

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// stateLockFile 是状态目录的**旁路**锁文件：锁加在它身上，而不是加在
// deployments.json / artifact-jobs.json 上。
//
// 为什么必须旁路：两个状态文件都是「临时文件 + rename 替换」落盘，rename 之后
// 路径指向的是新 inode；锁若加在旧 inode 上，后来者拿到的是新 inode 的锁，
// 互斥会**静默失效**（site 清单锁的教训）。旁路锁文件只会被创建 / 打开，
// 永不 rename，inode 恒定，flock 互斥才真正成立。
//
// 为什么一把锁管两个文件：deployments 与 jobs 同处一个状态目录、会被同一次
// 方法调用先后写入（如 deployFromArtifact）；按文件分锁会引入加锁顺序问题，
// 单锁从构造上消除死锁。临界区只含「读 JSON → 改内存 → 原子写回」，毫秒级。
const stateLockFile = ".state.lock"

// stateLockWaitTimeout 是获取状态锁的最长等待：临界区毫秒级，超时说明有异常
// 持有者，宁可返回结构化错误也不无限阻塞 RPC。
const stateLockWaitTimeout = 5 * time.Second

// lockState 在状态目录的旁路文件上取排他 flock（跨进程 + 跨 goroutine），
// 返回释放函数。flock 随 fd / 进程消亡由内核释放，SIGKILL 不会留下悬挂锁。
func lockState(stateDir string) (func(), *pluginapi.Error) {
	if err := ensureStateDir(stateDir); err != nil {
		return nil, internalErr("创建状态目录失败: " + err.Error())
	}
	file, err := os.OpenFile(filepath.Join(stateDir, stateLockFile), os.O_CREATE|os.O_RDWR, stateFileMode)
	if err != nil {
		return nil, internalErr("打开状态锁失败: " + err.Error())
	}
	deadline := time.Now().Add(stateLockWaitTimeout)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		// EINTR：信号打断等待，重试而不是把可恢复的中断当失败。
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			_ = file.Close()
			return nil, internalErr("获取状态锁失败: " + err.Error())
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, internalErr("等待状态锁超时（5s），可能有异常持有者")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}
