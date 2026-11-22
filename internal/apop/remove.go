package apop

import (
	"encoding/json"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callRemove 处理 app-deploy.remove：必须 confirm=true，且只删部署记录里的容器。
//
// 归属证据：记录里的容器完整 ID 是 deploy 时 docker run 的返回值，remove 只按它删；
// 名字前缀 kore-app- 再兜一道。记录里没有的容器一律拒绝（E_UNAUTHORIZED），
// 防止插件成为「删任意容器」的后门。
func (r *Runtime) callRemove(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p removeParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if perr := ValidateName(p.Name); perr != nil {
		return nil, perr
	}
	if !p.Confirm {
		return nil, invalid("remove 需要二次确认：请传 confirm:true 后重试")
	}
	st, perr := loadState(r.opts.StateDir)
	if perr != nil {
		return nil, perr
	}
	states, perr := r.snapshot()
	if perr != nil {
		return nil, perr
	}
	idx := findDeployment(st, p.Name)
	if idx < 0 {
		if foreign := foreignContainerNames(p.Name, states); len(foreign) > 0 {
			return nil, unauthorized("拒绝删除容器 " + strings.Join(foreign, ", ") +
				"：不在本插件部署记录中（非本插件创建，请用 docker 自行管理）")
		}
		return nil, notFound("部署不存在: " + p.Name)
	}
	record := st.Deployments[idx]
	if !strings.HasPrefix(record.Name, ContainerPrefix) {
		return nil, unauthorized("拒绝删除 " + record.Name + "：部署记录名缺少 " + ContainerPrefix + " 前缀")
	}
	refs := record.Containers
	if len(refs) == 0 && record.ContainerID != "" {
		refs = []ContainerRef{{Name: record.Name, ID: record.ContainerID}}
	}
	names := make([]string, 0, len(refs))
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		if !strings.HasPrefix(ref.Name, ContainerPrefix) {
			return nil, unauthorized("拒绝删除 " + ref.Name + "：容器名缺少 " + ContainerPrefix + " 前缀")
		}
		names = append(names, ref.Name)
		if ref.ID == "" {
			continue
		}
		if _, ok := findContainer(ref, states); ok {
			ids = append(ids, ref.ID)
		}
	}
	if len(ids) > 0 {
		if err := r.exec.RemoveContainers(ids); err != nil {
			return nil, classify(err)
		}
	}
	warnings := make([]string, 0, 4)
	volumeNames := make([]string, 0, len(record.Volumes))
	for _, volume := range record.Volumes {
		// 只删带前缀的命名卷；bind 目录保留（可能含用户文件，删了不可恢复）。
		if volume.Kind != VolumeNamed {
			continue
		}
		if !strings.HasPrefix(volume.Name, ContainerPrefix) {
			warnings = append(warnings, "跳过非本插件卷 "+volume.Name)
			continue
		}
		volumeNames = append(volumeNames, volume.Name)
	}
	if len(volumeNames) > 0 {
		if err := r.exec.RemoveVolumes(volumeNames); err != nil {
			warnings = append(warnings, "删除卷失败: "+err.Error())
		}
	}
	if record.Network != "" {
		if !strings.HasPrefix(record.Network, ContainerPrefix) {
			warnings = append(warnings, "跳过非本插件网络 "+record.Network)
		} else if err := r.exec.RemoveNetwork(record.Network); err != nil {
			warnings = append(warnings, "删除网络失败: "+err.Error())
		}
	}
	dropDeployment(&st, p.Name)
	if perr := saveState(r.opts.StateDir, st); perr != nil {
		return nil, perr
	}
	return encode(RemoveResult{
		Removed:    true,
		Name:       record.Name,
		Containers: names,
		Volumes:    volumeNames,
		Network:    record.Network,
		Warnings:   warnings,
	})
}

// foreignContainerNames 返回 docker 里存在、但名字属于该部署命名空间的容器。
// 用于「记录里没有，却要删同名容器」的拒绝分支。
func foreignContainerNames(deployment string, states []ContainerState) []string {
	names := make([]string, 0, 2)
	for _, state := range states {
		if state.Name == deployment || strings.HasPrefix(state.Name, deployment+"-") {
			names = append(names, state.Name)
		}
	}
	return names
}
