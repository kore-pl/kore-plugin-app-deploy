package apop

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestListEmpty 没有部署时返回空数组（不是 null）。
func TestListEmpty(t *testing.T) {
	rt := testRuntime(t, newFakeExecutor())
	raw := callOK(t, rt, "app-deploy.list", `{}`)
	if !contains(raw, `"deployments":[]`) || !contains(raw, `"count":0`) {
		t.Fatalf("空列表结果不符: %s", raw)
	}
}

// TestListStatusTransitions 覆盖 running / exited / missing 三态。
func TestListStatusTransitions(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy", `{"template":"nginx-static","name":"kore-app-x"}`)

	if got := listOne(t, rt, "kore-app-x"); got.Status != StatusRunning {
		t.Fatalf("刚部署应为 running，实际 %q", got.Status)
	}
	exec.setState("kore-app-x", "exited")
	if got := listOne(t, rt, "kore-app-x"); got.Status != StatusExited {
		t.Fatalf("容器退出后应为 exited，实际 %q", got.Status)
	}
	exec.dropState("kore-app-x")
	if got := listOne(t, rt, "kore-app-x"); got.Status != StatusMissing {
		t.Fatalf("容器消失后应为 missing，实际 %q", got.Status)
	}
	// 记录仍在：list 是只读的，不清理记录。
	st := readState(t, rt)
	if len(st.Deployments) != 1 {
		t.Fatalf("list 不应修改记录，当前 %+v", st.Deployments)
	}
}

// TestListFields 覆盖 list 视图的字段完整性。
func TestListFields(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy", `{"template":"nginx-static","name":"kore-app-x"}`)
	view := listOne(t, rt, "kore-app-x")
	if view.ID == "" || view.Template != "nginx-static" || view.Name != "kore-app-x" {
		t.Fatalf("视图基础字段不符: %+v", view)
	}
	if view.ContainerID == "" || view.CreatedAt == "" || view.AccessURL != "http://127.0.0.1:18090/" {
		t.Fatalf("视图缺少 container_id/created_at/access_url: %+v", view)
	}
	if len(view.Containers) != 1 || view.Containers[0].Name != "kore-app-x" || view.Containers[0].ID == "" {
		t.Fatalf("视图容器列表不符: %+v", view.Containers)
	}
}

// TestListWordpressAggregate 双容器任一缺失 → 部署级 missing。
func TestListWordpressAggregate(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy", `{"template":"wordpress","name":"kore-app-wp"}`)
	if got := listOne(t, rt, "kore-app-wp"); got.Status != StatusRunning || len(got.Containers) != 2 {
		t.Fatalf("双容器部署状态不符: %+v", got)
	}
	exec.dropState("kore-app-wp-db")
	got := listOne(t, rt, "kore-app-wp")
	if got.Status != StatusMissing {
		t.Fatalf("db 缺失时部署级状态应为 missing，实际 %q", got.Status)
	}
	if len(got.Containers) != 2 {
		t.Fatalf("容器级条目数=%d，期望 2", len(got.Containers))
	}
	// 断言按名字定位（容器按创建顺序排列：依赖 db 在前），不依赖下标。
	byName := map[string]string{}
	for _, container := range got.Containers {
		byName[container.Name] = container.Status
	}
	if byName["kore-app-wp-db"] != StatusMissing || byName["kore-app-wp"] != StatusRunning {
		t.Fatalf("容器级状态不符: %+v", byName)
	}
}

// TestListCorruptState 记录损坏时返回 E_INTERNAL 而不是假装空列表。
func TestListCorruptState(t *testing.T) {
	rt := testRuntime(t, newFakeExecutor())
	if err := ensureStateDir(rt.opts.StateDir); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(statePath(rt.opts.StateDir), []byte("not json"), 0o600); err != nil {
		t.Fatalf("写坏文件失败: %v", err)
	}
	callErr(t, rt, "app-deploy.list", `{}`, pluginapi.E_INTERNAL)
}

// TestListUsageSummary 覆盖资源占用概览：聚合到部署级、停止后不再显示、采样失败要可见。
func TestListUsageSummary(t *testing.T) {
	exec := newFakeExecutor()
	rt := testRuntime(t, exec)
	callOK(t, rt, "app-deploy.deploy", `{"template":"wordpress","name":"kore-app-wp"}`)
	exec.setUsage("kore-app-wp", ContainerUsage{Name: "kore-app-wp", CPUPercent: 1.5, MemUsedBytes: 64 << 20, MemLimitBytes: 512 << 20})
	exec.setUsage("kore-app-wp-db", ContainerUsage{Name: "kore-app-wp-db", CPUPercent: 0.5, MemUsedBytes: 128 << 20, MemLimitBytes: 512 << 20})

	view := listOne(t, rt, "kore-app-wp")
	if view.Usage == nil {
		t.Fatal("运行中的部署应带 usage 概览")
	}
	if view.Usage.CPUPercent != 2.0 {
		t.Fatalf("CPU 聚合=%.2f，期望 2.00", view.Usage.CPUPercent)
	}
	if view.Usage.MemUsedBytes != 192<<20 || view.Usage.MemLimitBytes != 1024<<20 {
		t.Fatalf("内存聚合不符: used=%d limit=%d", view.Usage.MemUsedBytes, view.Usage.MemLimitBytes)
	}

	// 采样失败：列表照常返回，但必须把原因说出来（不能假装不占资源）。
	exec.usageErr = NewFaultError(FaultTimeout, "docker stats 超时")
	var res ListResult
	if err := json.Unmarshal([]byte(callOK(t, rt, "app-deploy.list", "{}")), &res); err != nil {
		t.Fatalf("解析 list 失败: %v", err)
	}
	if res.UsageError == "" || !strings.Contains(res.UsageError, "资源占用暂不可用") {
		t.Fatalf("采样失败应回 usage_error，实际 %q", res.UsageError)
	}
	for _, view := range res.Deployments {
		if view.Usage != nil {
			t.Fatalf("采样失败时不应带 usage: %+v", view.Usage)
		}
	}

	// 全部停止后不再采样（也不该显示占用）。
	exec.usageErr = nil
	callOK(t, rt, "app-deploy.stop", `{"name":"kore-app-wp"}`)
	if got := listOne(t, rt, "kore-app-wp"); got.Usage != nil {
		t.Fatalf("停止后不应带 usage: %+v", got.Usage)
	}
}

// listOne 调用 list 并取出指定部署的视图。
func listOne(t *testing.T, rt *Runtime, name string) DeploymentView {
	t.Helper()
	raw := callOK(t, rt, "app-deploy.list", `{}`)
	var res ListResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatalf("解析 list 失败: %v（%s）", err, raw)
	}
	if res.Count != len(res.Deployments) {
		t.Fatalf("count=%d 与条数 %d 不一致", res.Count, len(res.Deployments))
	}
	for _, view := range res.Deployments {
		if view.Name == name {
			return view
		}
	}
	t.Fatalf("list 未包含 %s: %s", name, raw)
	return DeploymentView{}
}

// contains 是 strings.Contains 的本地封装。
func contains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
