package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
