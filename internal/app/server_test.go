package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"pikpak2directlink/internal/pikpak"
)

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
