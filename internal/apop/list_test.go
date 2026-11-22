package apop

import (
	"encoding/json"
	"os"
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
