package apop

import (
	"fmt"
	"strings"
	"testing"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestValidateName 覆盖允许与拒绝的部署名（含注入形态）。
func TestValidateName(t *testing.T) {
	accept := []string{
		"kore-app-test",
		"kore-app-a",
		"kore-app-123",
		"kore-app-my-site-2",
	}
	for _, name := range accept {
		if perr := ValidateName(name); perr != nil {
			t.Fatalf("name=%q 应通过，实际 %+v", name, perr)
		}
	}
	reject := []string{
		"",
		"blog",                                // 缺 kore-app- 前缀
		"Kore-app-x",                          // 大写开头
		"kore-app-Test",                       // 大写
		"kore-app_test",                       // 下划线
		"kore-app-test.x",                     // 点
		"kore-app-test;rm -rf",                // 分号
		"kore-app-$(id)",                      // 命令替换
		"kore-app-`id`",                       // 反引号
		"kore-app-test/x",                     // 斜杠
		"kore-app-test x",                     // 空格
		"kore-app-test\n",                     // 换行
		"kore-app-test\x00x",                  // NUL
		"-kore-app-x",                         // 连字符开头
		"kore-app-",                           // 连字符结尾
		"kore-app-a--b",                       // 连续连字符
		"kore-app-" + strings.Repeat("a", 30), // 超长
	}
	for _, name := range reject {
		perr := ValidateName(name)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("name=%q 应被拒绝，实际 %+v", name, perr)
		}
	}
}

// TestValidateEnvValue 覆盖 env 值的长度与元字符约束。
func TestValidateEnvValue(t *testing.T) {
	accept := []string{"", "wordpress", "appdb", "P@ssw0rd-2026", "a=b", "中文值", "a.b_c-d"}
	for _, value := range accept {
		if perr := ValidateEnvValue("K", value); perr != nil {
			t.Fatalf("value=%q 应通过，实际 %+v", value, perr)
		}
	}
	reject := []string{
		"a;b", "a|b", "a&b", "a$b", "a`b", `a"b`, "a'b", "a<b", "a>b", "a(b)",
		"a*b", "a?b", "a[b]", "a{b}", `a\b`, "a~b", "a!b", "a#b", "a\nb", "a\tb",
		"a\x00b", " a", "a ", strings.Repeat("x", MaxEnvValueLen+1),
	}
	for _, value := range reject {
		perr := ValidateEnvValue("K", value)
		if perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("value=%q 应被拒绝，实际 %+v", value, perr)
		}
	}
}

// TestValidateEnvWhitelistAndRequired 覆盖 env 白名单、必填与自动生成。
func TestValidateEnvWhitelistAndRequired(t *testing.T) {
	nginx := LookupTemplate("nginx-static")
	if _, perr := validateEnv(nginx, map[string]string{"FOO": "bar"}, seqSecret()); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("未声明的 env 键应被拒绝，实际 %+v", perr)
	}
	tooMany := make(map[string]string, MaxEnvEntries+1)
	for i := 0; i <= MaxEnvEntries; i++ {
		tooMany[fmt.Sprintf("K%d", i)] = "v"
	}
	if _, perr := validateEnv(nginx, tooMany, seqSecret()); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("env 条目超限应被拒绝，实际 %+v", perr)
	}

	postgres := LookupTemplate("postgres")
	if _, perr := validateEnv(postgres, nil, seqSecret()); perr == nil || perr.Code != pluginapi.E_INVALID {
		t.Fatalf("缺少 POSTGRES_PASSWORD 应被拒绝，实际 %+v", perr)
	} else if !strings.Contains(perr.Message, "POSTGRES_PASSWORD") {
		t.Fatalf("错误信息应点名缺失项，实际 %q", perr.Message)
	}
	plan, perr := validateEnv(postgres, map[string]string{"POSTGRES_PASSWORD": "s3cret-value"}, seqSecret())
	if perr != nil {
		t.Fatalf("提供必填项后应通过，实际 %+v", perr)
	}
	if got := plan.get("db", "POSTGRES_PASSWORD"); got != "s3cret-value" {
		t.Fatalf("必填项未注入，实际 %q", got)
	}
	if got := plan.get("db", "POSTGRES_USER"); got != "postgres" {
		t.Fatalf("可选默认值未生效，实际 %q", got)
	}

	// secret 留空 → 生成；必填 secret 缺失 → 报错（不允许静默生成）。
	wordpress := LookupTemplate("wordpress")
	plan, perr = validateEnv(wordpress, nil, seqSecret())
	if perr != nil {
		t.Fatalf("wordpress 无 env 时应可部署，实际 %+v", perr)
	}
	if plan.get("db", "MYSQL_PASSWORD") == "" || plan.get("db", "MYSQL_ROOT_PASSWORD") == "" {
		t.Fatal("secret 项留空时必须生成随机值")
	}
	if plan.get("db", "MYSQL_DATABASE") != "wordpress" {
		t.Fatal("MYSQL_DATABASE 默认值未生效")
	}
}

// seqSecret 返回递增的假口令生成器，便于断言两次生成不同。
func seqSecret() func() (string, error) {
	n := 0
	return func() (string, error) {
		n++
		return fmt.Sprintf("generated-secret-%d", n), nil
	}
}

// TestValidatePortRequests 覆盖端口覆盖的白名单、区间与重复。
func TestValidatePortRequests(t *testing.T) {
	nginx := LookupTemplate("nginx-static")
	bad := []struct {
		name string
		reqs []PortRequest
	}{
		{"未声明的容器端口", []PortRequest{{ContainerPort: 8080, HostPort: 18090}}},
		{"特权端口 80", []PortRequest{{ContainerPort: 80, HostPort: 80}}},
		{"特权端口 443", []PortRequest{{ContainerPort: 80, HostPort: 443}}},
		{"端口越界", []PortRequest{{ContainerPort: 80, HostPort: 70000}}},
		{"容器端口重复", []PortRequest{{ContainerPort: 80, HostPort: 18090}, {ContainerPort: 80, HostPort: 18091}}},
		{"宿主机端口重复", []PortRequest{{ContainerPort: 80, HostPort: 18090}, {ContainerPort: 80, HostPort: 18090}}},
	}
	for _, c := range bad {
		if perr := validatePortRequests(nginx, c.reqs); perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("%s 应被拒绝，实际 %+v", c.name, perr)
		}
	}
	if perr := validatePortRequests(nginx, []PortRequest{{ContainerPort: 80, HostPort: 18090}}); perr != nil {
		t.Fatalf("合法端口覆盖被拒: %+v", perr)
	}
	if perr := validatePortRequests(nginx, nil); perr != nil {
		t.Fatalf("空端口覆盖被拒: %+v", perr)
	}
}

// TestValidateVolumeRequests 覆盖挂载覆盖的白名单与子目录名。
func TestValidateVolumeRequests(t *testing.T) {
	nginx := LookupTemplate("nginx-static")
	bad := []struct {
		name string
		reqs []VolumeRequest
	}{
		{"未声明的卷", []VolumeRequest{{Name: "data", Subdir: "x"}}},
		{"子目录含大写", []VolumeRequest{{Name: "html", Subdir: "Site"}}},
		{"子目录含斜杠", []VolumeRequest{{Name: "html", Subdir: "../../etc"}}},
		{"子目录空", []VolumeRequest{{Name: "html", Subdir: ""}}},
		{"卷重复", []VolumeRequest{{Name: "html", Subdir: "a"}, {Name: "html", Subdir: "b"}}},
	}
	for _, c := range bad {
		if perr := validateVolumeRequests(nginx, c.reqs); perr == nil || perr.Code != pluginapi.E_INVALID {
			t.Fatalf("%s 应被拒绝，实际 %+v", c.name, perr)
		}
	}
	if perr := validateVolumeRequests(nginx, []VolumeRequest{{Name: "html", Subdir: "site-1"}}); perr != nil {
		t.Fatalf("合法挂载覆盖被拒: %+v", perr)
	}
}

// TestResolvePorts 覆盖自动选端口与显式冲突。
func TestResolvePorts(t *testing.T) {
	nginx := LookupTemplate("nginx-static")
	free := func(int) bool { return true }

	ports, perr := resolvePorts(nginx, nil, map[int]string{}, free)
	if perr != nil || len(ports) != 1 {
		t.Fatalf("默认端口解析失败: %+v / %v", ports, perr)
	}
	if ports[0].HostPort != 18090 || ports[0].AccessURL != "http://127.0.0.1:18090/" {
		t.Fatalf("默认端口=%+v", ports[0])
	}

	ports, perr = resolvePorts(nginx, nil, map[int]string{18090: "容器 other 已发布"}, free)
	if perr != nil || ports[0].HostPort != 18091 {
		t.Fatalf("默认端口被占时应顺延，实际 %+v / %v", ports, perr)
	}

	_, perr = resolvePorts(nginx, []PortRequest{{ContainerPort: 80, HostPort: 18090}},
		map[int]string{18090: "容器 other 已发布"}, free)
	if perr == nil || perr.Code != pluginapi.E_CONFLICT {
		t.Fatalf("显式端口被占应报 E_CONFLICT，实际 %+v", perr)
	}
	if perr != nil && !strings.Contains(perr.Message, "other") {
		t.Fatalf("冲突信息应指出占用者，实际 %q", perr.Message)
	}

	ports, perr = resolvePorts(nginx, []PortRequest{{ContainerPort: 80, HostPort: 20000}}, map[int]string{}, free)
	if perr != nil || ports[0].HostPort != 20000 {
		t.Fatalf("显式端口未生效: %+v / %v", ports, perr)
	}

	// 探测全部失败且无空闲端口可用 → 明确 E_CONFLICT，而不是静默选一个。
	_, perr = resolvePorts(nginx, nil, map[int]string{}, func(int) bool { return false })
	if perr == nil || perr.Code != pluginapi.E_CONFLICT {
		t.Fatalf("无可用端口应报 E_CONFLICT，实际 %+v", perr)
	}
}

// TestParseHostPorts 覆盖 docker ps 端口串解析。
func TestParseHostPorts(t *testing.T) {
	cases := []struct {
		raw  string
		want []int
	}{
		{"0.0.0.0:3306->3306/tcp, [::]:3306->3306/tcp", []int{3306, 3306}},
		{"127.0.0.1:18080->8080/tcp", []int{18080}},
		{"", nil},
		{"0.0.0.0:8000-8002->8000-8002/tcp", []int{8000, 8001, 8002}},
	}
	for _, c := range cases {
		got := ParseHostPorts(c.raw)
		if len(got) != len(c.want) {
			t.Fatalf("ParseHostPorts(%q)=%v，期望 %v", c.raw, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("ParseHostPorts(%q)=%v，期望 %v", c.raw, got, c.want)
			}
		}
	}
}
