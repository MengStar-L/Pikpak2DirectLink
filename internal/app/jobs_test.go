package app

import "testing"

func TestDetectResourceKind(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		kind  ResourceKind
		ok    bool
	}{
		{name: "magnet", input: "magnet:?xt=urn:btih:abc", kind: ResourceMagnet, ok: true},
		{name: "share", input: "https://mypikpak.com/s/VO8BcRb-0fibD0Ncymp8nxSMo1", kind: ResourceShare, ok: true},
		{name: "invalid", input: "https://example.com/file.zip", ok: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			kind, err := detectResourceKind(tt.input)
			if tt.ok && err != nil {
				t.Fatalf("expected success, got error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatalf("expected error, got kind %q", kind)
			}
			if tt.ok && kind != tt.kind {
				t.Fatalf("expected kind %q, got %q", tt.kind, kind)
			}
		})
	}
}

func TestSplitResourceLinesPerLineDetect(t *testing.T) {
	t.Parallel()

	input := "magnet:?xt=urn:btih:abc\nhttps://mypikpak.com/s/VO8BcRb-0fibD0Ncymp8nxSMo1\n\n  magnet:?xt=urn:btih:def  "
	lines := splitResourceLines(input)
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %v", len(lines), lines)
	}

	wantKinds := []ResourceKind{ResourceMagnet, ResourceShare, ResourceMagnet}
	for i, line := range lines {
		kind, err := detectResourceKind(line)
		if err != nil {
			t.Fatalf("line %d (%q): unexpected error %v", i, line, err)
		}
		if kind != wantKinds[i] {
			t.Fatalf("line %d (%q): kind %q, want %q", i, line, kind, wantKinds[i])
		}
	}
}

func TestSplitResourceLinesRejectsBadLine(t *testing.T) {
	t.Parallel()

	lines := splitResourceLines("magnet:?xt=urn:btih:abc\nhttps://example.com/file.zip")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if _, err := detectResourceKind(lines[1]); err == nil {
		t.Fatal("expected error for unrecognized line")
	}
}

func TestSplitResourceLineSpecsPreservesRawSharePassCode(t *testing.T) {
	t.Parallel()

	specs := splitResourceLineSpecs(" https://mypikpak.com/s/SHAREID?act=play&pwd=abcd#frag ")
	if len(specs) != 1 {
		t.Fatalf("specs = %+v, want one", specs)
	}
	if specs[0].clean != "https://mypikpak.com/s/SHAREID" {
		t.Fatalf("clean = %q", specs[0].clean)
	}
	share, passCode, err := shareStateAndPassCode(specs[0].raw, "")
	if err != nil {
		t.Fatalf("shareStateAndPassCode: %v", err)
	}
	if share.ShareID != "SHAREID" {
		t.Fatalf("share id = %q", share.ShareID)
	}
	if passCode != "abcd" {
		t.Fatalf("pass code = %q", passCode)
	}
}

func TestShareLinkWithPlayQueryKeepsTailOnlyAsParsedHint(t *testing.T) {
	t.Parallel()

	raw := "https://mypikpak.com/s/VOvcyQK1N_7s6UMZ3DrR7DsMo2/AAAAY_pRskUJZDb_c2vjzOMio2_VOv?act=play"
	specs := splitResourceLineSpecs(raw)
	if len(specs) != 1 {
		t.Fatalf("specs = %+v, want one", specs)
	}
	if specs[0].clean != "https://mypikpak.com/s/VOvcyQK1N_7s6UMZ3DrR7DsMo2/AAAAY_pRskUJZDb_c2vjzOMio2_VOv" {
		t.Fatalf("clean = %q", specs[0].clean)
	}
	share, passCode, err := shareStateAndPassCode(specs[0].raw, "")
	if err != nil {
		t.Fatalf("shareStateAndPassCode: %v", err)
	}
	if share.ShareID != "VOvcyQK1N_7s6UMZ3DrR7DsMo2" {
		t.Fatalf("share id = %q", share.ShareID)
	}
	if share.TailID != "AAAAY_pRskUJZDb_c2vjzOMio2_VOv" {
		t.Fatalf("tail id = %q", share.TailID)
	}
	if passCode != "" {
		t.Fatalf("pass code = %q", passCode)
	}
}

func TestPreferredResultURLFollowsMode(t *testing.T) {
	t.Parallel()

	if got := preferredResultURL("direct", "https://direct", "https://proxy"); got != "https://direct" {
		t.Fatalf("direct mode preferred URL = %q", got)
	}
	if got := preferredResultURL("proxy", "https://direct", "https://proxy"); got != "https://proxy" {
		t.Fatalf("proxy mode preferred URL = %q", got)
	}
	if got := preferredResultURL("proxy", "https://direct", ""); got != "https://direct" {
		t.Fatalf("proxy mode fallback URL = %q", got)
	}
	if got := preferredResultURL("direct", "", "https://proxy"); got != "https://proxy" {
		t.Fatalf("direct mode fallback URL = %q", got)
	}
}

func TestJobStoreDoesNotEvictActiveJobs(t *testing.T) {
	t.Parallel()

	store := newJobStore(2)
	store.create(&Job{ID: "queued", Status: JobQueued})
	store.create(&Job{ID: "running", Status: JobRunning})
	store.create(&Job{ID: "select", Status: JobSelectionRequired})

	for _, id := range []string{"queued", "running", "select"} {
		if _, ok := store.get(id); !ok {
			t.Fatalf("active job %q was evicted", id)
		}
	}

	store.create(&Job{ID: "done", Status: JobCompleted})
	if _, ok := store.get("done"); ok {
		t.Fatal("terminal overflow job should be evicted before active jobs")
	}
}

func TestParseShareLink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        string
		wantShareID  string
		wantTailID   string
		expectErrors bool
	}{
		{
			name:        "root share",
			input:       "https://mypikpak.com/s/VO8BcRb-0fibD0Ncymp8nxSMo1",
			wantShareID: "VO8BcRb-0fibD0Ncymp8nxSMo1",
		},
		{
			name:        "share with child id",
			input:       "https://mypikpak.com/s/VO8BcRb-0fibD0Ncymp8nxSMo1/VO8Ba45l-FRcCf559uZjwjFjo1",
			wantShareID: "VO8BcRb-0fibD0Ncymp8nxSMo1",
			wantTailID:  "VO8Ba45l-FRcCf559uZjwjFjo1",
		},
		{
			name:        "root share with official query",
			input:       "https://mypikpak.com/s/VO8BcRb-0fibD0Ncymp8nxSMo1?view=list&order=3",
			wantShareID: "VO8BcRb-0fibD0Ncymp8nxSMo1",
		},
		{
			name:        "share with child id and query",
			input:       "https://mypikpak.com/s/VO8BcRb-0fibD0Ncymp8nxSMo1/VO8Ba45l-FRcCf559uZjwjFjo1?pwd=abcd#files",
			wantShareID: "VO8BcRb-0fibD0Ncymp8nxSMo1",
			wantTailID:  "VO8Ba45l-FRcCf559uZjwjFjo1",
		},
		{
			name:        "scheme-less share with query",
			input:       "mypikpak.com/s/VO8BcRb-0fibD0Ncymp8nxSMo1?pwd=abcd",
			wantShareID: "VO8BcRb-0fibD0Ncymp8nxSMo1",
		},
		{
			name:         "invalid",
			input:        "https://mypikpak.com/",
			expectErrors: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			shareID, tailID, err := parseShareLink(tt.input)
			if tt.expectErrors {
				if err == nil {
					t.Fatalf("expected error, got shareID=%q tailID=%q", shareID, tailID)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if shareID != tt.wantShareID {
				t.Fatalf("expected shareID %q, got %q", tt.wantShareID, shareID)
			}
			if tailID != tt.wantTailID {
				t.Fatalf("expected tailID %q, got %q", tt.wantTailID, tailID)
			}
		})
	}
}

func TestShareStateAndPassCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		input           string
		defaultPassCode string
		wantShareID     string
		wantTailID      string
		wantPassCode    string
	}{
		{
			name:         "uses pwd from link",
			input:        "https://mypikpak.com/s/VO8BcRb-0fibD0Ncymp8nxSMo1?pwd=abcd",
			wantShareID:  "VO8BcRb-0fibD0Ncymp8nxSMo1",
			wantPassCode: "abcd",
		},
		{
			name:         "uses pass_code from link",
			input:        "https://mypikpak.com/s/VO8BcRb-0fibD0Ncymp8nxSMo1/VO8Ba45l-FRcCf559uZjwjFjo1?pass_code=efgh",
			wantShareID:  "VO8BcRb-0fibD0Ncymp8nxSMo1",
			wantTailID:   "VO8Ba45l-FRcCf559uZjwjFjo1",
			wantPassCode: "efgh",
		},
		{
			name:            "explicit pass code wins",
			input:           "https://mypikpak.com/s/VO8BcRb-0fibD0Ncymp8nxSMo1?pwd=from-link",
			defaultPassCode: "from-form",
			wantShareID:     "VO8BcRb-0fibD0Ncymp8nxSMo1",
			wantPassCode:    "from-form",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			share, passCode, err := shareStateAndPassCode(tt.input, tt.defaultPassCode)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if share.ShareID != tt.wantShareID {
				t.Fatalf("expected shareID %q, got %q", tt.wantShareID, share.ShareID)
			}
			if share.TailID != tt.wantTailID {
				t.Fatalf("expected tailID %q, got %q", tt.wantTailID, share.TailID)
			}
			if passCode != tt.wantPassCode {
				t.Fatalf("expected pass code %q, got %q", tt.wantPassCode, passCode)
			}
		})
	}
}

func TestCloneJobCopiesSelectedShareItems(t *testing.T) {
	t.Parallel()

	original := &Job{
		ID: "job-share",
		Share: &ShareState{
			ShareID:       "share",
			SelectedIDs:   []string{"share-file"},
			SelectedItems: []DownloadItem{{ID: "share-file", Name: "file.mkv", Path: "folder/file.mkv"}},
		},
	}

	cloned := cloneJob(original)
	original.Share.SelectedIDs[0] = "mutated-id"
	original.Share.SelectedItems[0].Path = "mutated/path.mkv"

	if cloned.Share.SelectedIDs[0] != "share-file" {
		t.Fatalf("cloned selected ids = %v, want share-file", cloned.Share.SelectedIDs)
	}
	if cloned.Share.SelectedItems[0].Path != "folder/file.mkv" {
		t.Fatalf("cloned selected item path = %q, want folder/file.mkv", cloned.Share.SelectedItems[0].Path)
	}
}
