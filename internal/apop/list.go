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
	views := make([]DeploymentView, 0, len(st.Deployments))
	for _, rec := range st.Deployments {
		views = append(views, buildDeploymentView(rec, states))
	}
	return encode(ListResult{Deployments: views, Count: len(views)})
}

// buildDeploymentView 把一条记录与 docker 快照合成对外视图。
func buildDeploymentView(rec Deployment, states []ContainerState) DeploymentView {
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
	return DeploymentView{
		ID:          rec.ID,
		Template:    rec.Template,
		Name:        rec.Name,
		ContainerID: rec.ContainerID,
		Status:      status,
		Ports:       rec.Ports,
		AccessURL:   firstAccessURL(rec.Ports),
		CreatedAt:   rec.CreatedAt,
		Containers:  containers,
	}
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
