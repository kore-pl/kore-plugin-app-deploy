package apop

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// stateDirMode / stateFileMode 是状态目录与部署记录的权限：目录 0700、文件 0600，
// 记录里有容器 ID 与端口，属运行期内部数据，不对外共享。
const (
	stateDirMode  fs.FileMode = 0o700
	stateFileMode fs.FileMode = 0o600
	volumesSubDir             = "volumes"
)

// statePath 返回 deployments.json 的绝对路径。
func statePath(stateDir string) string {
	return filepath.Join(stateDir, DeploymentsFile)
}

// loadState 读取部署记录：文件不存在返回空状态（不是错误），内容损坏 / 版本过高
// 一律报错，不静默重置（静默重置会让已有容器失去归属记录，后续无法删除）。
func loadState(stateDir string) (stateFile, *pluginapi.Error) {
	empty := stateFile{Version: StateVersion, Deployments: []Deployment{}}
	data, err := os.ReadFile(statePath(stateDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return empty, nil
		}
		return empty, internalErr("读取部署记录失败: " + err.Error())
	}
	var st stateFile
	if err := json.Unmarshal(data, &st); err != nil {
		return empty, internalErr("部署记录 " + statePath(stateDir) + " 损坏（拒绝自动重置）: " + err.Error())
	}
	if st.Version > StateVersion {
		return empty, unsupported("部署记录版本 " + strconv.Itoa(st.Version) + " 高于插件支持版本 " + strconv.Itoa(StateVersion) + "，请升级插件")
	}
	if st.Version == 0 {
		st.Version = StateVersion
	}
	if st.Deployments == nil {
		st.Deployments = []Deployment{}
	}
	return st, nil
}

// saveState 原子写入部署记录：同目录临时文件（0600）→ fsync → rename，避免半截文件。
func saveState(stateDir string, st stateFile) *pluginapi.Error {
	if err := ensureStateDir(stateDir); err != nil {
		return internalErr("创建状态目录失败: " + err.Error())
	}
	st.Version = StateVersion
	if st.Deployments == nil {
		st.Deployments = []Deployment{}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return internalErr("序列化部署记录失败: " + err.Error())
	}
	data = append(data, '\n')
	target := statePath(stateDir)
	tmp, err := os.CreateTemp(stateDir, ".deployments-*.json")
	if err != nil {
		return internalErr("创建临时部署记录失败: " + err.Error())
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(stateFileMode); err != nil {
		_ = tmp.Close()
		return internalErr("设置部署记录权限失败: " + err.Error())
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return internalErr("写入部署记录失败: " + err.Error())
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return internalErr("刷新部署记录失败: " + err.Error())
	}
	if err := tmp.Close(); err != nil {
		return internalErr("关闭部署记录失败: " + err.Error())
	}
	if err := os.Rename(tmpName, target); err != nil {
		return internalErr("替换部署记录失败: " + err.Error())
	}
	return nil
}

// ensureStateDir 创建状态目录并把权限收敛到 0700（已存在也收敛，防止被 umask 放宽）。
func ensureStateDir(stateDir string) error {
	if stateDir == "" {
		return errors.New("状态目录为空")
	}
	if err := os.MkdirAll(stateDir, stateDirMode); err != nil {
		return err
	}
	return os.Chmod(stateDir, stateDirMode)
}

// findDeployment 按部署名查找记录下标；未找到返回 -1。
func findDeployment(st stateFile, name string) int {
	for i := range st.Deployments {
		if st.Deployments[i].Name == name {
			return i
		}
	}
	return -1
}

// upsertDeployment 插入或替换同名的记录。
func upsertDeployment(st *stateFile, rec Deployment) {
	if idx := findDeployment(*st, rec.Name); idx >= 0 {
		st.Deployments[idx] = rec
		return
	}
	st.Deployments = append(st.Deployments, rec)
}

// dropDeployment 删除同名记录，返回是否删除。
func dropDeployment(st *stateFile, name string) bool {
	idx := findDeployment(*st, name)
	if idx < 0 {
		return false
	}
	st.Deployments = append(st.Deployments[:idx], st.Deployments[idx+1:]...)
	return true
}
