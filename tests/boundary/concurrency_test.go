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
// 断言四件事——① 每个请求自身结果正确、互不串扰；② 并发读状态文件永远读到
// 完整 JSON（原子替换，没有半截文件）；③ 执行面副作用次数与请求数一致；
// ④ 状态文件「读-改-写」不丢更新：N 个并发写者必须落 N 条记录，且逐条与
// 请求严格对应。
//
// ④ 是缺陷回归断言（a272f5b 的覆盖面缺口）：修复前 deployments.json /
// artifact-jobs.json 的 load→改→save 无互斥，8 个不同应用并发 deploy 全部成功
// 但最终只剩 1 条记录（last-writer-wins）。这四条断言在修复前必须 FAIL，
// 修复后必须 PASS——不许为了过绿把断言放宽成「记录数 ≤ N」。

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

	// 最终部署记录：必须恰好 N 条（每个并发写者一条，读-改-写不得丢更新）。
	records := deploymentRecords(t, state)
	if len(records) != n {
		t.Fatalf("并发部署后部署记录 %d 条，期望 %d 条：%v（读-改-写丢更新）",
			len(records), n, deploymentNames(t, state))
	}
	// 逐条严格对应：名字 → 部署 id / 容器 id / 宿主端口 / 容器引用必须来自
	// 各自那次调用，不能出现「A 的记录写了 B 的容器」这类串扰。
	for i, res := range results {
		rec, ok := records[names[i]]
		if !ok {
			t.Fatalf("部署记录缺少应用 %q（共 %d 条）", names[i], len(records))
		}
		if rec.ID != res.ID {
			t.Fatalf("%s 记录部署 id=%q，期望 %q（串扰）", names[i], rec.ID, res.ID)
		}
		if rec.Template != "nginx-static" {
			t.Fatalf("%s 记录模板=%q，期望 nginx-static", names[i], rec.Template)
		}
		if rec.ContainerID != res.ContainerID {
			t.Fatalf("%s 记录主容器 id=%q，期望 %q（串扰）", names[i], rec.ContainerID, res.ContainerID)
		}
		if len(rec.Containers) != 1 || rec.Containers[0].ID != res.ContainerID {
			t.Fatalf("%s 记录容器引用=%+v，期望单个容器 %q", names[i], rec.Containers, res.ContainerID)
		}
		wantPort := 19100 + i
		if len(rec.Ports) != 1 || rec.Ports[0].HostPort != wantPort {
			t.Fatalf("%s 记录宿主端口=%+v，期望 %d（串扰）", names[i], rec.Ports, wantPort)
		}
		if rec.CreatedAt != res.CreatedAt {
			t.Fatalf("%s 记录创建时间=%q，期望 %q", names[i], rec.CreatedAt, res.CreatedAt)
		}
	}
	// artifact-jobs.json 与 deployments.json 同处状态目录、共享同一把状态锁：
	// deploy 不写作业记录，所以这里断言该文件要么不存在、要么是 0 条的完整 JSON
	// ——即并发 deploy 不得把相邻状态文件写成半截或凭空塞记录。
	assertJobsFileConsistent(t, state, 0)
}

// deploymentRecords 读取 deployments.json，返回 name → 记录。
// 解析失败即判失败（并发原子替换保证读到的一定是完整文件）。
func deploymentRecords(t *testing.T, stateDir string) map[string]apop.Deployment {
	t.Helper()
	path := filepath.Join(stateDir, apop.DeploymentsFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		t.Fatalf("读取部署记录失败: %v", err)
	}
	var doc struct {
		Version     int               `json:"version"`
		Deployments []apop.Deployment `json:"deployments"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("部署记录不是完整 JSON（读到半截文件？）: %v（%s）", err, data)
	}
	out := make(map[string]apop.Deployment, len(doc.Deployments))
	for _, rec := range doc.Deployments {
		if _, dup := out[rec.Name]; dup {
			t.Fatalf("部署记录名字重复: %q", rec.Name)
		}
		out[rec.Name] = rec
	}
	return out
}

// assertJobsFileConsistent 断言 artifact-jobs.json 是完整 JSON 且恰好 want 条：
// 文件不存在等价于 0 条。
func assertJobsFileConsistent(t *testing.T, stateDir string, want int) []apop.ArtifactJob {
	t.Helper()
	data := readFile(t, filepath.Join(stateDir, "artifact-jobs.json"))
	if data == nil {
		if want != 0 {
			t.Fatalf("artifact-jobs.json 不存在，期望 %d 条作业记录", want)
		}
		return nil
	}
	var jobs []apop.ArtifactJob
	if err := json.Unmarshal(data, &jobs); err != nil {
		t.Fatalf("artifact-jobs.json 不是完整 JSON（读到半截文件？）: %v（%s）", err, data)
	}
	if len(jobs) != want {
		t.Fatalf("artifact-jobs.json 作业记录 %d 条，期望 %d 条（读-改-写丢更新）", len(jobs), want)
	}
	return jobs
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

	// 作业记录：8 个并发 upload 各写一条，必须恰好 8 条、名字与请求严格对应，
	// 且没有任何一条被后来的写者按旧快照覆盖掉。
	jobs := assertJobsFileConsistent(t, state, n)
	jobNames := make(map[string]bool, n)
	for _, job := range jobs {
		if job.Action != "upload" || job.Status != apop.JobSucceeded {
			t.Fatalf("作业记录状态异常: %+v", job)
		}
		if jobNames[job.Name] {
			t.Fatalf("作业记录应用名重复: %q（串扰）", job.Name)
		}
		jobNames[job.Name] = true
	}
	for _, name := range names {
		if !jobNames[name] {
			t.Fatalf("作业记录缺少应用 %q（共 %d 条）", name, len(jobs))
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
