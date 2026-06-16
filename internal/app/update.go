package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"pikpak2directlink/internal/version"
)

// assetPrefix is the leading token of every release binary uploaded by the
// GitHub Actions workflow, e.g. "Pikpak2DirectLink_linux_amd64". The updater
// and .github/workflows/release.yml must agree on this name.
const assetPrefix = "Pikpak2DirectLink"

// checksumFile is the manifest of SHA256 hashes published alongside the
// binaries. It is produced by `sha256sum *` in the release workflow.
const checksumFile = "SHA256SUMS"

// repoPattern matches a clean GitHub "owner/name" slug.
var repoPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

// UpdatePhase enumerates the stages the self-updater moves through. The frontend
// renders a different progress affordance per phase.
type UpdatePhase string

const (
	UpdateIdle        UpdatePhase = "idle"
	UpdateChecking    UpdatePhase = "checking"
	UpdateUpToDate    UpdatePhase = "up_to_date"
	UpdateAvailable   UpdatePhase = "available"
	UpdateDownloading UpdatePhase = "downloading"
	UpdateVerifying   UpdatePhase = "verifying"
	UpdateInstalling  UpdatePhase = "installing"
	UpdateRestarting  UpdatePhase = "restarting"
	UpdateError       UpdatePhase = "error"
)

// updateStatus is the JSON contract shared with the frontend. It captures both
// the result of the latest check and the live progress of an in-flight install.
type updateStatus struct {
	Phase           UpdatePhase `json:"phase"`
	CurrentVersion  string      `json:"current_version"`
	LatestVersion   string      `json:"latest_version,omitempty"`
	UpdateAvailable bool        `json:"update_available"`
	DownloadedBytes int64       `json:"downloaded_bytes"`
	TotalBytes      int64       `json:"total_bytes"`
	Progress        float64     `json:"progress"`
	Message         string      `json:"message,omitempty"`
	Error           string      `json:"error,omitempty"`
	ReleaseNotes    string      `json:"release_notes,omitempty"`
	ReleaseURL      string      `json:"release_url,omitempty"`
	AssetName       string      `json:"asset_name,omitempty"`
	CheckedAt       string      `json:"checked_at,omitempty"`
	Repo            string      `json:"repo"`
	Platform        string      `json:"platform"`
	Managed         bool        `json:"managed"`
}

// ghRelease and ghAsset mirror the slice of the GitHub Releases API we consume.
type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Name       string    `json:"name"`
	Body       string    `json:"body"`
	HTMLURL    string    `json:"html_url"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Assets     []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

// updater owns the self-update state machine. A single mutex guards both the
// published status and the "an operation is running" flag, so a check and an
// install can never race.
type updater struct {
	repo      string
	apiClient *http.Client
	dlClient  *http.Client
	logf      func(level LogLevel, message string, details ...string)
	exit      func(code int) // injectable for tests; defaults to os.Exit

	mu     sync.Mutex
	status updateStatus
	busy   bool
}

func newUpdater(repo string, requestTimeout time.Duration, logf func(LogLevel, string, ...string)) *updater {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	// Reject anything that is not a clean "owner/name" so the value cannot smuggle
	// extra path segments or traversal into the GitHub API URL. Fall back to the
	// default repo on a malformed override.
	if !repoPattern.MatchString(repo) {
		repo = version.DefaultRepo
	}
	if logf == nil {
		logf = func(LogLevel, string, ...string) {}
	}
	if requestTimeout <= 0 {
		requestTimeout = 20 * time.Second
	}

	managed := true
	if _, err := resolveExecutable(); err != nil {
		managed = false
	}

	u := &updater{
		repo:      repo,
		apiClient: &http.Client{Timeout: requestTimeout},
		// Downloads can be tens of megabytes; rely on the per-request context
		// rather than a fixed client timeout.
		dlClient: &http.Client{},
		logf:     logf,
		exit:     os.Exit,
	}
	u.status = updateStatus{
		Phase:          UpdateIdle,
		CurrentVersion: version.Version,
		Repo:           repo,
		Platform:       runtime.GOOS + "/" + runtime.GOARCH,
		Managed:        managed,
	}
	// Best-effort: clear a stale binary left behind by a previous update.
	cleanupLeftovers()
	return u
}

// snapshot returns a copy of the current status safe to serialize.
func (u *updater) snapshot() updateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.status
}

// mutate applies fn to the status under lock.
func (u *updater) mutate(fn func(*updateStatus)) {
	u.mu.Lock()
	defer u.mu.Unlock()
	fn(&u.status)
}

// runPeriodicCheck checks for updates shortly after startup and then on every
// tick. It never installs automatically — it only flips UpdateAvailable so the
// UI can surface the one-click button.
func (u *updater) runPeriodicCheck(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}

	// Give the network a moment to settle before the first check.
	select {
	case <-ctx.Done():
		return
	case <-time.After(8 * time.Second):
	}
	u.check(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.check(ctx)
		}
	}
}

// check queries the latest release and records whether an update is available.
// It is a no-op while an install is in progress. It returns the resulting
// status snapshot.
func (u *updater) check(ctx context.Context) updateStatus {
	u.mu.Lock()
	if u.busy {
		status := u.status
		u.mu.Unlock()
		return status
	}
	u.busy = true
	u.status.Phase = UpdateChecking
	u.status.Error = ""
	u.mu.Unlock()

	release, err := u.fetchLatestRelease(ctx)
	checkedAt := time.Now().Format(time.RFC3339)

	u.mu.Lock()
	defer u.mu.Unlock()
	u.busy = false
	u.status.CheckedAt = checkedAt
	if err != nil {
		u.status.Phase = UpdateError
		u.status.Error = err.Error()
		u.logf(LogWarn, "检查更新失败", err.Error())
		return u.status
	}

	latest := strings.TrimSpace(release.TagName)
	u.status.LatestVersion = latest
	u.status.ReleaseNotes = release.Body
	u.status.ReleaseURL = release.HTMLURL
	u.status.Error = ""

	if compareVersions(latest, version.Version) > 0 {
		u.status.UpdateAvailable = true
		u.status.Phase = UpdateAvailable
		u.status.Message = "发现新版本 " + latest
		u.logf(LogInfo, "发现新版本", "当前："+version.Version, "最新："+latest)
	} else {
		u.status.UpdateAvailable = false
		u.status.Phase = UpdateUpToDate
		u.status.Message = "已是最新版本"
	}
	return u.status
}

// startInstall kicks off the download/verify/replace pipeline in the background
// when an update is available. It returns an error if an operation is already
// running or no update was found.
func (u *updater) startInstall() error {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.busy {
		return errors.New("an update operation is already in progress")
	}
	if !u.status.Managed {
		return errors.New("self-update is not supported for this binary")
	}
	if !u.status.UpdateAvailable || u.status.LatestVersion == "" {
		return errors.New("no update is available; run a check first")
	}

	u.busy = true
	u.status.Phase = UpdateDownloading
	u.status.Error = ""
	u.status.DownloadedBytes = 0
	u.status.TotalBytes = 0
	u.status.Progress = 0
	u.status.Message = "准备下载 " + u.status.LatestVersion
	target := u.status.LatestVersion

	go u.runInstall(target)
	return nil
}

// runInstall performs the full update for the given release tag and, on success,
// replaces the running binary and exits so the supervisor restarts the new one.
func (u *updater) runInstall(tag string) {
	// Clear busy on every exit path...
	defer func() {
		u.mu.Lock()
		u.busy = false
		u.mu.Unlock()
	}()
	// ...and never let a panic in the download/install pipeline take down the
	// whole server. This recover runs after the busy-clear during unwind.
	defer func() {
		if r := recover(); r != nil {
			u.failInstall("更新过程发生异常", fmt.Errorf("%v", r))
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	u.logf(LogInfo, "开始下载更新", "版本："+tag)

	release, err := u.fetchLatestRelease(ctx)
	if err != nil {
		u.failInstall("获取发布信息失败", err)
		return
	}
	if compareVersions(release.TagName, tag) != 0 {
		// The latest release moved between the check and the install; retarget.
		tag = release.TagName
	}

	asset, ok := selectAsset(release.Assets, runtime.GOOS, runtime.GOARCH)
	if !ok {
		u.failInstall("没有匹配当前架构的发布文件",
			errors.New("no asset matched "+runtime.GOOS+"/"+runtime.GOARCH))
		return
	}
	u.mutate(func(s *updateStatus) { s.AssetName = asset.Name })

	exe, err := resolveExecutable()
	if err != nil {
		u.failInstall("无法定位当前可执行文件", err)
		return
	}
	newPath := exe + ".new"
	_ = os.Remove(newPath)

	// Download the binary, streaming progress to the shared status.
	sum, err := u.downloadAsset(ctx, asset, newPath)
	if err != nil {
		_ = os.Remove(newPath)
		u.failInstall("下载更新失败", err)
		return
	}

	// Verify against the published checksum manifest when present.
	u.mutate(func(s *updateStatus) {
		s.Phase = UpdateVerifying
		s.Message = "校验文件完整性"
	})
	if err := u.verifyChecksum(ctx, release.Assets, asset.Name, sum); err != nil {
		_ = os.Remove(newPath)
		u.failInstall("校验失败", err)
		return
	}
	u.logf(LogSuccess, "更新文件校验通过", "SHA256："+sum[:16]+"…")

	// Swap the binary into place.
	u.mutate(func(s *updateStatus) {
		s.Phase = UpdateInstalling
		s.Message = "安装新版本"
		s.Progress = 100
	})
	if err := replaceExecutable(exe, newPath); err != nil {
		_ = os.Remove(newPath)
		u.failInstall("替换可执行文件失败", err)
		return
	}
	u.logf(LogSuccess, "新版本已安装，正在重启", "版本："+tag)

	u.mutate(func(s *updateStatus) {
		s.Phase = UpdateRestarting
		s.CurrentVersion = tag
		s.UpdateAvailable = false
		s.Message = "正在重启以应用新版本"
	})

	// Give the HTTP layer a beat to flush the final status to the polling UI,
	// then exit so the supervisor (systemd Restart=always) launches the new
	// binary that is now on disk.
	go func() {
		time.Sleep(1200 * time.Millisecond)
		u.exit(0)
	}()
}

func (u *updater) failInstall(message string, err error) {
	detail := ""
	if err != nil {
		detail = err.Error()
	}
	u.logf(LogError, message, detail)
	u.mutate(func(s *updateStatus) {
		s.Phase = UpdateError
		s.Message = message
		s.Error = detail
	})
}

// fetchLatestRelease pulls the newest non-draft release from GitHub.
func (u *updater) fetchLatestRelease(ctx context.Context) (*ghRelease, error) {
	endpoint := "https://api.github.com/repos/" + u.repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", assetPrefix+"-updater")

	resp, err := u.apiClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("仓库还没有发布任何版本（GitHub Releases 为空）")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API 返回 %d", resp.StatusCode)
	}

	var release ghRelease
	if err := json.Unmarshal(body, &release); err != nil {
		return nil, err
	}
	if strings.TrimSpace(release.TagName) == "" {
		return nil, errors.New("发布信息缺少版本号")
	}
	return &release, nil
}

// downloadAsset streams asset into dst while updating progress, and returns the
// hex-encoded SHA256 of the downloaded bytes.
func (u *updater) downloadAsset(ctx context.Context, asset ghAsset, dst string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", assetPrefix+"-updater")

	resp, err := u.dlClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载返回 %d", resp.StatusCode)
	}

	total := asset.Size
	if total <= 0 {
		total = resp.ContentLength
	}
	u.mutate(func(s *updateStatus) {
		s.TotalBytes = total
		s.DownloadedBytes = 0
		s.Progress = 0
	})

	file, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return "", err
	}

	// Cap the bytes we are willing to write so a misbehaving CDN/response cannot
	// fill the disk. If the size is known we allow a small slack; otherwise a
	// generous flat ceiling. Truncation just makes the checksum fail safely.
	limit := int64(1 << 30) // 1 GiB fallback
	if total > 0 {
		limit = total + (1 << 20)
	}

	hasher := sha256.New()
	pr := &progressReader{
		reader: io.LimitReader(resp.Body, limit),
		total:  total,
		onProgress: func(read, total int64) {
			progress := 0.0
			if total > 0 {
				progress = float64(read) / float64(total) * 100
			}
			u.mutate(func(s *updateStatus) {
				s.DownloadedBytes = read
				s.TotalBytes = total
				s.Progress = progress
				s.Phase = UpdateDownloading
			})
		},
	}

	_, copyErr := io.Copy(io.MultiWriter(file, hasher), pr)
	closeErr := file.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// verifyChecksum downloads the SHA256SUMS manifest published with the release
// and confirms the downloaded asset matches. The release workflow always
// publishes this manifest, so a missing one is treated as a hard failure rather
// than installing an unverified binary.
func (u *updater) verifyChecksum(ctx context.Context, assets []ghAsset, assetName, gotSum string) error {
	var manifest *ghAsset
	for i := range assets {
		if assets[i].Name == checksumFile {
			manifest = &assets[i]
			break
		}
	}
	if manifest == nil {
		return errors.New("发布未提供 " + checksumFile + " 校验清单，已中止安装")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifest.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", assetPrefix+"-updater")
	resp, err := u.apiClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载校验清单返回 %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	want, ok := lookupChecksum(string(data), assetName)
	if !ok {
		return errors.New("校验清单中没有该文件的哈希")
	}
	if !strings.EqualFold(want, gotSum) {
		return fmt.Errorf("哈希不匹配：期望 %s，实际 %s", want, gotSum)
	}
	return nil
}

// progressReader wraps an io.Reader, invoking onProgress (throttled) as bytes
// flow through it.
type progressReader struct {
	reader     io.Reader
	total      int64
	read       int64
	lastNotify int64
	onProgress func(read, total int64)
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.reader.Read(buf)
	if n > 0 {
		p.read += int64(n)
	}
	// Notify at most every 256KB to keep lock churn low, and always on the
	// terminating read so the final 100% is reported even when EOF arrives with
	// a zero-byte read.
	if p.onProgress != nil && (p.read-p.lastNotify >= 256<<10 || err != nil) {
		p.lastNotify = p.read
		p.onProgress(p.read, p.total)
	}
	return n, err
}

// --- pure helpers (unit-tested) ---

// expectedAssetName returns the canonical asset name for an os/arch pair.
func expectedAssetName(goos, goarch string) string {
	name := assetPrefix + "_" + goos + "_" + goarch
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

// selectAsset finds the release asset matching the running os/arch. It prefers
// an exact name match and falls back to a whole-token match to tolerate naming
// drift. The fallback splits on '_', '.', and '-' and requires both the os and
// arch to appear as complete tokens, so "arm" never matches an "arm64" asset.
func selectAsset(assets []ghAsset, goos, goarch string) (ghAsset, bool) {
	want := expectedAssetName(goos, goarch)
	for _, a := range assets {
		if a.Name == want {
			return a, true
		}
	}
	for _, a := range assets {
		if a.Name == checksumFile {
			continue
		}
		tokens := strings.FieldsFunc(strings.ToLower(a.Name), func(r rune) bool {
			return r == '_' || r == '.' || r == '-'
		})
		if containsToken(tokens, strings.ToLower(goos)) && containsToken(tokens, strings.ToLower(goarch)) {
			// On Windows insist on an .exe asset so we never install a
			// non-executable file as the binary.
			if goos == "windows" && !strings.HasSuffix(strings.ToLower(a.Name), ".exe") {
				continue
			}
			return a, true
		}
	}
	return ghAsset{}, false
}

func containsToken(tokens []string, want string) bool {
	for _, t := range tokens {
		if t == want {
			return true
		}
	}
	return false
}

// lookupChecksum parses an `sha256sum`-style manifest and returns the hash for
// the named file. It tolerates the "*filename" binary-mode marker and path
// prefixes on the filename.
func lookupChecksum(manifest, name string) (string, bool) {
	for _, line := range strings.Split(manifest, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		hash := fields[0]
		file := strings.TrimPrefix(strings.Join(fields[1:], " "), "*")
		file = filepath.Base(strings.TrimSpace(file))
		if file == name {
			return hash, true
		}
	}
	return "", false
}

// compareVersions compares two version strings (semver-lite). It returns >0 when
// a is newer than b, <0 when older, 0 when equal. The sentinel "dev" is treated
// as older than any real release.
func compareVersions(a, b string) int {
	if a == b {
		return 0
	}
	na := normalizeVersion(a)
	nb := normalizeVersion(b)
	if na == "dev" && nb == "dev" {
		return 0
	}
	if na == "dev" {
		return -1
	}
	if nb == "dev" {
		return 1
	}

	coreA, preA := splitPrerelease(na)
	coreB, preB := splitPrerelease(nb)

	partsA := versionParts(coreA)
	partsB := versionParts(coreB)
	for i := 0; i < 3; i++ {
		if partsA[i] != partsB[i] {
			if partsA[i] > partsB[i] {
				return 1
			}
			return -1
		}
	}

	// Equal core: a release (no prerelease) outranks a prerelease.
	switch {
	case preA == "" && preB == "":
		return 0
	case preA == "":
		return 1
	case preB == "":
		return -1
	default:
		return strings.Compare(preA, preB)
	}
}

func normalizeVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "dev"
	}
	if strings.EqualFold(v, "dev") {
		return "dev"
	}
	return strings.TrimPrefix(strings.TrimPrefix(v, "v"), "V")
}

func splitPrerelease(v string) (core, pre string) {
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return v[:i], v[i+1:]
	}
	// Drop build metadata.
	if i := strings.IndexByte(v, '+'); i >= 0 {
		return v[:i], ""
	}
	return v, ""
}

func versionParts(core string) [3]int {
	var out [3]int
	for i, part := range strings.SplitN(core, ".", 3) {
		if i > 2 {
			break
		}
		// Strip any trailing build metadata on the patch segment.
		if j := strings.IndexAny(part, "+"); j >= 0 {
			part = part[:j]
		}
		n, _ := strconv.Atoi(strings.TrimSpace(part))
		out[i] = n
	}
	return out
}

// resolveExecutable returns the real path of the running binary with symlinks
// resolved, so we replace the actual file rather than a symlink.
func resolveExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved, nil
	}
	return exe, nil
}

// replaceExecutable swaps newPath in for the running binary at exe. It moves the
// current binary aside first (which both Unix and Windows allow even while it is
// running) so the rename of the new file into place cannot clobber a locked
// file. On any failure it leaves a working binary in place and reports every
// error so an operator can recover.
func replaceExecutable(exe, newPath string) error {
	if runtime.GOOS != "windows" {
		if err := os.Chmod(newPath, 0o755); err != nil {
			return err
		}
	}

	old := exe + ".old"
	_ = os.Remove(old)

	if err := os.Rename(exe, old); err != nil {
		// Some filesystems let us overwrite the running binary directly; try
		// that before giving up.
		if directErr := os.Rename(newPath, exe); directErr != nil {
			return fmt.Errorf("move current binary aside failed (%v) and direct overwrite also failed: %w", err, directErr)
		}
		return nil
	}

	if err := os.Rename(newPath, exe); err != nil {
		// Roll back so the service still has a working binary, and surface a
		// rollback failure loudly — it means exe is now missing entirely.
		if rbErr := os.Rename(old, exe); rbErr != nil {
			return fmt.Errorf("install new binary failed (%w); rollback also failed (%v) — recover the binary from %s", err, rbErr, old)
		}
		return fmt.Errorf("install new binary: %w", err)
	}

	// Best effort; on Windows the just-displaced binary may still be locked. It
	// is cleaned up on the next start regardless.
	_ = os.Remove(old)
	return nil
}

// cleanupLeftovers removes a stale ".old" binary from a previous update.
func cleanupLeftovers() {
	exe, err := resolveExecutable()
	if err != nil {
		return
	}
	_ = os.Remove(exe + ".old")
	_ = os.Remove(exe + ".new")
}
