package apop

import (
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// TestBuildRunArgs 断言 docker run 的固定 argv：内存限制、回环发布、env-file、无 shell。
func TestBuildRunArgs(t *testing.T) {
	spec := ContainerSpec{
		Name:     "kore-app-x",
		Image:    "nginx:alpine",
		MemoryMB: 128,
		Ports:    []PortBinding{{ContainerPort: 80, HostPort: 18090, Protocol: "tcp"}},
		Volumes:  []RunVolume{{Source: "/state/volumes/x/html", Target: "/usr/share/nginx/html"}},
		EnvFile:  "/state/.env-1",
		Labels: map[string]string{
			LabelTemplate: "nginx-static",
			LabelManaged:  "true",
		},
	}
	want := []string{
		"run", "-d", "--name", "kore-app-x",
		"--label", "kore.app-deploy.managed=true",
		"--label", "kore.app-deploy.template=nginx-static",
		"--memory", "128m", "--memory-swap", "128m",
		"--publish", "127.0.0.1:18090:80/tcp",
		"--volume", "/state/volumes/x/html:/usr/share/nginx/html",
		"--env-file", "/state/.env-1",
		"nginx:alpine",
	}
	got := BuildRunArgs(spec)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv 不符\n实际: %v\n期望: %v", got, want)
	}
}

// TestBuildRunArgsNoShell 证明 argv 里没有 shell 解释器、镜像永远在末尾附近，且不含 env 值。
func TestBuildRunArgsNoShell(t *testing.T) {
	spec := ContainerSpec{
		Name:     "kore-app-wp",
		Image:    "wordpress:6",
		Cmd:      nil,
		MemoryMB: 512,
		EnvFile:  "/state/.env-2",
		Network:  "kore-app-wp-net",
		Labels:   map[string]string{LabelManaged: "true"},
	}
	args := BuildRunArgs(spec)
	for _, arg := range args {
		if arg == "sh" || arg == "bash" || arg == "-c" || arg == "/bin/sh" {
			t.Fatalf("argv 含 shell 解释器: %v", args)
		}
	}
	last := args[len(args)-1]
	if last != "wordpress:6" {
		t.Fatalf("镜像应为最后一个参数，实际 %q（%v）", last, args)
	}
	if !containsPair(args, "--network", "kore-app-wp-net") {
		t.Fatalf("argv 缺少 --network: %v", args)
	}
}

// TestBuildRunArgsRedisCommand redis 模板带自定义启动参数。
func TestBuildRunArgsRedisCommand(t *testing.T) {
	spec := ContainerSpec{
		Name: "kore-app-r", Image: "redis:7-alpine", MemoryMB: 256,
		Cmd: []string{"redis-server", "--appendonly", "yes"},
	}
	args := BuildRunArgs(spec)
	tail := args[len(args)-4:]
	want := []string{"redis:7-alpine", "redis-server", "--appendonly", "yes"}
	if !reflect.DeepEqual(tail, want) {
		t.Fatalf("redis 启动参数不符: %v", tail)
	}
}

// containsPair 判断 argv 里是否出现 key 紧跟 value。
func containsPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}

// TestParsePSOutput 覆盖 docker ps JSON 行解析。
func TestParsePSOutput(t *testing.T) {
	raw := []byte(`{"ID":"2db45db6e20c","Names":"kore-app-x","Image":"nginx:alpine","State":"running","Status":"Up 2 minutes","Ports":"127.0.0.1:18090->80/tcp"}
{"ID":"abc123456789","Names":"/foreign","Image":"other:1","State":"exited","Status":"Exited (0)","Ports":""}
`)
	states, err := parsePSOutput(raw)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("条目数=%d，期望 2", len(states))
	}
	if states[0].Name != "kore-app-x" || states[0].State != "running" || states[0].Image != "nginx:alpine" {
		t.Fatalf("首条解析不符: %+v", states[0])
	}
	if states[1].Name != "foreign" {
		t.Fatalf("前导斜杠未去掉: %q", states[1].Name)
	}
}

// TestProbePort 用真实监听验证端口探测。
func TestProbePort(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if probePort(port) {
		t.Fatalf("端口 %d 正在被监听，探测应为 false", port)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("关闭监听失败: %v", err)
	}
	if !probePort(port) {
		t.Fatalf("端口 %d 已释放，探测应为 true", port)
	}
}

// TestDockerClientMissingBinary 未安装 docker 时映射为 E_UNSUPPORTED，而不是 E_INTERNAL。
func TestDockerClientMissingBinary(t *testing.T) {
	client := NewDockerClient("docker-not-installed-for-test-"+strconv.Itoa(1), time.Second)
	_, err := client.ListContainers()
	if err == nil {
		t.Fatal("不存在的 docker 二进制应当报错")
	}
	if perr := classify(err); perr.Code != pluginapi.E_UNSUPPORTED {
		t.Fatalf("错误码=%q，期望 E_UNSUPPORTED（message=%q）", perr.Code, perr.Message)
	}
}

// TestClassifyFaults 覆盖四类故障到协议错误码的映射。
func TestClassifyFaults(t *testing.T) {
	cases := []struct {
		kind FaultKind
		code string
		hint string
	}{
		{FaultKill, pluginapi.E_UPSTREAM, "信号 9"},
		{FaultTimeout, pluginapi.E_UPSTREAM, "超时"},
		{FaultDisk, pluginapi.E_INTERNAL, "磁盘满"},
		{FaultCgroup, pluginapi.E_UNSUPPORTED, "cgroup"},
	}
	for _, c := range cases {
		perr := classify(NewFaultError(c.kind, "注入"))
		if perr.Code != c.code || !strings.Contains(perr.Message, c.hint) {
			t.Fatalf("%s 映射=%q/%q，期望 %q 含 %q", c.kind, perr.Code, perr.Message, c.code, c.hint)
		}
	}
	if perr := classify(&OpError{Code: pluginapi.E_CONFLICT, Message: "冲突"}); perr.Code != pluginapi.E_CONFLICT {
		t.Fatalf("OpError 错误码未透传: %+v", perr)
	}
	if perr := classify(errPlain{}); perr.Code != pluginapi.E_INTERNAL {
		t.Fatalf("普通错误应归 E_INTERNAL: %+v", perr)
	}
}

// errPlain 是一个普通错误。
type errPlain struct{}

func (errPlain) Error() string { return "普通错误" }
