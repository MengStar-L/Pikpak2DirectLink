package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type librarySourceError struct{ message string }

func (e *librarySourceError) Error() string { return e.message }

// Paths are relative to the original resource. Account-local IDs are never used
// to identify a magnet file after restoring it in a different account.
func matchLibraryFile(items []DownloadItem, target DownloadItem) (DownloadItem, error) {
	var matches []DownloadItem
	for _, item := range items {
		if item.Path == target.Path && item.Name == target.Name &&
			(target.Size == "" || item.Size == target.Size) {
			matches = append(matches, item)
		}
	}
	if len(matches) != 1 {
		return DownloadItem{}, &librarySourceError{"原始资源中的目标文件已变化、丢失或存在重名，无法安全下载"}
	}
	return matches[0], nil
}

func (s *Server) newLibraryJob(entry *LibraryEntry, target *DownloadItem, mode, baseURL string) (*Job, error) {
	if !s.accounts.HasAccounts() {
		return nil, errors.New("请先添加可用的 PikPak 账号")
	}
	id, err := newJobID()
	if err != nil {
		return nil, err
	}
	input := normalizeResourceInput(entry.Input)
	kind, err := detectResourceKind(input)
	if err != nil {
		return nil, err
	}
	job := &Job{ID: id, Kind: kind, Mode: mode, Input: input, OriginalInput: entry.Input,
		PassCode: entry.PassCode, Status: JobQueued, Stage: StageTransfer, BaseURL: baseURL,
		LibraryDiscovery: target == nil, LibraryTarget: target, CreatedAt: s.now(), UpdatedAt: s.now()}
	if kind == ResourceShare {
		job.Share, job.PassCode, err = shareStateAndPassCode(entry.Input, entry.PassCode)
		if err != nil {
			return nil, err
		}
	}
	err = s.admission.withCapacity("", 1, func() error { return s.jobs.create(job) })
	return job, err
}

// All work belongs to the existing bounded queue, which also joins workers on
// shutdown. No background goroutine can outlive the database.
func (s *Server) startLibraryDiscovery(id string) {
	entry, err := s.library.get(id)
	if err != nil {
		return
	}
	job, err := s.newLibraryJob(entry, nil, "direct", s.config.PublicBaseURL)
	if err != nil {
		s.recordLibraryDiscoveryFailure(id, err)
		return
	}
	err = s.resolver.enqueue(job.ID, priorityAdmin, func(ctx context.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.failJob(job.ID, fmt.Errorf("文件库检查异常：%v", recovered))
			}
			finished, ok := s.jobs.get(job.ID)
			if !ok || finished.Status != JobCompleted {
				message := "文件检查未完成，请重试"
				if ok && finished.Error != "" {
					message = finished.Error
				}
				s.recordLibraryDiscoveryFailure(id, errors.New(message))
				return
			}
			files := make([]LibraryFile, 0, len(finished.Items))
			for _, item := range finished.Items {
				files = append(files, LibraryFile{Name: item.Name, Path: item.Path, Size: item.Size, MimeType: item.MimeType, Item: item})
			}
			if err := s.library.complete(id, files); err != nil && !errors.Is(err, errLibraryNotFound) {
				s.recordLibraryDiscoveryFailure(id, err)
			}
		}()
		s.processJob(ctx, job.ID)
	})
	if err != nil {
		s.recordLibraryDiscoveryFailure(id, err)
	}
}

func (s *Server) recordLibraryDiscoveryFailure(id string, cause error) {
	// Source text and account errors may contain credentials; redact before
	// persisting an error intended for the management UI.
	message := redactURLError(cause, "[上游地址]").Error()
	if err := s.library.fail(id, message); err != nil && !errors.Is(err, errLibraryNotFound) {
		s.logJob(LogError, "", "保存文件库检查结果失败", err.Error())
	}
}

func (s *Server) completeLibraryInspection(jobID string, account AccountRuntime, items []DownloadItem) error {
	if len(items) == 0 {
		return &librarySourceError{"资源中没有可下载的文件"}
	}
	if len(items) > 10000 {
		return &librarySourceError{"资源文件超过 10000 个，请拆分资源后添加"}
	}
	// Discovery has no download result and incurs no download traffic charge.
	if err := s.cleanupTempResourcesBestEffort(jobID, account, "library inspection completed", true); err != nil {
		var persistenceErr *jobPersistenceError
		if errors.As(err, &persistenceErr) {
			return err
		}
	}
	_, err := s.jobs.update(jobID, func(job *Job) error {
		job.Items = append([]DownloadItem(nil), items...)
		job.Status, job.Stage, job.Message = JobCompleted, StageComplete, "file list ready"
		job.Error = ""
		return nil
	})
	return wrapJobPersistenceError("persist library inspection", err)
}

func (s *Server) processLibraryShare(ctx context.Context, jobID string, account AccountRuntime) error {
	job := mustJob(s.jobs.get(jobID))
	if job.Share == nil {
		return &librarySourceError{"分享链接无效"}
	}
	parent := shareInitialParentID(job.Share)
	info, err := account.Client.GetShareInfo(ctx, job.Share.ShareID, job.PassCode, parent)
	scoped := parent != ""
	if parent != "" && shouldFallbackTailShareScope(info, err) {
		info, err = account.Client.GetShareInfo(ctx, job.Share.ShareID, job.PassCode, "")
		scoped = false
	}
	if err != nil {
		return err
	}
	if info == nil {
		return &librarySourceError{"分享链接未返回文件"}
	}
	var items []DownloadItem
	if parent != "" && !scoped {
		var item DownloadItem
		var found bool
		item, found, err = s.findShareFileByID(ctx, account, job.Share.ShareID, info.PassCodeToken, info.Files, parent, "")
		if err == nil && !found {
			return &librarySourceError{"分享链接中的目标文件已丢失"}
		}
		items = []DownloadItem{item}
	} else {
		items, err = s.collectShareItems(ctx, account, job.Share.ShareID, info.PassCodeToken, info.Files, "")
	}
	if err != nil {
		return err
	}
	if job.LibraryDiscovery {
		return s.completeLibraryInspection(jobID, account, items)
	}
	item, err := matchLibraryFile(items, *job.LibraryTarget)
	if err != nil {
		return err
	}
	_, err = s.jobs.update(jobID, func(current *Job) error {
		current.Share.PassCodeToken = info.PassCodeToken
		current.Share.SelectedIDs = []string{item.ID}
		current.Share.SelectedItems = []DownloadItem{item}
		current.ResolveSelected = true
		return nil
	})
	if err != nil {
		return wrapJobPersistenceError("persist library file selection", err)
	}
	return s.processShare(ctx, jobID, account)
}

type libraryDownload struct {
	done    chan struct{}
	jobID   string
	expires time.Time
}

const maxLibraryDownloadCache = 512

func (s *Server) libraryDownloadJob(entry *LibraryEntry, file *LibraryFile, mode, baseURL, token string) (*libraryDownload, error) {
	s.libraryDownloadsMu.Lock()
	defer s.libraryDownloadsMu.Unlock()
	if s.libraryDownloads == nil {
		s.libraryDownloads = make(map[string]*libraryDownload)
	}
	now := s.now()
	for key, call := range s.libraryDownloads {
		select {
		case <-call.done:
			if !now.Before(call.expires) {
				delete(s.libraryDownloads, key)
			}
		default:
		}
	}
	if call := s.libraryDownloads[token]; call != nil {
		// Job details are separately retained for a bounded period.
		if _, ok := s.jobs.get(call.jobID); ok {
			return call, nil
		}
		delete(s.libraryDownloads, token)
	}
	if len(s.libraryDownloads) >= maxLibraryDownloadCache {
		return nil, errResolveQueueFull
	}
	target := file.Item
	job, err := s.newLibraryJob(entry, &target, mode, baseURL)
	if err != nil {
		return nil, err
	}
	call := &libraryDownload{done: make(chan struct{}), jobID: job.ID}
	s.libraryDownloads[token] = call
	err = s.resolver.enqueue(job.ID, priorityUser, func(ctx context.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.failJob(job.ID, fmt.Errorf("文件下载解析异常：%v", recovered))
			}
			s.libraryDownloadsMu.Lock()
			defer s.libraryDownloadsMu.Unlock()
			call.expires = s.now().Add(5 * time.Second)
			if finished, ok := s.jobs.get(job.ID); ok && finished.Status == JobCompleted && finished.Result != nil {
				call.expires = s.now().Add(2 * time.Minute)
				if expiry, err := time.Parse(time.RFC3339, finished.Result.ExpiresAt); err == nil && expiry.Add(-30*time.Second).Before(call.expires) {
					call.expires = expiry.Add(-30 * time.Second)
				}
			}
			close(call.done)
		}()
		s.processJob(ctx, job.ID)
	})
	if err != nil {
		delete(s.libraryDownloads, token)
		return nil, err
	}
	return call, nil
}

func (s *Server) handleLibraryDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	token := r.PathValue("token")
	if len(token) != 64 {
		writeError(w, http.StatusNotFound, "下载链接不存在或已撤销")
		return
	}
	entry, file, mode, err := s.library.lookupToken(token)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errLibraryNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, "下载链接不存在或暂不可用")
		return
	}
	// Relative proxy addresses keep a public request's Host/forwarded headers
	// out of the shared cached job.
	call, err := s.libraryDownloadJob(entry, file, mode, "", token)
	if err != nil {
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusServiceUnavailable, "解析服务暂不可用，请稍后重试")
		return
	}
	timer := time.NewTimer(2 * time.Minute)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return
	case <-s.resolver.ctx.Done():
		writeError(w, http.StatusServiceUnavailable, "解析服务正在关闭")
		return
	case <-timer.C:
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusServiceUnavailable, "文件仍在解析，请稍后重试此链接")
		return
	case <-call.done:
	}
	// A deletion, revocation or mode change during resolution takes effect before
	// a usable address is returned to a waiting downloader.
	_, _, currentMode, err := s.library.lookupToken(token)
	if err != nil || currentMode != mode {
		writeError(w, http.StatusNotFound, "下载链接不存在或已撤销")
		return
	}
	job, ok := s.jobs.get(call.jobID)
	if !ok || job.Status != JobCompleted || job.Result == nil {
		w.Header().Set("Retry-After", "10")
		writeError(w, http.StatusBadGateway, "文件解析失败，请稍后重试或联系管理员")
		return
	}
	result := job.Result
	downloadURL := result.DirectURL
	if mode == "proxy" {
		downloadURL = result.ProxyURL
	}
	if strings.TrimSpace(downloadURL) == "" {
		writeError(w, http.StatusBadGateway, "未能获取下载地址")
		return
	}
	if r.URL.Query().Get("format") == "json" {
		if strings.HasPrefix(downloadURL, "/") {
			downloadURL = strings.TrimRight(s.baseURL(r), "/") + downloadURL
		}
		writeJSON(w, http.StatusOK, map[string]string{"url": downloadURL, "mode": mode,
			"name": result.File.Name, "size": result.File.Size, "expires_at": result.ExpiresAt})
		return
	}
	http.Redirect(w, r, downloadURL, http.StatusFound)
}
