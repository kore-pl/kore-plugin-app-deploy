package apop

import (
	"os"
	"path/filepath"
	"strings"
)

// ADR-0023 阶段 C：插件数据目录必须落在安装目录内（内核注入 KORE_PLUGIN_DATA，
// 0700 且属主 kore-<id>）。本文件是该口径的唯一解析入口。
const (
	// EnvPluginData 是内核注入的插件数据目录（0700）；阶段 C 的默认落点。
	EnvPluginData = "KORE_PLUGIN_DATA"
	// EnvInstallDir 是内核注入的安装目录；list 用它判定「是否在安装目录外」。
	EnvInstallDir = "KORE_INSTALL_DIR"
	// AppsSubdir 是部署产物（bind 挂载）默认子目录。
	AppsSubdir = "apps"
	// SnapshotsSubdir 是部署记录/元数据默认子目录。
	SnapshotsSubdir = "snapshots"
)

// ResolveDataDirs 按优先级解析部署根与状态目录：
//
//  1. 显式 KORE_APP_DEPLOY_ROOT（可选 KORE_APP_DEPLOY_STATE 再覆盖状态目录）；
//  2. 未显式配置且内核注入 KORE_PLUGIN_DATA → <plugin-data>/apps + <plugin-data>/snapshots；
//  3. 两者都没有 → 回退旧默认 /var/lib/kore/app-deploy + <root>/state，并返回非空 warn
//     （调用方必须 WARN，不允许静默）。
//
// 已部署应用不迁移：解析只影响新建产物的落点，历史记录原样保留。
func ResolveDataDirs(getenv func(string) string) (root, stateDir, warn string) {
	root = strings.TrimSpace(getenv(EnvRoot))
	state := strings.TrimSpace(getenv(EnvStateDir))
	if root != "" {
		if state == "" {
			state = filepath.Join(root, "state")
		}
		return root, state, ""
	}
	if data := strings.TrimSpace(getenv(EnvPluginData)); data != "" {
		root = filepath.Join(data, AppsSubdir)
		if state == "" {
			state = filepath.Join(data, SnapshotsSubdir)
		}
		return root, state, ""
	}
	root = DefaultRoot
	if state == "" {
		state = filepath.Join(root, "state")
	}
	warn = "未配置 " + EnvRoot + " 且内核未注入 " + EnvPluginData + "：回退旧默认 " + DefaultRoot +
		"（安装目录外写入，不符合 ADR-0023 零侵入；请升级内核或显式配置）"
	return root, state, warn
}

// DataRoots 返回「安装目录内」的可写根集合，供 list 判定 outside_install。
// 顺序固定：部署根、状态目录、内核安装目录、内核插件数据目录（去空、去重）。
func DataRoots(root, stateDir string) []string {
	roots := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		value = filepath.Clean(value)
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		roots = append(roots, value)
	}
	add(root)
	add(stateDir)
	add(os.Getenv(EnvInstallDir))
	add(os.Getenv(EnvPluginData))
	return roots
}

// dataRoots 返回本运行期认为「安装目录内」的可写根集合（供 list 判定 outside_install）。
func (r *Runtime) dataRoots() []string {
	return DataRoots(r.opts.Root, r.opts.StateDir)
}

// IsUnder 报告 path 是否位于 root 之内（含相等）；任一为空返回 false。
func IsUnder(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if path == root {
		return true
	}
	return strings.HasPrefix(path, root+string(filepath.Separator))
}

// OutsideAllRoots 报告 path 是否在所有可写根之外；roots 为空时视为未知（false）。
func OutsideAllRoots(path string, roots []string) bool {
	if strings.TrimSpace(path) == "" || len(roots) == 0 {
		return false
	}
	for _, root := range roots {
		if IsUnder(root, path) {
			return false
		}
	}
	return true
}
