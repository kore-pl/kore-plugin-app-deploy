package apop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// getenvFrom 构造一个从 map 读环境变量的 getenv，避免测试间污染真实环境。
func getenvFrom(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// TestResolveDataDirsDefault 覆盖三条解析优先级：显式 > plugin-data > 旧默认（带 WARN）。
func TestResolveDataDirsDefault(t *testing.T) {
	cases := []struct {
		name     string
		env      map[string]string
		wantRoot string
		wantStat string
		wantWarn bool
	}{
		{"旧默认（无变量）", nil, DefaultRoot, filepath.Join(DefaultRoot, "state"), true},
		{"plugin-data 默认", map[string]string{EnvPluginData: "/d"}, "/d/apps", "/d/snapshots", false},
		{"显式根优先于 plugin-data", map[string]string{EnvRoot: "/x", EnvPluginData: "/d"}, "/x", "/x/state", false},
		{"显式状态目录再覆盖", map[string]string{EnvRoot: "/x", EnvStateDir: "/s"}, "/x", "/s", false},
		{"显式根 + plugin-data + 显式状态", map[string]string{EnvRoot: "/x", EnvStateDir: "/s", EnvPluginData: "/d"}, "/x", "/s", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, state, warn := ResolveDataDirs(getenvFrom(tc.env))
			if root != tc.wantRoot || state != tc.wantStat {
				t.Fatalf("root/state=%q/%q，期望 %q/%q", root, state, tc.wantRoot, tc.wantStat)
			}
			if (warn != "") != tc.wantWarn {
				t.Fatalf("warn=%q，期望非空=%v", warn, tc.wantWarn)
			}
			if tc.wantWarn && !strings.Contains(warn, EnvPluginData) {
				t.Fatalf("回退 WARN 必须点名缺失变量：%q", warn)
			}
		})
	}
}

// TestDataRootsAndOutside 校验可写根集合与「安装目录外」判定。
func TestDataRootsAndOutside(t *testing.T) {
	t.Setenv(EnvInstallDir, "/opt/kore")
	t.Setenv(EnvPluginData, "/opt/kore/data/plugins/app-deploy")
	roots := DataRoots("/opt/kore/data/apps", "/opt/kore/data/snapshots")
	for _, inside := range []string{"/opt/kore/data/apps/a", "/opt/kore/data/snapshots/b", "/opt/kore/x", "/opt/kore"} {
		if OutsideAllRoots(inside, roots) {
			t.Errorf("%s 应判定为安装目录内", inside)
		}
	}
	for _, outside := range []string{"/var/lib/kore/app-deploy/state/volumes/a", "/opt/kore-evil/x", "/etc"} {
		if !OutsideAllRoots(outside, roots) {
			t.Errorf("%s 应判定为安装目录外", outside)
		}
	}
	if OutsideAllRoots("", roots) {
		t.Error("空路径应视为未知（false），不得误报")
	}
}

// TestListOutsideInstallKeepsExisting 既有应用保持原样：list 标记 outside_install 且不搬迁记录。
func TestListOutsideInstallKeepsExisting(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "snapshots")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := stateFile{Version: StateVersion, Deployments: []Deployment{{
		ID: "dep-1", Template: "nginx-static", Name: "old-app", Status: StatusRunning,
		Volumes: []VolumeRef{{Name: "/var/lib/kore/app-deploy/state/volumes/old-app/html", Kind: VolumeBind}},
	}}}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, DeploymentsFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvInstallDir, dir)
	t.Setenv(EnvPluginData, dir)
	runtime := NewRuntime(Options{Root: filepath.Join(dir, "apps"), StateDir: stateDir, DockerBin: "docker"}, &fakeExecutor{})
	_, perr := runtime.callList(json.RawMessage(`{}`))
	if perr != nil {
		t.Fatalf("list 失败: %+v", perr)
	}
	// 记录必须原样保留（不迁移、不重写）
	after, err := os.ReadFile(filepath.Join(stateDir, DeploymentsFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("list 不得改写既有部署记录（已部署应用不迁移）")
	}
	views := []DeploymentView{buildDeploymentView(legacy.Deployments[0], nil, runtime.dataRoots())}
	if !views[0].OutsideInstall || views[0].Hint == "" {
		t.Fatalf("历史应用应标记 outside_install=true + hint，实际 %+v", views[0])
	}
}

// TestBuildDeploymentViewInsideInstall 当前根内的应用不得误标 outside_install。
func TestBuildDeploymentViewInsideInstall(t *testing.T) {
	rec := Deployment{Name: "new-app", Volumes: []VolumeRef{{Name: "/data/apps/volumes/new-app/html", Kind: VolumeBind}}}
	view := buildDeploymentView(rec, nil, []string{"/data/apps", "/data/snapshots"})
	if view.OutsideInstall || view.Hint != "" {
		t.Fatalf("安装目录内应用不应标记 outside_install：%+v", view)
	}
}

// TestTemplatesReadOnly 随包模板只读：templates 只读常量，不落盘任何文件。
func TestTemplatesReadOnly(t *testing.T) {
	dir := t.TempDir()
	runtime := NewRuntime(Options{Root: filepath.Join(dir, "apps"), StateDir: filepath.Join(dir, "snapshots"), DockerBin: "docker"}, &fakeExecutor{})
	if _, perr := runtime.callTemplates(json.RawMessage(`{}`)); perr != nil {
		t.Fatalf("templates 失败: %+v", perr)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("templates 不得写盘（含模板/状态目录），实际产生 %d 项", len(entries))
	}
}
