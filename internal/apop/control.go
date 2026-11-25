package apop

import (
	"encoding/json"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 日志读取边界：默认 200 行、上限 2000 行、单次最多返回 64 KiB（防一次性把浏览器拖死）。
const (
	DefaultLogTail = 200
	MaxLogTail     = 2000
	MaxLogBytes    = 64 * 1024
)

// controller 取执行面的控制能力；不支持时返回 E_UNSUPPORTED 并写明缺什么（G-16 禁止静默降级）。
func (r *Runtime) controller() (Controller, *pluginapi.Error) {
	ctrl, ok := r.exec.(Controller)
	if !ok {
		return nil, unsupported("当前执行面没有控制能力（停止/启动/读日志）：请使用带 docker CLI 的执行面，或升级应用部署插件")
	}
	return ctrl, nil
}

// lookupDeployment 按部署名取记录；不存在时给「怎么办」而不是只报错码。
func (r *Runtime) lookupDeployment(name string) (Deployment, *pluginapi.Error) {
	if perr := ValidateName(name); perr != nil {
		return Deployment{}, perr
	}
	st, perr := loadState(r.opts.StateDir)
	if perr != nil {
		return Deployment{}, perr
	}
	idx := findDeployment(st, name)
	if idx < 0 {
		return Deployment{}, &pluginapi.Error{
			Code:    pluginapi.E_NOTFOUND,
			Message: "应用不存在: " + name + "（先「从模板部署」创建，再管理它）",
		}
	}
	return st.Deployments[idx], nil
}

// deploymentRefs 返回记录里的容器身份；兼容只有 container_id 的早期记录形状。
func deploymentRefs(rec Deployment) []ContainerRef {
	if len(rec.Containers) > 0 {
		return rec.Containers
	}
	return []ContainerRef{{Name: rec.Name, ID: rec.ContainerID}}
}

// deploymentContainerNames 取可操作的容器名（docker 的 stop/start/logs 都接受名字，
// 名字带 kore-app- 前缀且与记录一一对应；名字缺失时退回容器 ID）。
func deploymentContainerNames(rec Deployment) []string {
	refs := deploymentRefs(rec)
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		name := ref.Name
		if name == "" {
			name = ref.ID
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

// callStop / callStart 是同一套逻辑的两个方向：只动容器，不改部署记录
// （记录里的 status 只是历史值，对外视图一律按 docker ps 现算，见 list.go）。
func (r *Runtime) callStop(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	return r.callControl(raw, "stop")
}

// callStart 处理 app-deploy.start。
func (r *Runtime) callStart(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	return r.callControl(raw, "start")
}

// callControl 执行停止/启动：校验名字 → 取记录 → 取执行面 → 批量控制。
func (r *Runtime) callControl(raw json.RawMessage, action string) (json.RawMessage, *pluginapi.Error) {
	var p controlParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	rec, perr := r.lookupDeployment(p.Name)
	if perr != nil {
		return nil, perr
	}
	ctrl, perr := r.controller()
	if perr != nil {
		return nil, perr
	}
	names := deploymentContainerNames(rec)
	if len(names) == 0 {
		return nil, internalErr("部署记录里没有可操作的容器: " + p.Name + "（记录已损坏，请移除后重新部署）")
	}
	var (
		err    error
		status string
	)
	switch action {
	case "stop":
		err = ctrl.StopContainers(names)
		status = StatusExited
	case "start":
		err = ctrl.StartContainers(names)
		status = StatusRunning
	default:
		return nil, internalErr("未知控制动作: " + action)
	}
	if err != nil {
		return nil, classify(err)
	}
	return encode(ControlResult{Name: p.Name, Action: action, Status: status, Containers: names})
}

// callLogs 处理 app-deploy.logs：读主容器（或指定服务）最近 tail 行日志。
// 只读：不动容器、不改记录。
func (r *Runtime) callLogs(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p logsParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	rec, perr := r.lookupDeployment(p.Name)
	if perr != nil {
		return nil, perr
	}
	ctrl, perr := r.controller()
	if perr != nil {
		return nil, perr
	}
	ref, perr := pickLogContainer(rec, p.Service)
	if perr != nil {
		return nil, perr
	}
	tail := p.Tail
	if tail <= 0 {
		tail = DefaultLogTail
	}
	if tail > MaxLogTail {
		tail = MaxLogTail
	}
	out, err := ctrl.ContainerLogs(ref.Name, tail)
	if err != nil {
		return nil, classify(err)
	}
	lines, truncated := splitLogLines(out, MaxLogBytes)
	return encode(LogsResult{
		Name:      rec.Name,
		Service:   ref.Service,
		Container: ref.Name,
		Tail:      tail,
		Lines:     lines,
		Truncated: truncated,
	})
}

// pickLogContainer 选看哪个容器的日志：显式服务优先，其次主容器（ContainerID），最后第一个。
func pickLogContainer(rec Deployment, service string) (ContainerRef, *pluginapi.Error) {
	refs := deploymentRefs(rec)
	if service != "" {
		for _, ref := range refs {
			if ref.Service == service {
				return ref, nil
			}
		}
		available := make([]string, 0, len(refs))
		for _, ref := range refs {
			if ref.Service != "" {
				available = append(available, ref.Service)
			}
		}
		return ContainerRef{}, invalid("部署 " + rec.Name + " 没有服务 " + service + "（可选：" + strings.Join(available, ", ") + "）")
	}
	if rec.ContainerID != "" {
		for _, ref := range refs {
			if strings.HasPrefix(ref.ID, rec.ContainerID) || strings.HasPrefix(rec.ContainerID, ref.ID) {
				return ref, nil
			}
		}
	}
	if len(refs) == 0 {
		return ContainerRef{}, internalErr("部署记录里没有容器: " + rec.Name)
	}
	return refs[0], nil
}

// splitLogLines 把日志原文切成行；超过 maxBytes 时只保留尾部并丢弃被截断的半行。
func splitLogLines(raw string, maxBytes int) ([]string, bool) {
	truncated := false
	if len(raw) > maxBytes {
		raw = raw[len(raw)-maxBytes:]
		if idx := strings.IndexByte(raw, '\n'); idx >= 0 {
			raw = raw[idx+1:]
		}
		truncated = true
	}
	raw = strings.TrimSuffix(raw, "\n")
	if raw == "" {
		return []string{}, truncated
	}
	return strings.Split(raw, "\n"), truncated
}
