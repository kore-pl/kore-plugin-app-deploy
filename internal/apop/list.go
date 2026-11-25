package apop

import (
	"encoding/json"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// callList 处理 app-deploy.list：读部署记录 + 关联 docker ps 计算真实状态。
// 本方法是只读的：不改记录、不动容器。
func (r *Runtime) callList(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p listParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	st, perr := loadState(r.opts.StateDir)
	if perr != nil {
		return nil, perr
	}
	states, perr := r.snapshot()
	if perr != nil {
		return nil, perr
	}
	roots := r.dataRoots()
	views := make([]DeploymentView, 0, len(st.Deployments))
	for _, rec := range st.Deployments {
		views = append(views, buildDeploymentView(rec, states, roots))
	}
	usageErr := r.attachUsage(views)
	return encode(ListResult{Deployments: views, Count: len(views), UsageError: usageErr})
}

// attachUsage 给运行中的部署补资源占用概览（一次 docker stats 覆盖所有容器），
// 返回采样失败原因（有失败也要让调用方看见，不能假装"不占资源"）。
//
// 采样是**展示性附加数据**：执行面没有控制能力、或 docker stats 失败时，列表照常返回，
// 只是 usage 为空并把原因放进 usage_error。停止/启动/日志不受影响。
func (r *Runtime) attachUsage(views []DeploymentView) string {
	ctrl, ok := r.exec.(Controller)
	if !ok {
		return ""
	}
	names := make([]string, 0, len(views))
	for _, view := range views {
		if view.Status != StatusRunning {
			continue
		}
		for _, container := range view.Containers {
			if container.Status == StatusRunning && container.Name != "" {
				names = append(names, container.Name)
			}
		}
	}
	if len(names) == 0 {
		return ""
	}
	usages, err := ctrl.SampleUsage(names)
	if err != nil {
		return "资源占用暂不可用：" + err.Error()
	}
	byName := make(map[string]ContainerUsage, len(usages))
	for _, usage := range usages {
		byName[usage.Name] = usage
	}
	for i := range views {
		if views[i].Status != StatusRunning {
			continue
		}
		summary := UsageSummary{}
		seen := false
		for _, container := range views[i].Containers {
			usage, ok := byName[container.Name]
			if !ok {
				continue
			}
			seen = true
			summary.CPUPercent += usage.CPUPercent
			summary.MemUsedBytes += usage.MemUsedBytes
			summary.MemLimitBytes += usage.MemLimitBytes
		}
		if seen {
			views[i].Usage = &summary
		}
	}
	return ""
}

// buildDeploymentView 把一条记录与 docker 快照合成对外视图。
func buildDeploymentView(rec Deployment, states []ContainerState, roots []string) DeploymentView {
	refs := rec.Containers
	if len(refs) == 0 {
		// 兼容只有 container_id 的早期记录形状。
		refs = []ContainerRef{{Name: rec.Name, ID: rec.ContainerID}}
	}
	containers := make([]ContainerView, 0, len(refs))
	allRunning := true
	anyMissing := false
	for _, ref := range refs {
		status := containerStatus(ref, states)
		if status != StatusRunning {
			allRunning = false
		}
		if status == StatusMissing {
			anyMissing = true
		}
		containers = append(containers, ContainerView{Service: ref.Service, Name: ref.Name, ID: ref.ID, Status: status})
	}
	status := StatusRunning
	switch {
	case len(containers) == 0 || anyMissing:
		status = StatusMissing
	case !allRunning:
		status = StatusExited
	}
	outside, hint := outsideInstall(rec, roots)
	return DeploymentView{
		ID:             rec.ID,
		Template:       rec.Template,
		Name:           rec.Name,
		ContainerID:    rec.ContainerID,
		Status:         status,
		Ports:          rec.Ports,
		AccessURL:      firstAccessURL(rec.Ports),
		CreatedAt:      rec.CreatedAt,
		Containers:     containers,
		OutsideInstall: outside,
		Hint:           hint,
	}
}

// outsideInstall 判定一条部署记录是否落在安装目录之外，并给出不自动搬迁的提示。
// 只依据 bind 挂载的真实宿主机路径；named 卷由 docker 管理、无路径可判，记 false。
func outsideInstall(rec Deployment, roots []string) (bool, string) {
	for _, volume := range rec.Volumes {
		if volume.Kind != VolumeBind || volume.Name == "" {
			continue
		}
		if OutsideAllRoots(volume.Name, roots) {
			return true, "该应用部署在安装目录外（" + volume.Name + "）：插件不会自动搬迁，如需归位请先 remove 再重新 deploy"
		}
	}
	return false, ""
}

// containerStatus 在 docker 快照里定位一个记录中的容器：
// running / exited（存在但未运行）/ missing（容器已不在）。
func containerStatus(ref ContainerRef, states []ContainerState) string {
	state, ok := findContainer(ref, states)
	if !ok {
		return StatusMissing
	}
	if state.State == StatusRunning {
		return StatusRunning
	}
	return StatusExited
}

// findContainer 优先按完整 ID 前缀匹配（记录里存完整 ID，docker ps 给 12 位短 ID），
// 其次按容器名匹配。
func findContainer(ref ContainerRef, states []ContainerState) (ContainerState, bool) {
	if ref.ID != "" {
		for _, state := range states {
			if state.ID == "" {
				continue
			}
			if strings.HasPrefix(ref.ID, state.ID) || strings.HasPrefix(state.ID, ref.ID) {
				return state, true
			}
		}
	}
	if ref.Name != "" {
		for _, state := range states {
			if state.Name == ref.Name {
				return state, true
			}
		}
	}
	return ContainerState{}, false
}

// firstAccessURL 取记录中第一条端口映射的访问地址。
func firstAccessURL(ports []ResolvedPort) string {
	if len(ports) == 0 {
		return ""
	}
	return ports[0].AccessURL
}
