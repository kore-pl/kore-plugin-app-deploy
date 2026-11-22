package apop

import (
	"bytes"
	"encoding/json"
	"errors"
	"runtime"
	"strings"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// Call 是包级便捷入口：按 opts 构造 Runtime 并执行一个 app-deploy 方法。
func Call(method string, params json.RawMessage, opts Options, exec Executor) (json.RawMessage, *pluginapi.Error) {
	return NewRuntime(opts, exec).Call(method, params)
}

// Runtime 是 app-deploy 方法面的生命周期容器：持有执行面与运行期配置。
type Runtime struct {
	opts Options
	exec Executor
}

// NewRuntime 构造 Runtime；exec 为 nil 时使用真实 docker CLI 执行面。
func NewRuntime(opts Options, exec Executor) *Runtime {
	opts = opts.normalize()
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if exec == nil {
		exec = NewDockerClient(opts.DockerBin, opts.Timeout)
	}
	return &Runtime{opts: opts, exec: exec}
}

// Call 执行一个 app-deploy.<verb> 方法。
//
// 平台边界：真实部署依赖 Linux 上的 docker CLI，非 Linux 返回 E_UNSUPPORTED，
// 不做「看起来成功」的静默降级（G-16 降级矩阵要求显式）。
func (r *Runtime) Call(method string, params json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	if r.opts.GOOS != "linux" {
		return nil, unsupported("app-deploy 只在 Linux 提供真实部署能力（当前 " + r.opts.GOOS + "）")
	}
	verb, ok := businessVerb(method)
	if !ok {
		return nil, notFound("未知方法: " + method)
	}
	switch verb {
	case "templates":
		return r.callTemplates(params)
	case "deploy":
		return r.callDeploy(params)
	case "list":
		return r.callList(params)
	case "remove":
		return r.callRemove(params)
	default:
		return nil, notFound("未知方法: " + method)
	}
}

// methodPrefix 是方法名的插件域前缀（PLUGIN-PROTOCOL §7.2）。
const methodPrefix = PluginID + "."

// businessVerb 把 app-deploy.<verb> 归一为动词名；域不是本插件 id 的一律拒绝。
func businessVerb(method string) (string, bool) {
	verb, ok := strings.CutPrefix(method, methodPrefix)
	if !ok {
		return "", false
	}
	switch verb {
	case "templates", "deploy", "list", "remove":
		return verb, true
	}
	return "", false
}

// classify 把执行面错误映射为协议错误码：四类故障各有对应，其次 OpError，最后 E_INTERNAL。
func classify(err error) *pluginapi.Error {
	var faultErr *FaultError
	if errors.As(err, &faultErr) {
		switch faultErr.Kind {
		case FaultKill:
			return upstream("docker 进程被信号 9 终止: " + faultErr.Message)
		case FaultTimeout:
			return upstream("调用超时: " + faultErr.Message)
		case FaultDisk:
			return internalErr("磁盘满: " + faultErr.Message)
		case FaultCgroup:
			return unsupported("cgroup 限额触发: " + faultErr.Message)
		}
	}
	var opErr *OpError
	if errors.As(err, &opErr) {
		return &pluginapi.Error{Code: opErr.Code, Message: opErr.Message}
	}
	return internalErr("执行面失败: " + err.Error())
}

// decodeParams 校验 params 是 JSON 对象并反序列化到目标形状；空值/null 视为空对象。
func decodeParams(raw json.RawMessage, target any) *pluginapi.Error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	if trimmed[0] != '{' {
		return invalid("params 必须是 JSON 对象")
	}
	if err := json.Unmarshal(trimmed, target); err != nil {
		return invalid("params 解析失败: " + err.Error())
	}
	return nil
}

// encode 把结果编码为 json.RawMessage；序列化失败归 E_INTERNAL。
func encode(v any) (json.RawMessage, *pluginapi.Error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, internalErr("序列化结果失败: " + err.Error())
	}
	return data, nil
}

// 协议错误构造器。
func invalid(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_INVALID, Message: msg}
}

func notFound(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_NOTFOUND, Message: msg}
}

func conflict(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_CONFLICT, Message: msg}
}

func unauthorized(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_UNAUTHORIZED, Message: msg}
}

func unsupported(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_UNSUPPORTED, Message: msg}
}

func upstream(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_UPSTREAM, Message: msg}
}

func internalErr(msg string) *pluginapi.Error {
	return &pluginapi.Error{Code: pluginapi.E_INTERNAL, Message: msg}
}
