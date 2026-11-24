package apop

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pengtang1024/kore/pkg/pluginapi"
)

// 应用包（artifact）解压与部署的边界常量。
const (
	// ArtifactFormatZip / ArtifactFormatTarGz 是支持的两类包格式。
	ArtifactFormatZip   = "zip"
	ArtifactFormatTarGz = "tar.gz"
	// MaxArtifactBytes 是 base64 解码后单个包的上限（32 MiB）。
	MaxArtifactBytes = 32 << 20
	// MaxArtifactEntries / MaxArtifactTotalBytes / MaxArtifactFileBytes 是解压炸弹护栏。
	MaxArtifactEntries    = 10000
	MaxArtifactTotalBytes = 256 << 20
	MaxArtifactFileBytes  = 64 << 20
	// stagingSubdir / snapshotsSubdir / jobsFile 是状态目录布局。
	stagingSubdir   = "staging"
	snapshotsSubdir = "snapshots"
	jobsFile        = "artifact-jobs.json"
	// maxJobRecords 是保留的作业条数上限。
	maxJobRecords = 50
	// ArtifactTemplate 是 artifact 部署记录使用的模板标识（无内置模板）。
	ArtifactTemplate = "artifact"
)

// 作业状态取值（同步阶段推进，不起 ticker，见 G-19）。
const (
	JobPending   = "pending"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
)

// artifactNamePattern 是应用包目录名（不是容器名）：小写字母/数字/连字符。
var artifactNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// validateArtifactName 校验应用包名；目录名同时充当路径段，故只允许安全字符。
func validateArtifactName(name string) *pluginapi.Error {
	if !artifactNamePattern.MatchString(name) || strings.Contains(name, "..") {
		return invalid("name 非法 " + strconv.Quote(name) + "：只允许小写字母/数字/连字符（1-40 字符），不能含路径分隔符")
	}
	return nil
}

// DatabaseDecl 是「需要建库」的声明：只登记，不连 MySQL（db-mysql 的写域）。
type DatabaseDecl struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
}

// SiteHint 是站点联动建议：不写 nginx（site 的写域），只给 UI 引导。
type SiteHint struct {
	Domain string `json:"domain,omitempty"`
	Port   int    `json:"port,omitempty"`
	Root   string `json:"root"`
	Hint   string `json:"hint"`
}

// JobStep 是一条阶段记录。
type JobStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	At     string `json:"at"`
}

// ArtifactJob 是一次包处理的阶段状态（可轮询）。
type ArtifactJob struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Action      string    `json:"action"`
	Status      string    `json:"status"`
	Steps       []JobStep `json:"steps"`
	Error       string    `json:"error,omitempty"`
	ArtifactDir string    `json:"artifact_dir,omitempty"`
	Snapshot    string    `json:"snapshot,omitempty"`
	StartedAt   string    `json:"started_at"`
	FinishedAt  string    `json:"finished_at,omitempty"`
}

// UploadResult 是 app-deploy.upload 的结果。
type UploadResult struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Files       int    `json:"files"`
	Bytes       int64  `json:"bytes"`
	Sha256      string `json:"sha256"`
	Job         string `json:"job"`
	OutsideRoot bool   `json:"outside_install"`
}

// ArtifactDeployResult 是 app-deploy.deployFromArtifact 的结果。
type ArtifactDeployResult struct {
	Deployed    bool           `json:"deployed"`
	Name        string         `json:"name"`
	Path        string         `json:"path"`
	Template    string         `json:"template"`
	SiteHint    SiteHint       `json:"site_hint"`
	Databases   []DatabaseDecl `json:"databases,omitempty"`
	Hints       []string       `json:"hints,omitempty"`
	Job         ArtifactJob    `json:"job"`
	AccessURL   string         `json:"access_url,omitempty"`
	ContainerID string         `json:"container_id,omitempty"`
}

// UpgradeResult 是 app-deploy.upgrade 的结果。
type UpgradeResult struct {
	Name     string      `json:"name"`
	Upgraded bool        `json:"upgraded"`
	Snapshot string      `json:"snapshot"`
	Files    int         `json:"files"`
	Bytes    int64       `json:"bytes"`
	Rollback bool        `json:"rolled_back"`
	Job      ArtifactJob `json:"job"`
}

// JobsResult 是 app-deploy.jobs 的结果（可按 id 过滤）。
type JobsResult struct {
	Jobs  []ArtifactJob `json:"jobs"`
	Count int           `json:"count"`
}

// artifactParams 是 upload 的入参。
type artifactParams struct {
	Name          string         `json:"name"`
	Format        string         `json:"format"`
	ContentBase64 string         `json:"content_base64"`
	Sha256        string         `json:"sha256,omitempty"`
	Databases     []DatabaseDecl `json:"databases,omitempty"`
	SiteDomain    string         `json:"site_domain,omitempty"`
	SitePort      int            `json:"site_port,omitempty"`
}

// jobsParams 是 jobs 的入参。
type jobsParams struct {
	ID string `json:"id,omitempty"`
}

// renameFn 是目录换入的注入点（测试可替换以验证失败回滚）。
var renameFn = os.Rename

// nowStamp 返回 RFC3339 时间串。
func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

// loadJobs / saveJobs 持久化作业状态（原子写，保留最近 maxJobRecords 条）。
func (r *Runtime) loadJobs() ([]ArtifactJob, *pluginapi.Error) {
	data, err := os.ReadFile(filepath.Join(r.opts.StateDir, jobsFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []ArtifactJob{}, nil
		}
		return nil, internalErr("读取作业记录失败: " + err.Error())
	}
	var jobs []ArtifactJob
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, internalErr("作业记录损坏（拒绝自动重置）: " + err.Error())
	}
	return jobs, nil
}

func (r *Runtime) saveJobs(jobs []ArtifactJob) *pluginapi.Error {
	if len(jobs) > maxJobRecords {
		jobs = jobs[len(jobs)-maxJobRecords:]
	}
	if err := ensureStateDir(r.opts.StateDir); err != nil {
		return internalErr("创建状态目录失败: " + err.Error())
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return internalErr("序列化作业记录失败: " + err.Error())
	}
	data = append(data, '\n')
	target := filepath.Join(r.opts.StateDir, jobsFile)
	tmp, err := os.CreateTemp(r.opts.StateDir, ".jobs-*.json")
	if err != nil {
		return internalErr("创建临时作业记录失败: " + err.Error())
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return internalErr("设置作业记录权限失败: " + err.Error())
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return internalErr("写入作业记录失败: " + err.Error())
	}
	if err := tmp.Close(); err != nil {
		return internalErr("关闭作业记录失败: " + err.Error())
	}
	if err := os.Rename(name, target); err != nil {
		return internalErr("替换作业记录失败: " + err.Error())
	}
	return nil
}

// upsertJob 插入或替换一条作业记录并落盘。
//
// 读-改-写必须持状态锁：否则并发写者各自拿旧快照覆盖，作业记录会丢
// （实测 8 并发 upload 只剩 3 条）。
func (r *Runtime) upsertJob(job ArtifactJob) *pluginapi.Error {
	unlock, perr := lockState(r.opts.StateDir)
	if perr != nil {
		// 状态目录不可用时锁也打不开：错误信息必须明确指向「作业记录」这个
		// 失败面（E_INTERNAL 契约要求 message 能定位子系统，不能笼统报错）。
		return internalErr("作业记录不可用（" + perr.Message + "）")
	}
	defer unlock()
	jobs, perr := r.loadJobs()
	if perr != nil {
		return perr
	}
	replaced := false
	for i := range jobs {
		if jobs[i].ID == job.ID {
			jobs[i] = job
			replaced = true
			break
		}
	}
	if !replaced {
		jobs = append(jobs, job)
	}
	return r.saveJobs(jobs)
}

// step 追加一条阶段记录并同步作业状态。
func (r *Runtime) step(job *ArtifactJob, name, status string) {
	job.Steps = append(job.Steps, JobStep{Name: name, Status: status, At: nowStamp()})
	if status == JobFailed {
		job.Status = JobFailed
		job.FinishedAt = nowStamp()
	}
	_ = r.upsertJob(*job) // 阶段落盘失败不中断主流程，最终状态会再落一次
}

// decodeArtifact 校验并解码包内容（先 sha、后解压前置护栏）。
func decodeArtifact(p artifactParams) ([]byte, string, *pluginapi.Error) {
	if p.ContentBase64 == "" {
		return nil, "", invalid("缺少 content_base64（本插件不接受任意宿主机路径）")
	}
	raw, err := base64.StdEncoding.DecodeString(p.ContentBase64)
	if err != nil {
		return nil, "", invalid("content_base64 不是合法 base64: " + err.Error())
	}
	if len(raw) == 0 {
		return nil, "", invalid("包内容为空")
	}
	if len(raw) > MaxArtifactBytes {
		return nil, "", invalid("包超过上限 " + strconv.Itoa(MaxArtifactBytes) + " 字节: " + strconv.Itoa(len(raw)))
	}
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])
	if p.Sha256 != "" && !strings.EqualFold(strings.TrimSpace(p.Sha256), digest) {
		return nil, "", invalid("包 sha256 校验失败：声明 " + p.Sha256 + "，实际 " + digest)
	}
	return raw, digest, nil
}

// normalizeFormat 归一包格式；缺省按魔数猜。
func normalizeFormat(raw string, data []byte) (string, *pluginapi.Error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case ArtifactFormatZip, "application/zip":
		return ArtifactFormatZip, nil
	case ArtifactFormatTarGz, "tgz", "tar":
		return ArtifactFormatTarGz, nil
	case "":
		if len(data) >= 2 && data[0] == 'P' && data[1] == 'K' {
			return ArtifactFormatZip, nil
		}
		if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
			return ArtifactFormatTarGz, nil
		}
		return "", invalid("无法识别包格式：请显式传 format=zip|tar.gz")
	}
	return "", invalid("format 必须是 zip / tar.gz（收到 " + raw + "）")
}

// safeEntryName 校验单个解压条目名，返回相对路径；任何逃逸企图直接拒绝。
func safeEntryName(name string) (string, error) {
	raw := strings.TrimSpace(name)
	if raw == "" {
		return "", errors.New("空条目名")
	}
	if strings.ContainsRune(raw, 0) {
		return "", errors.New("条目名含 NUL: " + name)
	}
	if filepath.IsAbs(raw) || strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`) {
		return "", errors.New("绝对路径条目被拒绝: " + name)
	}
	if strings.Contains(raw, `\`) {
		return "", errors.New("反斜杠条目被拒绝: " + name)
	}
	if len(raw) >= 2 && raw[1] == ':' {
		return "", errors.New("带盘符条目被拒绝: " + name)
	}
	cleaned := filepath.Clean(raw)
	if cleaned == "." {
		return "", errors.New("空目录条目被拒绝: " + name)
	}
	for _, part := range strings.Split(cleaned, string(filepath.Separator)) {
		if part == ".." {
			return "", errors.New("路径穿越条目被拒绝: " + name)
		}
	}
	return cleaned, nil
}

// extractZip / extractTarGz 把归档解到 dest，逐条目护栏（穿越/符号链接/炸弹）。
func extractZip(data []byte, dest string) (int, int64, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0, 0, errors.New("坏包（zip 解析失败）: " + err.Error())
	}
	if len(reader.File) > MaxArtifactEntries {
		return 0, 0, errors.New("条目数超过上限: " + strconv.Itoa(len(reader.File)))
	}
	files := 0
	var total int64
	for _, file := range reader.File {
		rel, err := safeEntryName(file.Name)
		if err != nil {
			return 0, 0, err
		}
		mode := file.Mode()
		if mode&os.ModeSymlink != 0 {
			return 0, 0, errors.New("符号链接条目被拒绝: " + file.Name)
		}
		target := filepath.Join(dest, rel)
		if !IsUnder(dest, target) {
			return 0, 0, errors.New("路径穿越条目被拒绝: " + file.Name)
		}
		if mode.IsDir() || strings.HasSuffix(file.Name, "/") {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return 0, 0, err
			}
			continue
		}
		if file.UncompressedSize64 > MaxArtifactFileBytes {
			return 0, 0, errors.New("单文件超过上限: " + file.Name)
		}
		total += int64(file.UncompressedSize64)
		if total > MaxArtifactTotalBytes {
			return 0, 0, errors.New("解压总量超过上限")
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return 0, 0, err
		}
		rc, err := file.Open()
		if err != nil {
			return 0, 0, errors.New("读取包条目失败: " + err.Error())
		}
		if err := writeEntry(target, rc); err != nil {
			_ = rc.Close()
			return 0, 0, err
		}
		_ = rc.Close()
		files++
	}
	return files, total, nil
}

func extractTarGz(data []byte, dest string) (int, int64, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return 0, 0, errors.New("坏包（gzip 解析失败）: " + err.Error())
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	files := 0
	var total int64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, 0, errors.New("坏包（tar 解析失败）: " + err.Error())
		}
		if files+1 > MaxArtifactEntries {
			return 0, 0, errors.New("条目数超过上限")
		}
		rel, err := safeEntryName(header.Name)
		if err != nil {
			return 0, 0, err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			target := filepath.Join(dest, rel)
			if !IsUnder(dest, target) {
				return 0, 0, errors.New("路径穿越条目被拒绝: " + header.Name)
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return 0, 0, err
			}
		case tar.TypeReg, tar.TypeRegA:
			target := filepath.Join(dest, rel)
			if !IsUnder(dest, target) {
				return 0, 0, errors.New("路径穿越条目被拒绝: " + header.Name)
			}
			if header.Size > MaxArtifactFileBytes {
				return 0, 0, errors.New("单文件超过上限: " + header.Name)
			}
			total += header.Size
			if total > MaxArtifactTotalBytes {
				return 0, 0, errors.New("解压总量超过上限")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return 0, 0, err
			}
			if err := writeEntry(target, io.LimitReader(reader, MaxArtifactFileBytes+1)); err != nil {
				return 0, 0, err
			}
			files++
		case tar.TypeSymlink, tar.TypeLink:
			return 0, 0, errors.New("链接条目被拒绝: " + header.Name)
		default:
			return 0, 0, errors.New("不支持的条目类型被拒绝: " + header.Name)
		}
	}
	return files, total, nil
}

// writeEntry 以 0644 新建文件（O_EXCL，不覆盖既有文件）。
func writeEntry(target string, src io.Reader) error {
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(file, io.LimitReader(src, MaxArtifactFileBytes+1)); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

// extractInto 把归档解到全新目录（先解到 staging，成功后 rename 到目标）。
func extractInto(root, name, format string, data []byte) (dir string, files int, total int64, perr *pluginapi.Error) {
	target := filepath.Join(root, name)
	if !IsUnder(root, target) {
		return "", 0, 0, invalid("目标目录不在数据根内")
	}
	if _, err := os.Stat(target); err == nil {
		return "", 0, 0, conflict("目标目录已存在: " + target + "（升级请用 app-deploy.upgrade）")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", 0, 0, internalErr("创建数据根失败: " + err.Error())
	}
	staging, err := os.MkdirTemp(root, "."+stagingSubdir+"-*")
	if err != nil {
		return "", 0, 0, internalErr("创建解压暂存目录失败: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(staging) }()
	switch format {
	case ArtifactFormatZip:
		files, total, err = extractZip(data, staging)
	case ArtifactFormatTarGz:
		files, total, err = extractTarGz(data, staging)
	default:
		return "", 0, 0, invalid("不支持的格式: " + format)
	}
	if err != nil {
		return "", 0, 0, invalid(err.Error())
	}
	if files == 0 {
		return "", 0, 0, invalid("包内没有可部署文件")
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", 0, 0, internalErr("创建目标父目录失败: " + err.Error())
	}
	if err := os.Rename(staging, target); err != nil {
		return "", 0, 0, internalErr("落盘失败: " + err.Error())
	}
	return target, files, total, nil
}

// artifactDir 返回某个包在数据根下的目录。
func (r *Runtime) artifactDir(name string) string { return filepath.Join(r.opts.Root, name) }

// callUpload 处理 app-deploy.upload：解码 → 逐条目护栏 → 解压到 <root>/<name>。
func (r *Runtime) callUpload(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p artifactParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if perr := validateArtifactName(p.Name); perr != nil {
		return nil, perr
	}
	data, digest, perr := decodeArtifact(p)
	if perr != nil {
		return nil, perr
	}
	format, perr := normalizeFormat(p.Format, data)
	if perr != nil {
		return nil, perr
	}
	job := ArtifactJob{ID: newJobID(), Name: p.Name, Action: "upload", Status: JobRunning, StartedAt: nowStamp()}
	r.step(&job, "received", JobSucceeded)
	r.step(&job, "extract", JobRunning)
	dir, files, total, perr := extractInto(r.opts.Root, p.Name, format, data)
	if perr != nil {
		job.Error = perr.Message
		r.step(&job, "extract", JobFailed)
		return nil, perr
	}
	job.ArtifactDir = dir
	job.Status = JobSucceeded
	job.FinishedAt = nowStamp()
	job.Steps = append(job.Steps, JobStep{Name: "extract", Status: JobSucceeded, At: nowStamp()})
	if perr := r.upsertJob(job); perr != nil {
		return nil, perr
	}
	return encode(UploadResult{
		Name: p.Name, Path: dir, Files: files, Bytes: total, Sha256: digest,
		Job: job.ID, OutsideRoot: false,
	})
}

// callDeployFromArtifact 处理 app-deploy.deployFromArtifact：登记部署记录 + 站点/建库提示。
// 不启动容器、不写 nginx、不连 MySQL：容器清单属本仓模板能力，vhost 属 site，建库属 db-mysql。
func (r *Runtime) callDeployFromArtifact(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p artifactParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if perr := validateArtifactName(p.Name); perr != nil {
		return nil, perr
	}
	job := ArtifactJob{ID: newJobID(), Name: p.Name, Action: "deployFromArtifact", Status: JobRunning, StartedAt: nowStamp()}
	r.step(&job, "received", JobSucceeded)
	dir := r.artifactDir(p.Name)
	if p.ContentBase64 != "" {
		data, _, perr := decodeArtifact(p)
		if perr != nil {
			job.Error = perr.Message
			r.step(&job, "extract", JobFailed)
			return nil, perr
		}
		format, perr := normalizeFormat(p.Format, data)
		if perr != nil {
			return nil, perr
		}
		extracted, _, _, perr := extractInto(r.opts.Root, p.Name, format, data)
		if perr != nil {
			job.Error = perr.Message
			r.step(&job, "extract", JobFailed)
			return nil, perr
		}
		dir = extracted
	} else if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return nil, notFound("应用包不存在: " + p.Name + "（先 app-deploy.upload 或在本方法带 content_base64）")
	}
	job.ArtifactDir = dir
	r.step(&job, "extract", JobSucceeded)
	now := nowStamp()
	record := Deployment{
		ID: newJobID(), Template: ArtifactTemplate, Name: p.Name,
		Volumes:   []VolumeRef{{Name: dir, Kind: VolumeBind}},
		CreatedAt: now, Status: StatusExited,
	}
	r.step(&job, "register", JobRunning)
	// 登记是「读-改-写」：持状态锁重读最新快照再 upsert，避免覆盖并发写者的新增。
	// step 会写作业文件（同一把锁），所以必须在临界区外调用，不能嵌套加锁。
	saveErr := func() *pluginapi.Error {
		unlock, perr := lockState(r.opts.StateDir)
		if perr != nil {
			return perr
		}
		defer unlock()
		st, perr := loadState(r.opts.StateDir)
		if perr != nil {
			return perr
		}
		upsertDeployment(&st, record)
		return saveState(r.opts.StateDir, st)
	}()
	if saveErr != nil {
		job.Error = saveErr.Message
		r.step(&job, "register", JobFailed)
		return nil, saveErr
	}
	job.Status = JobSucceeded
	job.FinishedAt = nowStamp()
	job.Steps = append(job.Steps, JobStep{Name: "register", Status: JobSucceeded, At: nowStamp()})
	if perr := r.upsertJob(job); perr != nil {
		return nil, perr
	}
	siteHint := SiteHint{
		Domain: strings.TrimSpace(p.SiteDomain), Port: p.SitePort, Root: dir,
		Hint: "站点 vhost 属 site 插件的写域：请到「站点」页创建站点，根目录填 " + dir + "（本插件不写 nginx、不 reload）",
	}
	hints := []string{siteHint.Hint}
	for _, decl := range p.Databases {
		hints = append(hints, databaseHint(decl))
	}
	return encode(ArtifactDeployResult{
		Deployed: true, Name: p.Name, Path: dir, Template: ArtifactTemplate,
		SiteHint: siteHint, Databases: p.Databases, Hints: hints, Job: job,
	})
}

// databaseHint 把建库声明转成可操作提示（不连 MySQL、不建库）。
func databaseHint(decl DatabaseDecl) string {
	engine := strings.TrimSpace(decl.Engine)
	if engine == "" {
		engine = "mysql"
	}
	return "数据库 '" + decl.Name + "'（" + engine + "）需要在「数据库」页由 db-mysql 插件创建：本插件不持有数据库凭据、不跨插件调用（G-13）"
}

// callUpgrade 处理 app-deploy.upgrade：新包解到暂存 → 旧目录快照 → 换入 → 失败回滚。
func (r *Runtime) callUpgrade(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p artifactParams
	if perr := decodeParams(raw, &p); perr != nil {
		return nil, perr
	}
	if perr := validateArtifactName(p.Name); perr != nil {
		return nil, perr
	}
	data, _, perr := decodeArtifact(p)
	if perr != nil {
		return nil, perr
	}
	format, perr := normalizeFormat(p.Format, data)
	if perr != nil {
		return nil, perr
	}
	target := r.artifactDir(p.Name)
	if _, err := os.Stat(target); err != nil {
		return nil, notFound("应用包不存在: " + p.Name + "（首次部署请用 app-deploy.upload）")
	}
	job := ArtifactJob{ID: newJobID(), Name: p.Name, Action: "upgrade", Status: JobRunning, StartedAt: nowStamp()}
	r.step(&job, "received", JobSucceeded)
	// 1) 新包解到同根暂存目录（不碰旧目录）
	staging, err := os.MkdirTemp(filepath.Dir(target), "."+stagingSubdir+"-*")
	if err != nil {
		return nil, internalErr("创建升级暂存目录失败: " + err.Error())
	}
	defer func() { _ = os.RemoveAll(staging) }()
	newDir := filepath.Join(staging, "new")
	if err := os.MkdirAll(newDir, 0o755); err != nil {
		return nil, internalErr("创建升级目录失败: " + err.Error())
	}
	var files int
	var total int64
	switch format {
	case ArtifactFormatZip:
		files, total, err = extractZip(data, newDir)
	case ArtifactFormatTarGz:
		files, total, err = extractTarGz(data, newDir)
	}
	if err != nil {
		job.Error = err.Error()
		r.step(&job, "extract", JobFailed)
		return nil, invalid(err.Error())
	}
	r.step(&job, "extract", JobSucceeded)
	if files == 0 {
		r.step(&job, "extract", JobFailed)
		return nil, invalid("包内没有可部署文件")
	}
	// 2) 旧目录快照（<state>/snapshots/<name>/<ts>）
	snapshotRoot := filepath.Join(r.opts.StateDir, snapshotsSubdir, p.Name)
	if err := os.MkdirAll(snapshotRoot, 0o700); err != nil {
		return nil, internalErr("创建快照目录失败: " + err.Error())
	}
	snapshot := filepath.Join(snapshotRoot, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(snapshot, 0o700); err != nil {
		return nil, internalErr("创建快照失败: " + err.Error())
	}
	if perr := copyTree(target, snapshot); perr != nil {
		job.Error = perr.Message
		r.step(&job, "snapshot", JobFailed)
		return nil, perr
	}
	job.Snapshot = snapshot
	r.step(&job, "snapshot", JobSucceeded)
	// 3) 换入：旧目录改名备份 → 新目录改名到位；任一步失败回滚
	backup := target + ".old-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if err := renameFn(target, backup); err != nil {
		job.Error = "旧目录改名失败: " + err.Error()
		r.step(&job, "swap", JobFailed)
		return nil, internalErr(job.Error)
	}
	if err := renameFn(newDir, target); err != nil {
		_ = renameFn(backup, target) // 回滚
		job.Error = "新目录换入失败（已回滚）: " + err.Error()
		r.step(&job, "swap", JobFailed)
		return nil, internalErr(job.Error)
	}
	r.step(&job, "swap", JobSucceeded)
	_ = os.RemoveAll(backup)
	job.Status = JobSucceeded
	job.FinishedAt = nowStamp()
	if perr := r.upsertJob(job); perr != nil {
		return nil, perr
	}
	return encode(UpgradeResult{
		Name: p.Name, Upgraded: true, Snapshot: snapshot, Files: files, Bytes: total, Job: job,
	})
}

// callJobs 处理 app-deploy.jobs：只读查询作业状态（供 UI 轮询，无后台 ticker）。
func (r *Runtime) callJobs(raw json.RawMessage) (json.RawMessage, *pluginapi.Error) {
	var p jobsParams
	if len(raw) != 0 && string(raw) != "null" {
		if perr := decodeParams(raw, &p); perr != nil {
			return nil, perr
		}
	}
	jobs, perr := r.loadJobs()
	if perr != nil {
		return nil, perr
	}
	if p.ID != "" {
		filtered := make([]ArtifactJob, 0, 1)
		for _, job := range jobs {
			if job.ID == p.ID {
				filtered = append(filtered, job)
			}
		}
		return encode(JobsResult{Jobs: filtered, Count: len(filtered)})
	}
	sort.SliceStable(jobs, func(i, j int) bool { return jobs[i].StartedAt < jobs[j].StartedAt })
	return encode(JobsResult{Jobs: jobs, Count: len(jobs)})
}

// copyTree 递归复制目录（文件 0644、目录 0700），快照失败即报错。
func copyTree(src, dst string) *pluginapi.Error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return internalErr("读取待快照目录失败: " + err.Error())
	}
	for _, entry := range entries {
		from := filepath.Join(src, entry.Name())
		to := filepath.Join(dst, entry.Name())
		if entry.IsDir() {
			if err := os.MkdirAll(to, 0o700); err != nil {
				return internalErr("创建快照子目录失败: " + err.Error())
			}
			if perr := copyTree(from, to); perr != nil {
				return perr
			}
			continue
		}
		data, err := os.ReadFile(from)
		if err != nil {
			return internalErr("读取快照源文件失败: " + err.Error())
		}
		if err := os.WriteFile(to, data, 0o600); err != nil {
			return internalErr("写入快照文件失败: " + err.Error())
		}
	}
	return nil
}

// newJobID 生成作业 id（随机 6 字节，不依赖时间碰撞）。
func newJobID() string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return "job-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "job-" + hex.EncodeToString(buf)
}
