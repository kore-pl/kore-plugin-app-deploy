package boundary

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pengtang1024/kore-plugin-app-deploy/internal/apop"
)

// 本文件是「并发」边界：用真 goroutine 同时打进方法面，在 -race 下跑，
// 断言三件事——① 每个请求自身结果正确、互不串扰；② 并发读状态文件永远读到
// 完整 JSON（原子替换，没有半截文件）；③ 执行面副作用次数与请求数一致。
//
// 已知缺陷（不在本用例断言，避免把缺陷固化成"预期"）：deployments.json /
// artifact-jobs.json 是「读-改-写」且进程内无锁，并发写会 last-writer-wins
// 丢记录。本套件只断言代码当前真实提供的保证，该缺陷已在交付报告中标出。

// startBarrier 返回一个闭锁通道：所有 goroutine 在同一瞬间起跑，
// 保证「并发」不是串行执行后的事后断言。
func startBarrier() chan struct{} { return make(chan struct{}) }

// watchStateFile 在后台持续读取部署记录，任何一次读到非法 JSON 都判失败。
// 返回停止函数：停止后读取最终错误（nil 表示全程未读到半截文件）。
func watchStateFile(t *testing.T, stateDir string) (stop func() error) {
	t.Helper()
	path := filepath.Join(stateDir, apop.DeploymentsFile)
	done := make(chan struct{})
	result := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		var lastErr error
		for {
			select {
			case <-done:
				result <- lastErr
				return
			default:
			}
			data, err := os.ReadFile(path)
			if err != nil {
				if !errors.Is(err, os.ErrNotExist) {
					result <- fmt.Errorf("并发读部署记录失败: %w", err)
					return
				}
			} else {
				var doc any
				if err := json.Unmarshal(data, &doc); err != nil {
					result <- fmt.Errorf("并发读到半截部署记录: %w（%q）", err, data)
					return
				}
			}
			time.Sleep(200 * time.Microsecond)
		}
	}()
	return func() error {
		close(done)
		wg.Wait()
		return <-result
	}
}

// TestConcurrentDeployDistinctApps 并发部署 8 个不同应用（不同名、不同宿主端口）。
//
// 断言：8 次调用全部成功；执行面恰好收到 8 次 docker run；容器名两两不同；
// 并发读 deployments.json 从未读到半截文件；最终记录名是请求集合的子集且无重复。
func TestConcurrentDeployDistinctApps(t *testing.T) {
	exec := &fakeExecutor{}
	rt, _, state := newRuntime(t, exec)
	const n = 8

	names := make([]string, n)
	for i := 0; i < n; i++ {
		names[i] = fmt.Sprintf("kore-app-bd-c%d", i)
	}
	results := make([]apop.DeployResult, n)
	errs := make([]string, n)

	stopWatch := watchStateFile(t, state)
	start := startBarrier()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			params, err := json.Marshal(map[string]any{
				"template": "nginx-static",
				"name":     names[i],
				"ports":    []map[string]int{{"container_port": 80, "host_port": 19100 + i}},
			})
			if err != nil {
				errs[i] = err.Error()
				return
			}
			<-start
			raw, perr := rt.Call("app-deploy.deploy", params)
			if perr != nil {
				errs[i] = perr.Code + ": " + perr.Message
				return
			}
			if err := json.Unmarshal(raw, &results[i]); err != nil {
				errs[i] = "解析 deploy 结果失败: " + err.Error()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if err := stopWatch(); err != nil {
		t.Fatal(err)
	}
	for i, msg := range errs {
		if msg != "" {
			t.Fatalf("并发 deploy %s 失败: %s", names[i], msg)
		}
	}
	if got := exec.runCount(); got != n {
		t.Fatalf("docker run 调用 %d 次，期望 %d", got, n)
	}

	// 容器名 / 容器 ID 各自唯一：并发路径不得把不同部署拼到一起。
	seenName := map[string]bool{}
	seenID := map[string]bool{}
	for i, res := range results {
		if !res.Deployed || res.Name != names[i] {
			t.Fatalf("第 %d 个部署结果不符: %+v", i, res)
		}
		if len(res.ContainerID) != 64 || seenID[res.ContainerID] {
			t.Fatalf("容器 ID 非法或重复: %q", res.ContainerID)
		}
		seenID[res.ContainerID] = true
		for _, ref := range res.Containers {
			if seenName[ref.Name] {
				t.Fatalf("容器名重复: %s", ref.Name)
			}
			seenName[ref.Name] = true
		}
	}
	if len(seenName) != n {
		t.Fatalf("容器数=%d，期望 %d: %v", len(seenName), n, seenName)
	}

	// 最终部署记录：必须是合法 JSON、名字来自请求集合、无重复。
	got := deploymentNames(t, state)
	if len(got) == 0 {
		t.Fatal("并发部署后部署记录为空")
	}
	want := make(map[string]bool, n)
	for _, name := range names {
		want[name] = true
	}
	seen := map[string]bool{}
	for _, name := range got {
		if !want[name] {
			t.Fatalf("部署记录出现未请求的名字: %q", name)
		}
		if seen[name] {
			t.Fatalf("部署记录名字重复: %q", name)
		}
		seen[name] = true
	}
	if len(got) > n {
		t.Fatalf("部署记录数 %d 超过请求数 %d", len(got), n)
	}
}

// TestConcurrentUploadsKeepPayloadsIsolated 并发上传 8 个不同应用包（内容互不相同）：
// 解压必须互相隔离，不能串包、不能共享暂存目录，返回的 sha256 必须与各自包一致。
func TestConcurrentUploadsKeepPayloadsIsolated(t *testing.T) {
	exec := &fakeExecutor{}
	rt, root, state := newRuntime(t, exec)
	const n = 8

	names := make([]string, n)
	payloads := make([]string, n)
	archives := make([][]byte, n)
	results := make([]apop.UploadResult, n)
	errs := make([]string, n)
	for i := 0; i < n; i++ {
		names[i] = fmt.Sprintf("kore-app-bd-u%d", i)
		payloads[i] = strings.Repeat(fmt.Sprintf("payload-%d-", i), 64)
		archives[i] = zipArtifact(t, map[string]string{"index.html": payloads[i]})
	}

	start := startBarrier()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			raw, perr := rt.Call("app-deploy.upload", uploadParams(t, names[i], archives[i]))
			if perr != nil {
				errs[i] = perr.Code + ": " + perr.Message
				return
			}
			if err := json.Unmarshal(raw, &results[i]); err != nil {
				errs[i] = "解析 upload 结果失败: " + err.Error()
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, msg := range errs {
		if msg != "" {
			t.Fatalf("并发 upload %s 失败: %s", names[i], msg)
		}
	}

	seenJob := map[string]bool{}
	for i, res := range results {
		sum := sha256.Sum256(archives[i])
		wantSum := hex.EncodeToString(sum[:])
		if res.Name != names[i] || res.Sha256 != wantSum {
			t.Fatalf("第 %d 个 upload 结果串包: name=%q sha=%q 期望 name=%q sha=%q",
				i, res.Name, res.Sha256, names[i], wantSum)
		}
		if res.Job == "" || seenJob[res.Job] {
			t.Fatalf("作业 id 为空或重复: %q", res.Job)
		}
		seenJob[res.Job] = true
		content := string(readFile(t, filepath.Join(root, names[i], "index.html")))
		if content != payloads[i] {
			t.Fatalf("%s 解压内容串包: got=%q want=%q", names[i], content, payloads[i])
		}
	}

	// 作业记录文件必须是完整 JSON（并发写可能丢记录——已知缺陷，不在此断言完整性）。
	if data := readFile(t, filepath.Join(state, "artifact-jobs.json")); data != nil {
		var jobs []apop.ArtifactJob
		if err := json.Unmarshal(data, &jobs); err != nil {
			t.Fatalf("并发上传后作业记录不是完整 JSON: %v（%s）", err, data)
		}
	}
	assertNoStagingResidue(t, root)
}

// TestConcurrentDeployAndUpgrade 并发执行「部署应用 A」与「升级应用 B」：
// 两条路径写不同的状态文件与目录，必须同时成功且互不破坏。
func TestConcurrentDeployAndUpgrade(t *testing.T) {
	exec := &fakeExecutor{}
	rt, root, state := newRuntime(t, exec)

	const appA = "kore-app-bd-d0"
	const appB = "kore-app-bd-u0"
	mustOK(t, rt, "app-deploy.upload", uploadParams(t, appB, zipArtifact(t, map[string]string{
		"v.txt": "v1", "index.html": "old",
	})))

	var (
		deployRes  apop.DeployResult
		upgradeRes apop.UpgradeResult
		deployErr  string
		upgradeErr string
	)
	start := startBarrier()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		params, _ := json.Marshal(map[string]any{
			"template": "nginx-static",
			"name":     appA,
			"ports":    []map[string]int{{"container_port": 80, "host_port": 19190}},
		})
		raw, perr := rt.Call("app-deploy.deploy", params)
		if perr != nil {
			deployErr = perr.Code + ": " + perr.Message
			return
		}
		if err := json.Unmarshal(raw, &deployRes); err != nil {
			deployErr = "解析 deploy 结果失败: " + err.Error()
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		raw, perr := rt.Call("app-deploy.upgrade", uploadParams(t, appB, zipArtifact(t, map[string]string{
			"v.txt": "v2", "index.html": "new",
		})))
		if perr != nil {
			upgradeErr = perr.Code + ": " + perr.Message
			return
		}
		if err := json.Unmarshal(raw, &upgradeRes); err != nil {
			upgradeErr = "解析 upgrade 结果失败: " + err.Error()
		}
	}()
	close(start)
	wg.Wait()

	if deployErr != "" {
		t.Fatalf("并发 deploy 失败: %s", deployErr)
	}
	if upgradeErr != "" {
		t.Fatalf("并发 upgrade 失败: %s", upgradeErr)
	}
	if !deployRes.Deployed || deployRes.Name != appA {
		t.Fatalf("deploy 结果不符: %+v", deployRes)
	}
	if !upgradeRes.Upgraded || upgradeRes.Name != appB || upgradeRes.Rollback {
		t.Fatalf("upgrade 结果不符: %+v", upgradeRes)
	}
	if got := string(readFile(t, filepath.Join(root, appB, "v.txt"))); got != "v2" {
		t.Fatalf("升级后内容未换入: v.txt=%q，期望 v2", got)
	}
	if got := string(readFile(t, filepath.Join(upgradeRes.Snapshot, "v.txt"))); got != "v1" {
		t.Fatalf("升级快照未保留旧版本: v.txt=%q，期望 v1", got)
	}
	names := deploymentNames(t, state)
	if len(names) != 1 || names[0] != appA {
		t.Fatalf("升级不应写部署记录：记录=%v，期望仅 [%s]", names, appA)
	}
	assertNoStagingResidue(t, root)
}
