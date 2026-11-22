//go:build linux

// Package e2e 对真实 docker 做端到端验证：templates → deploy（nginx 单容器 / wordpress 双容器）
// → curl 首页 → list → remove 护栏 → 清理。
//
// 默认跳过；显式开启：
//
//	KORE_APP_E2E=1 go test -timeout 900s -v ./tests/e2e
package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// testContainers 是 E2E 独占的容器名（清理只碰这些，不按前缀乱删）。
var testContainers = []string{"kore-app-test", "kore-app-wp", "kore-app-wp-db"}

// testNetworks / testVolumes 是 E2E 可能创建的资源。
var (
	testNetworks = []string{"kore-app-wp-net"}
	testVolumes  = []string{"kore-app-wp-html", "kore-app-wp-data"}
)

// buildPlugin 构建真实插件二进制到临时目录。
func buildPlugin(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "app-deploy-plugin")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "GOWORK=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("构建插件失败: %v\n%s", err, out)
	}
	return binary
}

// startPlugin 以协议环境变量启动插件进程；stderr 收集起来便于失败时定位。
func startPlugin(t *testing.T, binary, socket, root, state string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	stderr := &bytes.Buffer{}
	process := exec.Command(binary)
	process.Env = withEnv(os.Environ(),
		"KORE_PLUGIN_SOCK="+socket,
		"KORE_PLUGIN_ID=app-deploy",
		"KORE_PLUGIN_TOKEN=e2e-token",
		"KORE_APP_DEPLOY_ROOT="+root,
		"KORE_APP_DEPLOY_STATE="+state,
	)
	process.Stdout = io.Discard
	process.Stderr = stderr
	if err := process.Start(); err != nil {
		t.Fatalf("启动插件失败: %v", err)
	}
	t.Cleanup(func() {
		if process.ProcessState == nil {
			_ = process.Process.Kill()
			_, _ = process.Process.Wait()
		}
	})
	return process, stderr
}

// withEnv 先删掉同名环境变量再追加，避免宿主机残留值覆盖测试配置。
func withEnv(base []string, pairs ...string) []string {
	keys := make(map[string]bool, len(pairs))
	for _, pair := range pairs {
		key, _, _ := strings.Cut(pair, "=")
		keys[key] = true
	}
	env := make([]string, 0, len(base)+len(pairs))
	for _, item := range base {
		key, _, _ := strings.Cut(item, "=")
		if !keys[key] {
			env = append(env, item)
		}
	}
	return append(env, pairs...)
}

// listen 监听 UDS 并设置超时。
func listen(t *testing.T, socket string) *net.UnixListener {
	t.Helper()
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("监听 UDS 失败: %v", err)
	}
	unixListener := listener.(*net.UnixListener)
	_ = unixListener.SetDeadline(time.Now().Add(600 * time.Second))
	t.Cleanup(func() { _ = unixListener.Close() })
	return unixListener
}

// accept 接受插件连接并设置帧超时。
func accept(t *testing.T, listener *net.UnixListener, stderr *bytes.Buffer) net.Conn {
	t.Helper()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatalf("接受插件连接失败: %v (stderr=%s)", err, stderr.String())
	}
	_ = conn.SetDeadline(time.Now().Add(600 * time.Second))
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// handshake 读 kore.hello 并回 ok 响应。
func handshake(t *testing.T, conn net.Conn) {
	t.Helper()
	request := readRequest(t, conn)
	if request.Method != "kore.hello" {
		t.Fatalf("首帧应为 kore.hello，实际 %s", request.Method)
	}
	response := &pluginapi.Response{
		Type: pluginapi.TypeResponse, ID: request.ID, ReqID: request.ReqID,
		Result: json.RawMessage(`{"ok":true,"protocol":"kore.plugin.v1"}`),
	}
	if err := pluginapi.WriteJSON(conn, response); err != nil {
		t.Fatalf("回写 hello 失败: %v", err)
	}
}

// request 发送一次方法调用并读回响应。
func request(t *testing.T, conn net.Conn, id, method string, params any) *pluginapi.Response {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("序列化 %s 参数失败: %v", method, err)
	}
	req := &pluginapi.Request{
		Type: pluginapi.TypeRequest, ID: id, ReqID: "r-" + id, Method: method, Params: raw,
	}
	if err := pluginapi.WriteJSON(conn, req); err != nil {
		t.Fatalf("发送 %s 失败: %v", method, err)
	}
	return readResponse(t, conn)
}

// mustCall 要求调用成功并返回 result 原文。
func mustCall(t *testing.T, conn net.Conn, id, method string, params any) string {
	t.Helper()
	response := request(t, conn, id, method, params)
	if response.Error != nil {
		t.Fatalf("%s 失败: %+v", method, response.Error)
	}
	return string(response.Result)
}

// expectError 要求调用失败且错误码匹配。
func expectError(t *testing.T, conn net.Conn, id, method string, params any, wantCode string) {
	t.Helper()
	response := request(t, conn, id, method, params)
	if response.Error == nil {
		t.Fatalf("%s 期望 %s，实际成功: %s", method, wantCode, response.Result)
	}
	if response.Error.Code != wantCode {
		t.Fatalf("%s 错误码=%q，期望 %q（message=%q）", method, response.Error.Code, wantCode, response.Error.Message)
	}
	if response.Result != nil {
		t.Fatalf("%s 出错时不应带结果", method)
	}
}

// readRequest 读下一帧并断言是 request。
func readRequest(t *testing.T, conn net.Conn) *pluginapi.Request {
	t.Helper()
	env, err := pluginapi.DecodeFrame(readPayload(t, conn))
	if err != nil {
		t.Fatalf("解码请求失败: %v", err)
	}
	request, ok := env.(*pluginapi.Request)
	if !ok {
		t.Fatalf("期望 request，实际 %T", env)
	}
	return request
}

// readResponse 读下一帧并断言是 response。
func readResponse(t *testing.T, conn net.Conn) *pluginapi.Response {
	t.Helper()
	env, err := pluginapi.DecodeFrame(readPayload(t, conn))
	if err != nil {
		t.Fatalf("解码响应失败: %v", err)
	}
	response, ok := env.(*pluginapi.Response)
	if !ok {
		t.Fatalf("期望 response，实际 %T", env)
	}
	return response
}

// readPayload 读一整帧。
func readPayload(t *testing.T, conn net.Conn) []byte {
	t.Helper()
	payload, err := pluginapi.ReadFrame(conn, pluginapi.MaxFrameSize)
	if err != nil {
		t.Fatalf("读帧失败: %v", err)
	}
	return payload
}

// shutdown 尽力优雅关闭插件会话。
func shutdown(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	req := &pluginapi.Request{
		Type: pluginapi.TypeRequest, ID: "e2e-shutdown", ReqID: "r-shutdown", Method: "kore.shutdown",
	}
	if err := pluginapi.WriteJSON(conn, req); err != nil {
		return
	}
	_, _ = pluginapi.ReadFrame(conn, pluginapi.MaxFrameSize)
}

// runDocker 直接调用 docker CLI（测试侧交叉核对，不经过插件）。
func runDocker(args ...string) ([]byte, error) {
	cmd := exec.Command("docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker %s 失败: %v（stderr=%s）", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// dockerOutput 直接调用 docker 并要求成功。
func dockerOutput(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runDocker(args...)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return string(out)
}

// dockerState 读取容器状态；容器不存在时返回空串。
func dockerState(t *testing.T, name string) string {
	t.Helper()
	out, err := runDocker("inspect", "--format", "{{.State.Status}}", name)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// waitDockerState 轮询容器状态直到命中或超时。
func waitDockerState(t *testing.T, name, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if dockerState(t, name) == want {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("容器 %s 未在 %s 内进入 %s（当前 %q）", name, timeout, want, dockerState(t, name))
}

// waitDockerGone 轮询直到容器不存在。
func waitDockerGone(t *testing.T, name string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if dockerState(t, name) == "" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("容器 %s 未在 %s 内被删除（当前 %q）", name, timeout, dockerState(t, name))
}

// httpGet 请求一个 URL 并返回状态码与响应体。
func httpGet(url string) (int, string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(url)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, "", err
	}
	return response.StatusCode, string(body), nil
}

// waitHTTP200 轮询直到返回 200 且响应体命中关键字。
func waitHTTP200(t *testing.T, url, wantBody string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	lastStatus, lastErr := 0, error(nil)
	lastBody := ""
	for time.Now().Before(deadline) {
		status, body, err := httpGet(url)
		lastStatus, lastErr, lastBody = status, err, body
		if err == nil && status == http.StatusOK && strings.Contains(body, wantBody) {
			return body
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("等待 %s 超时：lastStatus=%d lastErr=%v body=%q", url, lastStatus, lastErr, truncate(lastBody, 400))
	return ""
}

// truncate 截断日志文本。
func truncate(raw string, limit int) string {
	runes := []rune(raw)
	if len(runes) <= limit {
		return raw
	}
	return string(runes[:limit]) + "…"
}

// cleanTestResources 只清理 E2E 自己用到的容器/网络/卷（不按前缀横扫，避免误删用户资源）。
func cleanTestResources(t *testing.T) {
	t.Helper()
	for _, name := range testContainers {
		_, _ = runDocker("rm", "-f", name)
	}
	for _, network := range testNetworks {
		_, _ = runDocker("network", "rm", network)
	}
	for _, volume := range testVolumes {
		_, _ = runDocker("volume", "rm", volume)
	}
}

// dockerNames 返回 `docker ps -a` 的全部容器名。
func dockerNames(t *testing.T) []string {
	t.Helper()
	raw := dockerOutput(t, "ps", "-a", "--format", "{{.Names}}")
	names := make([]string, 0)
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			names = append(names, line)
		}
	}
	return names
}

// hasDockerName 判断容器名是否仍存在。
func hasDockerName(t *testing.T, want string) bool {
	t.Helper()
	for _, name := range dockerNames(t) {
		if name == want {
			return true
		}
	}
	return false
}
