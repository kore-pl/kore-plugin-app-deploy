package apop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestRemoveRequiresConfirm confirm 缺失或 false 一律拒绝，且不动任何资源。
func TestRemoveRequiresConfirm(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy", `{"template":"nginx-static","name":"kore-app-x"}`)
	before := len(exec.states)

	for _, params := range []string{
		`{"name":"kore-app-x"}`,
		`{"name":"kore-app-x","confirm":false}`,
	} {
		perr := callErr(t, rt, "app-deploy.remove", params, pluginapi.E_INVALID)
		if !strings.Contains(perr.Message, "confirm") {
			t.Fatalf("错误信息应提示 confirm，实际 %q", perr.Message)
		}
	}
	if len(exec.removedContainers) != 0 {
		t.Fatalf("未确认却删了容器: %+v", exec.removedContainers)
	}
	if len(exec.states) != before {
		t.Fatalf("未确认却改了容器状态: %d → %d", before, len(exec.states))
	}
	if st := readState(t, rt); len(st.Deployments) != 1 {
		t.Fatalf("未确认却改了记录: %+v", st.Deployments)
	}
}

// TestRemoveHappyPath 覆盖单容器删除：容器按记录 ID 删除、记录清空。
func TestRemoveHappyPath(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy", `{"template":"nginx-static","name":"kore-app-x"}`)
	raw := callOK(t, rt, "app-deploy.remove", `{"name":"kore-app-x","confirm":true}`)
	var res RemoveResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("解析结果失败: %v（%s）", err, raw)
	}
	if !res.Removed || res.Name != "kore-app-x" {
		t.Fatalf("结果不符: %+v", res)
	}
	if len(res.Containers) != 1 || res.Containers[0] != "kore-app-x" {
		t.Fatalf("删除的容器名不符: %+v", res.Containers)
	}
	if len(exec.removedContainers) != 1 {
		t.Fatalf("容器未删除: %+v", exec.removedContainers)
	}
	if st := readState(t, rt); len(st.Deployments) != 0 {
		t.Fatalf("记录未清空: %+v", st.Deployments)
	}
	// bind 目录保留（可能含用户文件），不会被删。
	host := filepath.Join(rt.opts.StateDir, VolumesDir, "kore-app-x", "html")
	if _, err := os.Stat(host); err != nil {
		t.Fatalf("bind 目录不应被删除: %v", err)
	}
	// 再删一次 → E_NOTFOUND（记录已不在，docker 里也没有同名容器）。
	callErr(t, rt, "app-deploy.remove", `{"name":"kore-app-x","confirm":true}`, pluginapi.E_NOTFOUND)
}

// TestRemoveWordpressCleansVolumesAndNetwork 双容器删除：卷与网络一起清理。
func TestRemoveWordpressCleansVolumesAndNetwork(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy", `{"template":"wordpress","name":"kore-app-wp"}`)
	raw := callOK(t, rt, "app-deploy.remove", `{"name":"kore-app-wp","confirm":true}`)
	var res RemoveResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("解析结果失败: %v", err)
	}
	if len(exec.removedContainers) != 2 {
		t.Fatalf("应删除两个容器，实际 %+v", exec.removedContainers)
	}
	if len(exec.removedVolumes) != 2 {
		t.Fatalf("应删除两个命名卷，实际 %+v", exec.removedVolumes)
	}
	for _, volume := range exec.removedVolumes {
		if !strings.HasPrefix(volume, ContainerPrefix) {
			t.Fatalf("删除了非本插件卷: %q", volume)
		}
	}
	if len(exec.removedNetworks) != 1 || exec.removedNetworks[0] != "kore-app-wp-net" {
		t.Fatalf("网络未删除: %+v", exec.removedNetworks)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("正常删除不应有告警: %+v", res.Warnings)
	}
	if st := readState(t, rt); len(st.Deployments) != 0 {
		t.Fatalf("记录未清空: %+v", st.Deployments)
	}
}

// TestRemoveForeignContainer 记录里没有的容器一律拒绝（E_UNAUTHORIZED）。
func TestRemoveForeignContainer(t *testing.T) {
	exec := newFakeExecutor()
	exec.addForeign("kore-app-foreign")
	rt := testRuntime(t, exec)
	perr := callErr(t, rt, "app-deploy.remove", `{"name":"kore-app-foreign","confirm":true}`, pluginapi.E_UNAUTHORIZED)
	if !strings.Contains(perr.Message, "非本插件创建") {
		t.Fatalf("错误信息应说明归属，实际 %q", perr.Message)
	}
	if len(exec.removedContainers) != 0 {
		t.Fatalf("竟删了非本插件容器: %+v", exec.removedContainers)
	}
	// 连名字都无法寻址（无前缀）→ 第一道防线 E_INVALID。
	callErr(t, rt, "app-deploy.remove", `{"name":"other-app","confirm":true}`, pluginapi.E_INVALID)
}

// TestRemoveTamperedRecordRefused 记录被篡改成非本插件容器名时拒绝删除。
func TestRemoveTamperedRecordRefused(t *testing.T) {
	exec := newFakeExecutor()
	exec.addForeign("mysql-prod")
	rt := testRuntime(t, exec)
	if err := ensureStateDir(rt.opts.StateDir); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	tampered := stateFile{Version: StateVersion, Deployments: []Deployment{{
		ID:          "deadbeef",
		Template:    "nginx-static",
		Name:        "kore-app-x",
		ContainerID: "0000000000000000000000000000000000000000000000000000000000000001",
		Containers: []ContainerRef{{
			Service: "web",
			Name:    "mysql-prod",
			ID:      "0000000000000000000000000000000000000000000000000000000000000002",
		}},
		Status: StatusRunning,
	}}}
	if perr := saveState(rt.opts.StateDir, tampered); perr != nil {
		t.Fatalf("写记录失败: %+v", perr)
	}
	callErr(t, rt, "app-deploy.remove", `{"name":"kore-app-x","confirm":true}`, pluginapi.E_UNAUTHORIZED)
	if len(exec.removedContainers) != 0 {
		t.Fatalf("篡改记录导致误删: %+v", exec.removedContainers)
	}
}

// TestRemoveUnknownDeployment 记录与容器都不存在 → E_NOTFOUND。
func TestRemoveUnknownDeployment(t *testing.T) {
	rt := testRuntime(t, newFakeExecutor())
	callErr(t, rt, "app-deploy.remove", `{"name":"kore-app-none","confirm":true}`, pluginapi.E_NOTFOUND)
}

// TestRemovePartialWarning 卷/网络删除失败以 warnings 返回，不把已完成的容器删除判为失败。
func TestRemovePartialWarning(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy", `{"template":"wordpress","name":"kore-app-wp"}`)
	exec.volErr = NewFaultError(FaultDisk, "卷删除失败")
	raw := callOK(t, rt, "app-deploy.remove", `{"name":"kore-app-wp","confirm":true}`)
	var res RemoveResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if !res.Removed || len(res.Warnings) == 0 {
		t.Fatalf("卷删除失败应降级为告警: %+v", res)
	}
	if st := readState(t, rt); len(st.Deployments) != 0 {
		t.Fatalf("记录应被清理: %+v", st.Deployments)
	}
}
