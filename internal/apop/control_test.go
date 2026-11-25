package apop

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// bareExecutor 只实现部署面 Executor（没有控制面 Controller），
// 用于验证「执行面不支持停止/启动/日志」时报 E_UNSUPPORTED 而不是静默降级。
type bareExecutor struct{}

func (bareExecutor) RunContainer(ContainerSpec) (string, error) { return "", nil }
func (bareExecutor) ListContainers() ([]ContainerState, error)  { return nil, nil }
func (bareExecutor) RemoveContainers([]string) error            { return nil }
func (bareExecutor) RemoveVolumes([]string) error               { return nil }
func (bareExecutor) CreateNetwork(string) error                 { return nil }
func (bareExecutor) RemoveNetwork(string) error                 { return nil }

// TestStopStartLifecycle 覆盖停止/启动的正常闭环：结果字段、容器快照、list 视图跟随，
// 以及「控制操作不改写部署记录」这一约定（记录 status 是历史值，视图一律现算）。
func TestStopStartLifecycle(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy", "{\"template\":\"nginx-static\",\"name\":\"kore-app-svc\"}")

	var stopped ControlResult
	if err := json.Unmarshal([]byte(callOK(t, rt, "app-deploy.stop", "{\"name\":\"kore-app-svc\"}")), &stopped); err != nil {
		t.Fatalf("解析 stop 结果失败: %v", err)
	}
	if stopped.Action != "stop" || stopped.Status != StatusExited || stopped.Name != "kore-app-svc" {
		t.Fatalf("stop 结果不符: %+v", stopped)
	}
	if len(stopped.Containers) != 1 || stopped.Containers[0] != "kore-app-svc" {
		t.Fatalf("stop 容器列表不符: %+v", stopped.Containers)
	}
	if got := listOne(t, rt, "kore-app-svc"); got.Status != StatusExited {
		t.Fatalf("停止后 list 状态=%q，期望 %q", got.Status, StatusExited)
	}
	if st := readState(t, rt); st.Deployments[0].Status != StatusRunning {
		t.Fatalf("控制操作不应改写记录里的 status，实际 %q", st.Deployments[0].Status)
	}

	var started ControlResult
	if err := json.Unmarshal([]byte(callOK(t, rt, "app-deploy.start", "{\"name\":\"kore-app-svc\"}")), &started); err != nil {
		t.Fatalf("解析 start 结果失败: %v", err)
	}
	if started.Action != "start" || started.Status != StatusRunning {
		t.Fatalf("start 结果不符: %+v", started)
	}
	if got := listOne(t, rt, "kore-app-svc"); got.Status != StatusRunning {
		t.Fatalf("启动后 list 状态=%q，期望 %q", got.Status, StatusRunning)
	}
}

// TestStopMultiContainer 双容器模板一次停掉全部容器（不是只停主容器）。
func TestStopMultiContainer(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy", "{\"template\":\"wordpress\",\"name\":\"kore-app-wp\"}")
	var res ControlResult
	if err := json.Unmarshal([]byte(callOK(t, rt, "app-deploy.stop", "{\"name\":\"kore-app-wp\"}")), &res); err != nil {
		t.Fatalf("解析 stop 结果失败: %v", err)
	}
	if len(res.Containers) != 2 {
		t.Fatalf("双容器模板 stop 容器数=%d，期望 2（%v）", len(res.Containers), res.Containers)
	}
	if got := listOne(t, rt, "kore-app-wp"); got.Status != StatusExited {
		t.Fatalf("双容器停止后状态=%q，期望 %q", got.Status, StatusExited)
	}
}

// TestControlErrors 覆盖「应用不存在 / 名字非法 / 执行面失败」。
func TestControlErrors(t *testing.T) {
	rt := testRuntime(t, newFakeExecutor())
	perr := callErr(t, rt, "app-deploy.stop", "{\"name\":\"kore-app-ghost\"}", pluginapi.E_NOTFOUND)
	if !strings.Contains(perr.Message, "从模板部署") {
		t.Fatalf("不存在时应给下一步动作，实际 %q", perr.Message)
	}
	callErr(t, rt, "app-deploy.stop", "{\"name\":\"Bad Name\"}", pluginapi.E_INVALID)

	faulty := newFakeExecutor()
	faulty.ctrlErr = NewFaultError(FaultTimeout, "docker stop 无响应")
	faultyRT := testRuntime(t, faulty)
	callOK(t, faultyRT, "app-deploy.deploy", "{\"template\":\"nginx-static\",\"name\":\"kore-app-svc\"}")
	callErr(t, faultyRT, "app-deploy.stop", "{\"name\":\"kore-app-svc\"}", pluginapi.E_UPSTREAM)
	callErr(t, faultyRT, "app-deploy.start", "{\"name\":\"kore-app-svc\"}", pluginapi.E_UPSTREAM)
}

// TestControlUnsupportedWithoutController 执行面没有控制面时显式报 E_UNSUPPORTED（不静默）。
func TestControlUnsupportedWithoutController(t *testing.T) {
	dir := t.TempDir()
	rt := NewRuntime(Options{
		Root: dir, StateDir: dir + "/state", GOOS: "linux",
		PortProbe: func(int) bool { return true },
	}, bareExecutor{})
	callOK(t, rt, "app-deploy.deploy", "{\"template\":\"nginx-static\",\"name\":\"kore-app-bare\"}")
	for _, method := range []string{"app-deploy.stop", "app-deploy.start", "app-deploy.logs"} {
		perr := callErr(t, rt, method, "{\"name\":\"kore-app-bare\"}", pluginapi.E_UNSUPPORTED)
		if !strings.Contains(perr.Message, "控制能力") {
			t.Fatalf("%s 的错误应写明缺什么，实际 %q", method, perr.Message)
		}
	}
}

// TestLogsReadsPrimaryAndService 覆盖日志读取：默认主容器、指定服务、未知服务。
func TestLogsReadsPrimaryAndService(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy", "{\"template\":\"wordpress\",\"name\":\"kore-app-wp\"}")
	exec.setLogs("kore-app-wp", "booting\nready\n")
	exec.setLogs("kore-app-wp-db", "mysql ready\n")

	var res LogsResult
	if err := json.Unmarshal([]byte(callOK(t, rt, "app-deploy.logs", "{\"name\":\"kore-app-wp\"}")), &res); err != nil {
		t.Fatalf("解析 logs 结果失败: %v", err)
	}
	if res.Container != "kore-app-wp" || res.Service != "web" {
		t.Fatalf("默认应读主容器，实际 container=%q service=%q", res.Container, res.Service)
	}
	if len(res.Lines) != 2 || res.Lines[0] != "booting" || res.Lines[1] != "ready" {
		t.Fatalf("日志行不符: %v", res.Lines)
	}

	if err := json.Unmarshal([]byte(callOK(t, rt, "app-deploy.logs", "{\"name\":\"kore-app-wp\",\"service\":\"db\"}")), &res); err != nil {
		t.Fatalf("解析 db 日志失败: %v", err)
	}
	if res.Container != "kore-app-wp-db" {
		t.Fatalf("指定 service=db 应读 db 容器，实际 %q", res.Container)
	}
	perr := callErr(t, rt, "app-deploy.logs", "{\"name\":\"kore-app-wp\",\"service\":\"nope\"}", pluginapi.E_INVALID)
	if !strings.Contains(perr.Message, "web") || !strings.Contains(perr.Message, "db") {
		t.Fatalf("未知服务应列出可选服务，实际 %q", perr.Message)
	}
}

// TestLogsTailClampTruncate 覆盖 tail 上限与字节上限：只保留尾部，且标记 truncated。
func TestLogsTailClampTruncate(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy", "{\"template\":\"nginx-static\",\"name\":\"kore-app-log\"}")
	big := strings.Repeat("x", MaxLogBytes+512) + "\nlast-line\n"
	exec.setLogs("kore-app-log", big)

	var res LogsResult
	raw := callOK(t, rt, "app-deploy.logs", "{\"name\":\"kore-app-log\",\"tail\":999999}")
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("解析 logs 结果失败: %v", err)
	}
	if res.Tail != MaxLogTail {
		t.Fatalf("tail=%d，期望被夹到上限 %d", res.Tail, MaxLogTail)
	}
	if !res.Truncated {
		t.Fatal("超过字节上限时必须标记 truncated=true")
	}
	if got := res.Lines[len(res.Lines)-1]; got != "last-line" {
		t.Fatalf("应保留最后一行为 last-line，实际 %q", got)
	}
	if size := len(strings.Join(res.Lines, "\n")); size > MaxLogBytes {
		t.Fatalf("返回日志 %d 字节超过上限 %d", size, MaxLogBytes)
	}

	// 空日志：返回空数组而不是 null（前端直接 join）。
	var empty LogsResult
	if err := json.Unmarshal([]byte(callOK(t, rt, "app-deploy.logs", "{\"name\":\"kore-app-log\"}")), &empty); err != nil {
		t.Fatalf("解析空日志失败: %v", err)
	}
	if empty.Lines == nil {
		t.Fatal("lines 应为空数组而不是 null")
	}
}
