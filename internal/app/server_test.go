package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"pikpak2directlink/internal/pikpak"
)

type fakeRestoredLister struct {
	filesByParent map[string][]pikpak.FileEntry
	calls         []string
}

func (f *fakeRestoredLister) ListFiles(ctx context.Context, parentID string) ([]pikpak.FileEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.calls = append(f.calls, parentID)
	files, ok := f.filesByParent[parentID]
	if !ok {
		return nil, fmt.Errorf("unexpected ListFiles(%s)", parentID)
	}
	return append([]pikpak.FileEntry(nil), files...), nil
}

func TestBuildContentDispositionIncludesFallbackFilename(t *testing.T) {
	t.Parallel()

	header := buildContentDisposition("视频文件.mp4")
	if !strings.Contains(header, `filename="____.mp4"`) {
		t.Fatalf("expected ASCII fallback filename, got %q", header)
	}
	if !strings.Contains(header, "filename*=UTF-8''%E8%A7%86%E9%A2%91%E6%96%87%E4%BB%B6.mp4") {
		t.Fatalf("expected UTF-8 filename, got %q", header)
	}
}

func TestProxyErrorsDoNotExposeInternalDetails(t *testing.T) {
	t.Parallel()

	const leakedAccount = "alice@example.com"
	s := &Server{
		jobs: newJobStore(10),
		logs: newLogStore(10),
	}
	s.jobs.create(&Job{
		ID:     "job-proxy",
		Status: JobCompleted,
		Result: &JobResult{
			File:       DownloadItem{ID: "file-1", Name: "file.bin"},
			DirectURL:  "http://example.com/\naccount=" + leakedAccount,
			ProxyToken: "tok",
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/proxy/job-proxy?token=tok", nil)
	req.SetPathValue("id", "job-proxy")
	rec := httptest.NewRecorder()

	s.handleProxy(rec, req)

	body := rec.Body.String()
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected bad gateway, got %d with body %s", rec.Code, body)
	}
	if !strings.Contains(body, proxyDownloadFailedError) {
		t.Fatalf("expected generic proxy error, got %s", body)
	}
	if strings.Contains(body, leakedAccount) {
		t.Fatalf("proxy response leaked account detail: %s", body)
	}

	logs := s.logs.list(0)
	if len(logs) != 1 {
		t.Fatalf("expected one internal log entry, got %d", len(logs))
	}
	if len(logs[0].Details) == 0 || !strings.Contains(logs[0].Details[0], leakedAccount) {
		t.Fatalf("expected internal log to retain the diagnostic detail, got %+v", logs[0])
	}
}

func TestParseContentRangeSize(t *testing.T) {
	cases := []struct {
		header string
		want   int64
		ok     bool
	}{
		{header: "bytes 0-0/12345", want: 12345, ok: true},
		{header: "bytes 10-20/12345", want: 12345, ok: true},
		{header: "bytes 0-0/*", ok: false},
		{header: "items 0-0/12345", ok: false},
		{header: "bytes 0-0/not-a-number", ok: false},
		{header: "", ok: false},
	}
	for _, tc := range cases {
		got, ok := parseContentRangeSize(tc.header)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("parseContentRangeSize(%q) = (%d, %v), want (%d, %v)", tc.header, got, ok, tc.want, tc.ok)
		}
	}
}

func TestProxyMultipartServesFullDownloadWithUpstreamRanges(t *testing.T) {
	payload := []byte("abcdefghijklmnopqrstuvwxyz")
	var mu sync.Mutex
	var seen []string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		mu.Lock()
		seen = append(seen, rangeHeader)
		mu.Unlock()

		if rangeHeader == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		start, end, ok := parseTestRange(rangeHeader, int64(len(payload)))
		if !ok {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer upstream.Close()

	s := &Server{logs: newLogStore(10)}
	req := httptest.NewRequest(http.MethodGet, "/proxy/job-proxy?token=tok", nil)
	rec := httptest.NewRecorder()

	served := s.serveProxyMultipart(rec, req, upstream.URL, &JobResult{File: DownloadItem{Name: "file.bin"}}, "job-proxy", proxyMultipartConfig{
		Concurrency: 3,
		ChunkSize:   5,
		MinSize:     4,
	})

	if !served {
		t.Fatal("expected multipart proxy to serve the response")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, payload) {
		t.Fatalf("body = %q, want %q", got, payload)
	}
	if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(payload)) {
		t.Fatalf("Content-Length = %q, want %d", got, len(payload))
	}
	if got := rec.Header().Get("Content-Range"); got != "" {
		t.Fatalf("Content-Range should not be sent for full response, got %q", got)
	}

	mu.Lock()
	gotRanges := append([]string(nil), seen...)
	mu.Unlock()
	wantRanges := []string{"bytes=0-0", "bytes=0-4", "bytes=5-9", "bytes=10-14", "bytes=15-19", "bytes=20-24", "bytes=25-25"}
	if !seenAllRanges(gotRanges, wantRanges) {
		t.Fatalf("upstream ranges = %v, want at least %v", gotRanges, wantRanges)
	}
}

func TestProxyRangeRequestUsesSingleUpstreamRange(t *testing.T) {
	payload := []byte("abcdefghijklmnopqrstuvwxyz")
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Range"))
		start, end, ok := parseTestRange(r.Header.Get("Range"), int64(len(payload)))
		if !ok {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer upstream.Close()

	s := &Server{jobs: newJobStore(10), logs: newLogStore(10)}
	s.jobs.create(&Job{
		ID:     "job-proxy",
		Status: JobCompleted,
		Result: &JobResult{
			File:       DownloadItem{ID: "file-1", Name: "file.bin"},
			DirectURL:  upstream.URL,
			ProxyToken: "tok",
		},
	})
	req := httptest.NewRequest(http.MethodGet, "/proxy/job-proxy?token=tok", nil)
	req.Header.Set("Range", "bytes=3-8")
	req.SetPathValue("id", "job-proxy")
	rec := httptest.NewRecorder()

	s.handleProxy(rec, req)

	if rec.Code != http.StatusPartialContent {
		t.Fatalf("status = %d, body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != string(payload[3:9]) {
		t.Fatalf("body = %q, want %q", got, payload[3:9])
	}
	if len(seen) != 1 || seen[0] != "bytes=3-8" {
		t.Fatalf("upstream ranges = %v, want only client range", seen)
	}
}

func TestProxyMultipartFallsBackWhenUpstreamIgnoresRange(t *testing.T) {
	payload := []byte("small fallback payload")
	var seen []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Range"))
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer upstream.Close()

	s := &Server{logs: newLogStore(10)}
	req := httptest.NewRequest(http.MethodGet, "/proxy/job-proxy?token=tok", nil)
	rec := httptest.NewRecorder()
	served := s.serveProxyMultipart(rec, req, upstream.URL, &JobResult{File: DownloadItem{Name: "file.bin"}}, "job-proxy", proxyMultipartConfig{
		Concurrency: 2,
		ChunkSize:   4,
		MinSize:     4,
	})
	if served {
		t.Fatal("multipart path should not serve when upstream ignores Range")
	}

	rec = httptest.NewRecorder()
	s.serveProxySingleStream(rec, req, upstream.URL, &JobResult{File: DownloadItem{Name: "file.bin"}}, "job-proxy")
	if rec.Code != http.StatusOK || rec.Body.String() != string(payload) {
		t.Fatalf("fallback status/body = %d/%q", rec.Code, rec.Body.String())
	}
	if len(seen) != 2 || seen[0] != "bytes=0-0" || seen[1] != "" {
		t.Fatalf("upstream ranges = %v, want probe then plain fallback", seen)
	}
}

func TestProxyMultipartRetriesShortRangeRead(t *testing.T) {
	payload := []byte("abcdefghijklmnopqrstuvwxyz")
	var mu sync.Mutex
	attempts := map[string]int{}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		start, end, ok := parseTestRange(rangeHeader, int64(len(payload)))
		if !ok {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}

		mu.Lock()
		attempts[rangeHeader]++
		attempt := attempts[rangeHeader]
		mu.Unlock()

		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		if rangeHeader == "bytes=5-9" && attempt == 1 {
			_, _ = w.Write(payload[start : start+2])
			return
		}
		_, _ = w.Write(payload[start : end+1])
	}))
	defer upstream.Close()

	s := &Server{logs: newLogStore(10)}
	req := httptest.NewRequest(http.MethodGet, "/proxy/job-proxy?token=tok", nil)
	rec := httptest.NewRecorder()
	served := s.serveProxyMultipart(rec, req, upstream.URL, &JobResult{File: DownloadItem{Name: "file.bin"}}, "job-proxy", proxyMultipartConfig{
		Concurrency:    2,
		ChunkSize:      5,
		MinSize:        4,
		MaxAttempts:    2,
		RetryBaseDelay: time.Millisecond,
		WindowChunks:   4,
	})

	if !served {
		t.Fatal("expected multipart proxy to recover and serve the response")
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, payload) {
		t.Fatalf("body = %q, want %q", got, payload)
	}
	mu.Lock()
	gotAttempts := attempts["bytes=5-9"]
	mu.Unlock()
	if gotAttempts < 2 {
		t.Fatalf("range bytes=5-9 attempts = %d, want retry", gotAttempts)
	}
	if !logsContain(s.logs.list(0), "proxy multipart range recovered after retry", "range=bytes=5-9") {
		t.Fatalf("expected retry recovery log, got %+v", s.logs.list(0))
	}
}

func TestProxyMultipartFallsBackWhenFirstRangeKeepsFailing(t *testing.T) {
	payload := []byte("abcdefghijklmnopqrstuvwxyz")
	var mu sync.Mutex
	attempts := map[string]int{}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		start, end, ok := parseTestRange(rangeHeader, int64(len(payload)))
		if !ok {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		mu.Lock()
		attempts[rangeHeader]++
		mu.Unlock()
		if rangeHeader == "bytes=0-4" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeTestRangeResponse(w, payload, start, end)
	}))
	defer upstream.Close()

	s := &Server{logs: newLogStore(10)}
	req := httptest.NewRequest(http.MethodGet, "/proxy/job-proxy?token=tok", nil)
	rec := httptest.NewRecorder()
	served := s.serveProxyMultipart(rec, req, upstream.URL, &JobResult{File: DownloadItem{Name: "file.bin"}}, "job-proxy", proxyMultipartConfig{
		Concurrency:    2,
		ChunkSize:      5,
		MinSize:        4,
		MaxAttempts:    2,
		RetryBaseDelay: time.Millisecond,
		WindowChunks:   4,
	})

	if served {
		t.Fatal("multipart should decline before writing headers so caller can fall back")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body length = %d, want 0 before fallback", rec.Body.Len())
	}
	mu.Lock()
	gotAttempts := attempts["bytes=0-4"]
	mu.Unlock()
	if gotAttempts != 2 {
		t.Fatalf("range bytes=0-4 attempts = %d, want 2", gotAttempts)
	}
	if !logsContain(s.logs.list(0), "proxy multipart range failed before fallback", "range=bytes=0-4") {
		t.Fatalf("expected fallback diagnostic log, got %+v", s.logs.list(0))
	}
}

func TestProxyMultipartLogsFailureAfterPartialWrite(t *testing.T) {
	payload := []byte("abcdefghijklmnopqrstuvwxyz")
	var mu sync.Mutex
	attempts := map[string]int{}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		start, end, ok := parseTestRange(rangeHeader, int64(len(payload)))
		if !ok {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		mu.Lock()
		attempts[rangeHeader]++
		mu.Unlock()

		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
		w.WriteHeader(http.StatusPartialContent)
		if rangeHeader == "bytes=5-9" {
			_, _ = w.Write(payload[start : start+2])
			return
		}
		_, _ = w.Write(payload[start : end+1])
	}))
	defer upstream.Close()

	s := &Server{logs: newLogStore(10)}
	req := httptest.NewRequest(http.MethodGet, "/proxy/job-proxy?token=tok", nil)
	rec := httptest.NewRecorder()
	served := s.serveProxyMultipart(rec, req, upstream.URL, &JobResult{File: DownloadItem{Name: "file.bin"}}, "job-proxy", proxyMultipartConfig{
		Concurrency:    1,
		ChunkSize:      5,
		MinSize:        4,
		MaxAttempts:    2,
		RetryBaseDelay: time.Millisecond,
		WindowChunks:   2,
	})

	if !served {
		t.Fatal("multipart should report it handled the already-started response")
	}
	if got, want := rec.Body.String(), string(payload[:5]); got != want {
		t.Fatalf("body = %q, want only first chunk %q", got, want)
	}
	if !logsContain(s.logs.list(0), "proxy multipart download interrupted", "range=bytes=5-9") {
		t.Fatalf("expected interrupted log with range detail, got %+v", s.logs.list(0))
	}
}

func TestProxyMultipartLimitsPrefetchWindow(t *testing.T) {
	payload := []byte("abcdefghijklmnopqrstuvwxyz")
	releaseFirst := make(chan struct{})
	bodyRequestsChanged := make(chan struct{}, 8)
	var mu sync.Mutex
	bodyRequests := 0

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		start, end, ok := parseTestRange(rangeHeader, int64(len(payload)))
		if !ok {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}

		if rangeHeader != "bytes=0-0" {
			mu.Lock()
			bodyRequests++
			mu.Unlock()
			select {
			case bodyRequestsChanged <- struct{}{}:
			default:
			}
		}

		if rangeHeader == "bytes=0-4" {
			<-releaseFirst
		}
		writeTestRangeResponse(w, payload, start, end)
	}))
	defer upstream.Close()

	s := &Server{logs: newLogStore(10)}
	req := httptest.NewRequest(http.MethodGet, "/proxy/job-proxy?token=tok", nil)
	rec := httptest.NewRecorder()
	done := make(chan bool, 1)
	go func() {
		done <- s.serveProxyMultipart(rec, req, upstream.URL, &JobResult{File: DownloadItem{Name: "file.bin"}}, "job-proxy", proxyMultipartConfig{
			Concurrency:    4,
			ChunkSize:      5,
			MinSize:        4,
			MaxAttempts:    1,
			RetryBaseDelay: time.Millisecond,
			WindowChunks:   2,
		})
	}()

	waitForTestCondition(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return bodyRequests >= 2
	}, bodyRequestsChanged)
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	gotBeforeRelease := bodyRequests
	mu.Unlock()
	if gotBeforeRelease != 2 {
		close(releaseFirst)
		t.Fatalf("body range requests before first chunk released = %d, want 2", gotBeforeRelease)
	}

	close(releaseFirst)
	select {
	case served := <-done:
		if !served {
			t.Fatal("expected multipart proxy to serve after releasing first chunk")
		}
	case <-time.After(time.Second):
		t.Fatal("multipart proxy did not finish")
	}
	if got := rec.Body.Bytes(); !bytes.Equal(got, payload) {
		t.Fatalf("body = %q, want %q", got, payload)
	}
}

func TestProxySingleStreamLogsUpstreamReadError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("short"))
	}))
	defer upstream.Close()

	s := &Server{logs: newLogStore(10)}
	req := httptest.NewRequest(http.MethodGet, "/proxy/job-proxy?token=tok", nil)
	rec := httptest.NewRecorder()
	s.serveProxySingleStream(rec, req, upstream.URL, &JobResult{File: DownloadItem{Name: "file.bin"}}, "job-proxy")

	if got := rec.Body.String(); got != "short" {
		t.Fatalf("body = %q, want short", got)
	}
	if !logsContain(s.logs.list(0), "proxy single stream interrupted", "bytes=5") {
		t.Fatalf("expected single-stream interruption log, got %+v", s.logs.list(0))
	}
}

func parseTestRange(header string, size int64) (int64, int64, bool) {
	if !strings.HasPrefix(header, "bytes=") {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(header, "bytes="), "-")
	if len(parts) != 2 {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if start < 0 || end < start || end >= size {
		return 0, 0, false
	}
	return start, end, true
}

func writeTestRangeResponse(w http.ResponseWriter, payload []byte, start, end int64) {
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
	w.Header().Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(payload[start : end+1])
}

func seenAllRanges(got, want []string) bool {
	counts := make(map[string]int, len(got))
	for _, value := range got {
		counts[value]++
	}
	for _, value := range want {
		if counts[value] == 0 {
			return false
		}
		counts[value]--
	}
	return true
}

func logsContain(entries []LogEntry, message, detail string) bool {
	for _, entry := range entries {
		if entry.Message != message {
			continue
		}
		if detail == "" {
			return true
		}
		for _, got := range entry.Details {
			if strings.Contains(got, detail) {
				return true
			}
		}
	}
	return false
}

func waitForTestCondition(t *testing.T, timeout time.Duration, ok func() bool, changed <-chan struct{}) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		if ok() {
			return
		}
		select {
		case <-changed:
		case <-deadline.C:
			t.Fatal("timed out waiting for test condition")
		}
	}
}

func TestRestoreFileIDsDedupesResponseIDs(t *testing.T) {
	t.Parallel()

	got := restoreFileIDs(&pikpak.RestoreShareResponse{
		FileID: " root ",
		TaskInfo: []pikpak.RestoreTaskInfo{
			{FileID: "child-a"},
			{FileID: "root"},
			{FileID: ""},
			{FileID: " child-b "},
		},
	})
	want := []string{"root", "child-a", "child-b"}
	if len(got) != len(want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids = %v, want %v", got, want)
		}
	}
}

func TestDownloadItemIDExists(t *testing.T) {
	t.Parallel()

	items := []DownloadItem{{ID: "file-a"}, {ID: "file-b"}}
	if !downloadItemIDExists(items, "file-b") {
		t.Fatal("expected file-b to be found")
	}
	if downloadItemIDExists(items, "missing-tail-id") {
		t.Fatal("unexpectedly trusted a tail id not present in the collected share items")
	}
	if downloadItemIDExists(items, " ") {
		t.Fatal("blank id should not match")
	}
}

func TestShareInitialParentIDUsesTailOnlyBeforeSelection(t *testing.T) {
	t.Parallel()

	share := &ShareState{ShareID: "share-id", TailID: "folder-id"}
	if got := shareInitialParentID(share); got != "folder-id" {
		t.Fatalf("initial parent id = %q, want folder-id", got)
	}

	share.SelectedIDs = []string{"file-id"}
	if got := shareInitialParentID(share); got != "" {
		t.Fatalf("selected share should not use tail as parent id, got %q", got)
	}
}

func TestDecideShareSourceItemsTailFolderRequiresSelection(t *testing.T) {
	t.Parallel()

	job := &Job{
		Kind:  ResourceShare,
		Share: &ShareState{ShareID: "share-id", TailID: "folder-id"},
	}
	items := []DownloadItem{
		{ID: "a", Name: "a.mp4"},
		{ID: "b", Name: "b.mp4"},
	}

	decision, err := decideShareSourceItems(job, items, true)
	if err != nil {
		t.Fatalf("decideShareSourceItems: %v", err)
	}
	if len(decision.SelectedIDs) != 0 {
		t.Fatalf("selected IDs = %v, want none before user selection", decision.SelectedIDs)
	}
	if len(decision.SelectionItems) != len(items) {
		t.Fatalf("selection items = %v, want %v", decision.SelectionItems, items)
	}

	job.ResolveAll = true
	if _, err := decideShareSourceItems(job, items, true); err == nil {
		t.Fatal("batch share folder should require explicit selection instead of resolving every file")
	}
}

func TestResolveRestoredSelectedItemsLocatesNestedFileByPath(t *testing.T) {
	t.Parallel()

	lister := &fakeRestoredLister{
		filesByParent: map[string][]pikpak.FileEntry{
			"root": {
				{ID: "target-folder", Name: "Target", Kind: "drive#folder"},
				{ID: "unrelated-folder", Name: "Unrelated", Kind: "drive#folder"},
			},
			"target-folder": {
				{ID: "restored-file", Name: "episode.mkv", Kind: "drive#file", Size: "123"},
			},
		},
	}
	restored := []pikpak.FileEntry{{ID: "root", Name: "Show", Kind: "drive#folder"}}
	selected := []DownloadItem{{ID: "share-file", Name: "episode.mkv", Path: "Show/Target/episode.mkv", Size: "123"}}

	items, err := resolveRestoredSelectedItems(context.Background(), lister, restored, selected)
	if err != nil {
		t.Fatalf("resolveRestoredSelectedItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("resolved item count = %d, want 1", len(items))
	}
	if items[0].ID != "restored-file" || items[0].Path != "Show/Target/episode.mkv" {
		t.Fatalf("resolved item = %+v, want restored-file with original path", items[0])
	}
	if got := strings.Join(lister.calls, ","); got != "root,target-folder" {
		t.Fatalf("ListFiles calls = %s, want root,target-folder", got)
	}
}

func TestResolveRestoredSelectedItemsStopsOnCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	lister := &fakeRestoredLister{
		filesByParent: map[string][]pikpak.FileEntry{
			"root": {{ID: "file", Name: "file.mkv", Kind: "drive#file"}},
		},
	}
	restored := []pikpak.FileEntry{{ID: "root", Name: "Root", Kind: "drive#folder"}}
	selected := []DownloadItem{{ID: "share-file", Name: "file.mkv", Path: "Root/file.mkv"}}

	_, err := resolveRestoredSelectedItems(ctx, lister, restored, selected)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if len(lister.calls) != 0 {
		t.Fatalf("canceled context should not list files, got calls %v", lister.calls)
	}
}

func TestWaitForShareDirectURLsStopsOnCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := &Server{
		config: Config{
			ShareURLTimeout:   time.Second,
			SharePollInterval: time.Millisecond,
		},
		logs: newLogStore(10),
	}
	_, err := s.waitForShareDirectURLs(ctx, "job-canceled", AccountRuntime{}, []DownloadItem{
		{ID: "a", Name: "a.mp4"},
		{ID: "b", Name: "b.mp4"},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}

	for _, entry := range s.logs.list(0) {
		if entry.Level == LogWarn {
			t.Fatalf("canceled context should not emit per-file warnings, got %+v", entry)
		}
		if strings.Contains(entry.Message, "a.mp4") || strings.Contains(entry.Message, "b.mp4") {
			t.Fatalf("canceled context logged a file-specific message: %+v", entry)
		}
	}
}

func TestFinishWithItemsLargeShareSelectionRequiresNarrowing(t *testing.T) {
	t.Parallel()

	s := &Server{
		jobs: newJobStore(10),
		logs: newLogStore(10),
	}
	s.jobs.create(&Job{
		ID:              "job-share-large",
		Kind:            ResourceShare,
		Status:          JobRunning,
		Stage:           StageTransfer,
		ResolveSelected: true,
	})

	items := make([]DownloadItem, maxSelectedFilesPerResolve+1)
	for i := range items {
		items[i] = DownloadItem{ID: "file-" + strconv.Itoa(i), Name: "secret-name.mkv"}
	}

	if err := s.finishWithItems(context.Background(), "job-share-large", AccountRuntime{ID: "acct"}, items); err != nil {
		t.Fatalf("finishWithItems: %v", err)
	}

	job, _ := s.jobs.get("job-share-large")
	if job.Status != JobSelectionRequired || job.Stage != StageResultSelection {
		t.Fatalf("job status/stage = %s/%s, want selection_required/result_selection", job.Status, job.Stage)
	}
	if len(job.Items) != len(items) {
		t.Fatalf("selection item count = %d, want %d", len(job.Items), len(items))
	}
	for _, entry := range s.logs.list(0) {
		if strings.Contains(strings.Join(entry.Details, " "), "secret-name.mkv") {
			t.Fatalf("log leaked sample filename: %+v", entry)
		}
	}
}

func TestSampleItemDetailDoesNotExposeFilenames(t *testing.T) {
	t.Parallel()

	if got := sampleItemDetail([]DownloadItem{{Name: "private-video.mkv", Path: "folder/private-video.mkv"}}); got != "" {
		t.Fatalf("sample detail = %q, want empty", got)
	}
}
