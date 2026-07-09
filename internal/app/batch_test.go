package app

import (
	"strings"
	"testing"
)

func TestSplitResourceLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{name: "single", input: "magnet:?xt=urn:btih:a", want: []string{"magnet:?xt=urn:btih:a"}},
		{name: "empty", input: "", want: nil},
		{name: "blank only", input: "  \n\t\n", want: nil},
		{
			name:  "crlf and trim",
			input: "  magnet:?a \r\n\r\n  magnet:?b  \n",
			want:  []string{"magnet:?a", "magnet:?b"},
		},
		{
			name:  "dedupe preserves order",
			input: "magnet:?a\nmagnet:?b\nmagnet:?a",
			want:  []string{"magnet:?a", "magnet:?b"},
		},
		{
			name:  "dedupe after magnet cleanup",
			input: "magnet:?xt=urn:btih:abc&dn=Movie&xl=123\nmagnet:?xt=urn:btih:abc&dn=Movie&ws=http://example.com",
			want:  []string{"magnet:?xt=urn:btih:abc&dn=Movie"},
		},
		{
			name:  "clean share query and fragment",
			input: "https://mypikpak.com/s/SHAREID?act=play&pwd=1234#frag",
			want:  []string{"https://mypikpak.com/s/SHAREID"},
		},
		{
			name:  "dedupe after share cleanup",
			input: "https://mypikpak.com/s/SHAREID?act=play\nhttps://mypikpak.com/s/SHAREID#frag",
			want:  []string{"https://mypikpak.com/s/SHAREID"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := splitResourceLines(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestBatchLinkLabel(t *testing.T) {
	t.Parallel()

	if got := batchLinkLabel(1, "magnet:?xt=urn:btih:abc&dn=Cool+Movie", ResourceMagnet); got != "链接1 Cool Movie" {
		t.Fatalf("magnet with dn: got %q", got)
	}
	if got := batchLinkLabel(2, "magnet:?xt=urn:btih:abc", ResourceMagnet); got != "链接2" {
		t.Fatalf("magnet without dn: got %q", got)
	}
	if got := batchLinkLabel(3, "https://mypikpak.com/s/SHAREID", ResourceShare); got != "链接3 SHAREID" {
		t.Fatalf("share: got %q", got)
	}
}

func TestSanitizePathSegment(t *testing.T) {
	t.Parallel()

	if got := sanitizePathSegment("a/b\\c"); got != "a_b_c" {
		t.Fatalf("slash sanitize: got %q", got)
	}
	long := strings.Repeat("x", 80)
	if got := sanitizePathSegment(long); len(got) != 60 {
		t.Fatalf("length clamp: got len %d", len(got))
	}
}

func TestCreateBatchJobRejectsTooManyLinks(t *testing.T) {
	t.Parallel()

	lines := make([]resourceLineSpec, maxBatchLinks+1)
	for i := range lines {
		lines[i] = resourceLineSpec{raw: "magnet:?xt=urn:btih:test", clean: "magnet:?xt=urn:btih:test"}
	}
	s := &Server{}
	if _, status, _ := s.createBatchJob(lines, "direct", "", "", "", priorityAdmin, "https://host"); status != 400 {
		t.Fatalf("status = %d, want 400", status)
	}
}

// TestBatchChildDoneMerge drives three pre-seeded terminal children through the
// coordinator and asserts the parent's merged result set, path prefixes,
// parent-pointing proxy URLs, and "x/x" rollup. No PikPak/network is needed
// because the children are seeded as already-terminal jobs.
func TestBatchChildDoneMerge(t *testing.T) {
	t.Parallel()

	s := &Server{
		jobs:    newJobStore(50),
		batches: make(map[string]*batchState),
	}

	parentID := "parent01"
	s.jobs.create(&Job{
		ID:     parentID,
		Kind:   ResourceBatch,
		Status: JobRunning,
		Stage:  StageTransfer,
		Batch:  &BatchProgress{Total: 3},
	})
	bs := &batchState{parentID: parentID, baseURL: "https://host", total: 3}
	s.registerBatch(bs)

	// Two completed children with results, one failed child.
	s.jobs.create(&Job{
		ID:     "childA",
		Mode:   "proxy",
		Status: JobCompleted,
		Results: []JobResult{{
			File:       DownloadItem{Name: "a.mkv", Path: "a.mkv", Size: "100"},
			URL:        "https://host/proxy/childA?token=tokA",
			DirectURL:  "https://direct/a",
			ProxyToken: "tokA",
			ProxyURL:   "https://host/proxy/childA?token=tokA",
			AccountID:  "acct1",
		}},
	})
	s.jobs.create(&Job{
		ID:     "childB",
		Mode:   "direct",
		Status: JobCompleted,
		Results: []JobResult{{
			File:       DownloadItem{Name: "b.mp4", Path: "sub/b.mp4", Size: "200"},
			URL:        "https://direct/b",
			DirectURL:  "https://direct/b",
			ProxyToken: "tokB",
			ProxyURL:   "https://host/proxy/childB?token=tokB",
			AccountID:  "acct2",
		}},
	})
	s.jobs.create(&Job{ID: "childC", Status: JobFailed, Error: "boom"})

	s.batchChildDone(parentID, "childA", "链接1 A")
	s.batchChildDone(parentID, "childC", "链接3")
	s.batchChildDone(parentID, "childB", "链接2 B")

	parent, ok := s.jobs.get(parentID)
	if !ok {
		t.Fatal("parent job missing")
	}
	if parent.Status != JobCompleted {
		t.Fatalf("status = %q, want completed", parent.Status)
	}
	if parent.Batch == nil || parent.Batch.Total != 3 || parent.Batch.Succeeded != 2 || parent.Batch.Failed != 1 {
		t.Fatalf("batch rollup = %+v, want total3 ok2 fail1", parent.Batch)
	}
	if parent.Message != "解析成功 2/3 条" {
		t.Fatalf("message = %q", parent.Message)
	}
	if len(parent.Results) != 2 {
		t.Fatalf("results len = %d, want 2", len(parent.Results))
	}

	byPath := make(map[string]JobResult)
	for _, r := range parent.Results {
		byPath[r.File.Path] = r
	}
	ra, okA := byPath["链接1 A/a.mkv"]
	if !okA {
		t.Fatalf("missing prefixed path for A; got %v", keysOf(byPath))
	}
	if ra.ProxyURL != "https://host/proxy/parent01?token=tokA" {
		t.Fatalf("A proxy URL = %q, want parent-pointing", ra.ProxyURL)
	}
	if ra.URL != "https://host/proxy/parent01?token=tokA" {
		t.Fatalf("A preferred URL = %q, want parent-pointing proxy", ra.URL)
	}
	if ra.AccountID != "acct1" {
		t.Fatalf("A account id lost: %q", ra.AccountID)
	}
	rb, okB := byPath["链接2 B/sub/b.mp4"]
	if !okB {
		t.Fatalf("missing prefixed nested path for B; got %v", keysOf(byPath))
	}
	if rb.URL != "https://direct/b" {
		t.Fatalf("B preferred URL = %q, want direct", rb.URL)
	}

	// The batch state is removed once finalized.
	if s.batchByID(parentID) != nil {
		t.Fatal("batch state should be removed after finalize")
	}
}

// TestBatchChildDoneAllFailed asserts an all-failed batch fails the parent.
func TestBatchChildDoneAllFailed(t *testing.T) {
	t.Parallel()

	s := &Server{jobs: newJobStore(50), batches: make(map[string]*batchState)}
	parentID := "parentZ"
	s.jobs.create(&Job{ID: parentID, Kind: ResourceBatch, Status: JobRunning, Batch: &BatchProgress{Total: 2}})
	s.registerBatch(&batchState{parentID: parentID, baseURL: "https://h", total: 2})
	s.jobs.create(&Job{ID: "f1", Status: JobFailed, Error: "x"})
	s.jobs.create(&Job{ID: "f2", Status: JobFailed, Error: "y"})

	s.batchChildDone(parentID, "f1", "链接1")
	s.batchChildDone(parentID, "f2", "链接2")

	parent, _ := s.jobs.get(parentID)
	if parent.Status != JobFailed {
		t.Fatalf("status = %q, want failed", parent.Status)
	}
	if parent.Batch.Succeeded != 0 || parent.Batch.Failed != 2 {
		t.Fatalf("rollup = %+v", parent.Batch)
	}
}

func TestBatchChildDoneRecordsBadResourceFailure(t *testing.T) {
	t.Parallel()

	s := &Server{jobs: newJobStore(50), batches: make(map[string]*batchState)}
	parentID := "parentBad"
	s.jobs.create(&Job{ID: parentID, Kind: ResourceBatch, Status: JobRunning, Batch: &BatchProgress{Total: 2}})
	s.registerBatch(&batchState{parentID: parentID, baseURL: "https://h", total: 2})
	s.jobs.create(&Job{
		ID:     "ok",
		Status: JobCompleted,
		Results: []JobResult{{
			File:       DownloadItem{Name: "ok.mkv", Path: "ok.mkv"},
			DirectURL:  "https://direct/ok",
			ProxyToken: "tok",
		}},
	})
	s.jobs.create(&Job{ID: "bad", Status: JobFailed, Error: badResourceParseUserError})

	s.batchChildDone(parentID, "bad", "链接1 Bad")
	s.batchChildDone(parentID, "ok", "链接2 OK")

	parent, _ := s.jobs.get(parentID)
	if parent.Status != JobCompleted {
		t.Fatalf("status = %q, want completed", parent.Status)
	}
	if len(parent.Batch.Failures) != 1 {
		t.Fatalf("failures = %+v, want one", parent.Batch.Failures)
	}
	if parent.Batch.Failures[0].Label != "链接1 Bad" || parent.Batch.Failures[0].Error != badResourceParseUserError {
		t.Fatalf("failure entry = %+v", parent.Batch.Failures[0])
	}
}

func TestBatchChildDoneRecordsGenericFailure(t *testing.T) {
	t.Parallel()

	s := &Server{jobs: newJobStore(50), batches: make(map[string]*batchState)}
	parentID := "parentGeneric"
	s.jobs.create(&Job{ID: parentID, Kind: ResourceBatch, Status: JobRunning, Batch: &BatchProgress{Total: 1}})
	s.registerBatch(&batchState{parentID: parentID, baseURL: "https://h", total: 1})
	s.jobs.create(&Job{ID: "failed", Status: JobFailed, Error: "all PikPak accounts failed: alice@example.com: boom"})

	s.batchChildDone(parentID, "failed", "link 1")

	parent, _ := s.jobs.get(parentID)
	if parent.Batch == nil || len(parent.Batch.Failures) != 1 {
		t.Fatalf("failures = %+v, want one", parent.Batch)
	}
	failure := parent.Batch.Failures[0]
	if failure.Label != "link 1" {
		t.Fatalf("label = %q", failure.Label)
	}
	if failure.Error != genericUserJobError {
		t.Fatalf("error = %q, want generic user-safe error", failure.Error)
	}
	if strings.Contains(failure.Error, "@") || strings.Contains(failure.Error, "alice") {
		t.Fatalf("failure leaked account detail: %q", failure.Error)
	}
}

func keysOf(m map[string]JobResult) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
