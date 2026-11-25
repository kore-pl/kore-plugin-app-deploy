package apop

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// fakeExecutor 是内存版执行面：记录调用、可注入故障、可模拟 docker ps 快照。
type fakeExecutor struct {
	mu sync.Mutex

	// states 是 docker ps 快照（含已停止）。
	states []ContainerState
	// runSpecs 记录每次 docker run 的完整规格。
	runSpecs []ContainerSpec
	// envFiles 记录每次 run 时 env-file 的实际内容与权限。
	envContents map[string]string
	envModes    map[string]os.FileMode

	// runErr / listErr / rmErr / volErr / netErr 用于注入执行面错误。
	runErr  error
	listErr error
	rmErr   error
	volErr  error
	netErr  error
	// failOn 指定某个容器名的 docker run 失败（验证回滚）。
	failOn map[string]error

	// ctrlErr / logsErr / usageErr 用于注入控制面错误；logs / usage 是按容器名登记的假数据。
	ctrlErr  error
	logsErr  error
	usageErr error
	logs     map[string]string
	usage    map[string]ContainerUsage

	removedContainers []string
	removedVolumes    []string
	removedNetworks   []string
	createdNetworks   []string
	calls             []string

	seq int
}

// newFakeExecutor 构造空的假执行面。
func newFakeExecutor() *fakeExecutor {
	return &fakeExecutor{
		envContents: map[string]string{},
		envModes:    map[string]os.FileMode{},
		failOn:      map[string]error{},
	}
}

// RunContainer 记录规格、读取 env-file（插件随后会删除），并把容器加入快照。
func (f *fakeExecutor) RunContainer(spec ContainerSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "run:"+spec.Name)
	if err := f.runErr; err != nil {
		return "", err
	}
	if err, ok := f.failOn[spec.Name]; ok {
		return "", err
	}
	if spec.EnvFile != "" {
		data, err := os.ReadFile(spec.EnvFile)
		if err != nil {
			return "", fmt.Errorf("读取 env-file 失败: %w", err)
		}
		info, err := os.Stat(spec.EnvFile)
		if err != nil {
			return "", fmt.Errorf("stat env-file 失败: %w", err)
		}
		f.envContents[spec.Name] = string(data)
		f.envModes[spec.Name] = info.Mode().Perm()
	}
	f.seq++
	id := fakeContainerID(f.seq)
	f.runSpecs = append(f.runSpecs, spec)
	f.states = append(f.states, ContainerState{
		ID:    id[:12],
		Name:  spec.Name,
		Image: spec.Image,
		State: StatusRunning,
		Ports: publishedPorts(spec.Ports),
	})
	return id, nil
}

// fakeContainerID 生成前 12 位唯一、总长 64 位的假容器 ID（对齐 docker 的真实形态）。
func fakeContainerID(seq int) string {
	return fmt.Sprintf("%012x", seq) + strings.Repeat("a", 52)
}

// publishedPorts 把端口绑定渲染成 docker ps 风格字符串（供端口占用解析）。
func publishedPorts(ports []PortBinding) string {
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, fmt.Sprintf("127.0.0.1:%d->%d/%s", port.HostPort, port.ContainerPort, port.Protocol))
	}
	return strings.Join(parts, ", ")
}

// ListContainers 返回快照副本。
func (f *fakeExecutor) ListContainers() ([]ContainerState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "list")
	if err := f.listErr; err != nil {
		return nil, err
	}
	out := make([]ContainerState, len(f.states))
	copy(out, f.states)
	return out, nil
}

// RemoveContainers 记录并移除快照中的容器。
func (f *fakeExecutor) RemoveContainers(ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "rm:"+strings.Join(ids, ","))
	if err := f.rmErr; err != nil {
		return err
	}
	f.removedContainers = append(f.removedContainers, ids...)
	kept := make([]ContainerState, 0, len(f.states))
	for _, state := range f.states {
		drop := false
		for _, id := range ids {
			if len(state.ID) > 0 && (strings.HasPrefix(id, state.ID) || strings.HasPrefix(state.ID, id)) {
				drop = true
				break
			}
		}
		if !drop {
			kept = append(kept, state)
		}
	}
	f.states = kept
	return nil
}

// RemoveVolumes 记录删除的卷名。
func (f *fakeExecutor) RemoveVolumes(names []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "volume-rm:"+strings.Join(names, ","))
	if err := f.volErr; err != nil {
		return err
	}
	f.removedVolumes = append(f.removedVolumes, names...)
	return nil
}

// CreateNetwork 记录创建的网络。
func (f *fakeExecutor) CreateNetwork(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "network-create:"+name)
	if err := f.netErr; err != nil {
		return err
	}
	f.createdNetworks = append(f.createdNetworks, name)
	return nil
}

// RemoveNetwork 记录删除的网络。
func (f *fakeExecutor) RemoveNetwork(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "network-rm:"+name)
	if err := f.netErr; err != nil {
		return err
	}
	f.removedNetworks = append(f.removedNetworks, name)
	return nil
}

// setState 改写某个容器的状态（模拟容器退出/消失）。
func (f *fakeExecutor) setState(name, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.states {
		if f.states[i].Name == name {
			f.states[i].State = state
			return
		}
	}
}

// dropState 从快照里移除容器（模拟被外部删除）。
func (f *fakeExecutor) dropState(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := make([]ContainerState, 0, len(f.states))
	for _, state := range f.states {
		if state.Name != name {
			kept = append(kept, state)
		}
	}
	f.states = kept
}

// addState 往快照里塞一个容器（模拟宿主上已有的容器）。
func (f *fakeExecutor) addState(state ContainerState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if state.ID == "" {
		state.ID = fmt.Sprintf("%012x", len(f.states)+1)
	}
	f.states = append(f.states, state)
}

// addForeign 往快照里塞一个非本插件创建的容器。
func (f *fakeExecutor) addForeign(name string) {
	f.addState(ContainerState{Name: name, Image: "other:1", State: StatusRunning})
}

// argvJoined 把全部 run 规格的 argv 拼成一个串（断言敏感值不出现在 argv）。
func (f *fakeExecutor) argvJoined() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := make([]string, 0, len(f.runSpecs))
	for _, spec := range f.runSpecs {
		parts = append(parts, BuildRunArgs(spec)...)
	}
	return strings.Join(parts, " ")
}

// ── 控制面（Controller）：停止 / 启动 / 日志 / 资源占用采样 ─────────────────

// StopContainers 把快照里的容器置为 exited；ctrlErr 可注入失败。
func (f *fakeExecutor) StopContainers(ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "stop:"+strings.Join(ids, ","))
	if err := f.ctrlErr; err != nil {
		return err
	}
	for _, id := range ids {
		for i := range f.states {
			if f.states[i].Name == id {
				f.states[i].State = StatusExited
			}
		}
	}
	return nil
}

// StartContainers 把快照里的容器置为 running。
func (f *fakeExecutor) StartContainers(ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "start:"+strings.Join(ids, ","))
	if err := f.ctrlErr; err != nil {
		return err
	}
	for _, id := range ids {
		for i := range f.states {
			if f.states[i].Name == id {
				f.states[i].State = StatusRunning
			}
		}
	}
	return nil
}

// ContainerLogs 返回按容器名登记的假日志；未登记时返回空串。
func (f *fakeExecutor) ContainerLogs(name string, tail int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "logs:"+name)
	if err := f.logsErr; err != nil {
		return "", err
	}
	return f.logs[name], nil
}

// SampleUsage 返回按容器名登记的假资源占用。
func (f *fakeExecutor) SampleUsage(names []string) ([]ContainerUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "stats:"+strings.Join(names, ","))
	if err := f.usageErr; err != nil {
		return nil, err
	}
	out := make([]ContainerUsage, 0, len(names))
	for _, name := range names {
		if usage, ok := f.usage[name]; ok {
			out = append(out, usage)
		}
	}
	return out, nil
}

// setLogs 登记某个容器的假日志输出。
func (f *fakeExecutor) setLogs(name, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logs == nil {
		f.logs = map[string]string{}
	}
	f.logs[name] = body
}

// setUsage 登记某个容器的假资源占用。
func (f *fakeExecutor) setUsage(name string, usage ContainerUsage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.usage == nil {
		f.usage = map[string]ContainerUsage{}
	}
	f.usage[name] = usage
}

// testRuntime 构造指向临时状态目录、端口全可用的 Runtime。
func testRuntime(t *testing.T, exec Executor) *Runtime {
	t.Helper()
	dir := t.TempDir()
	return NewRuntime(Options{
		Root:      dir,
		StateDir:  filepath.Join(dir, "state"),
		GOOS:      "linux",
		PortProbe: func(int) bool { return true },
	}, exec)
}

// callOK 调用方法并要求成功，返回结果原文。
func callOK(t *testing.T, rt *Runtime, method, params string) string {
	t.Helper()
	result, perr := rt.Call(method, json.RawMessage(params))
	if perr != nil {
		t.Fatalf("%s 意外失败: %+v", method, perr)
	}
	return string(result)
}

// callErr 调用方法并要求失败，返回协议错误。
func callErr(t *testing.T, rt *Runtime, method, params, wantCode string) *pluginapi.Error {
	t.Helper()
	result, perr := rt.Call(method, json.RawMessage(params))
	if perr == nil {
		t.Fatalf("%s 期望失败（%s），实际成功: %s", method, wantCode, result)
	}
	if result != nil {
		t.Fatalf("%s 出错时不应带结果: %s", method, result)
	}
	if perr.Code != wantCode {
		t.Fatalf("%s 错误码=%q，期望 %q（message=%q）", method, perr.Code, wantCode, perr.Message)
	}
	return perr
}

// readState 读取部署记录文件原文。
func readState(t *testing.T, rt *Runtime) stateFile {
	t.Helper()
	st, perr := loadState(rt.opts.StateDir)
	if perr != nil {
		t.Fatalf("读取部署记录失败: %+v", perr)
	}
	return st
}
