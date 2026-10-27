package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"pikpak2directlink/internal/pikpak"
)

func TestMatchLibraryFileRequiresUniqueDurableSelector(t *testing.T) {
	target := DownloadItem{ID: "old-account-id", Name: "video.mp4", Path: "season-2/video.mp4", Size: "20"}
	items := []DownloadItem{
		{ID: "wrong-path", Name: target.Name, Path: "season-1/video.mp4", Size: target.Size},
		{ID: "new-account-id", Name: target.Name, Path: target.Path, Size: target.Size},
		{ID: "wrong-size", Name: target.Name, Path: target.Path, Size: "30"},
	}
	got, err := matchLibraryFile(items, target)
	if err != nil || got.ID != "new-account-id" {
		t.Fatalf("matched file = %#v, %v; want new account ID for same durable selector", got, err)
	}
	for name, candidates := range map[string][]DownloadItem{
		"missing":   items[:1],
		"changed":   items[2:],
		"ambiguous": {items[1], items[1]},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := matchLibraryFile(candidates, target)
			var sourceErr *librarySourceError
			if !errors.As(err, &sourceErr) {
				t.Fatalf("error = %v; want deterministic source error", err)
			}
		})
	}
}

func TestLibraryJobFieldsSurviveEncryptedPersistenceAndIndependentClone(t *testing.T) {
	store, db, now := newTestSQLJobStore(t)
	for _, discovery := range []bool{true, false} {
		job := &Job{ID: "library-target", Kind: ResourceShare, Mode: "direct", Input: "https://mypikpak.com/s/private-source",
			Status: JobQueued, Stage: StageTransfer, CreatedAt: now, UpdatedAt: now, LibraryDiscovery: discovery}
		if discovery {
			job.ID = "library-discovery"
		} else {
			job.LibraryTarget = &DownloadItem{ID: "transient", Name: "secret-name.mp4", Path: "private/secret-name.mp4", Size: "20"}
		}
		if err := store.create(job); err != nil {
			t.Fatal(err)
		}
		loaded, ok, err := store.get(job.ID, resolveJobOwnerAdmin, "", now)
		if err != nil || !ok || loaded.LibraryDiscovery != discovery || !reflect.DeepEqual(loaded.LibraryTarget, job.LibraryTarget) {
			t.Fatalf("library job persistence: job=%#v, ok=%v, err=%v", loaded, ok, err)
		}
		var encrypted string
		if err := db.QueryRow(`SELECT payload_encrypted FROM resolve_jobs WHERE id=?`, job.ID).Scan(&encrypted); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(encrypted, "private-source") || strings.Contains(encrypted, "secret-name") {
			t.Fatal("library job selector leaked outside encrypted payload")
		}
		if !discovery {
			cloned := cloneJob(loaded)
			cloned.LibraryTarget.Path = "changed"
			if loaded.LibraryTarget.Path != job.LibraryTarget.Path {
				t.Fatal("clone mutated original file selector")
			}
		}
	}
}

func TestLibraryHTTPAuthAndUnknownPublicToken(t *testing.T) {
	s := newTestServer(t)
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/api/library", http.StatusUnauthorized},
		{http.MethodPost, "/api/library", http.StatusUnauthorized},
		{http.MethodGet, "/d/short", http.StatusNotFound},
		{http.MethodGet, "/d/", http.StatusNotFound},
		{http.MethodGet, "/d/invalid/nested", http.StatusNotFound},
		{http.MethodHead, "/d/" + strings.Repeat("a", 64), http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.status {
			t.Fatalf("%s %s status=%d body=%s", tc.method, tc.path, rec.Code, rec.Body.String())
		}
	}
}

// The share has duplicate basenames in different folders, and its restored IDs
// differ from public-share IDs, as they do across actual PikPak accounts.
type libraryShareTestClient struct {
	*fakePikPakClient
	stateMu      sync.Mutex
	shareCalls   int
	restoreCalls int
	restored     []string
	block        <-chan struct{}
	entered      chan<- struct{}
	expiry       time.Time
	directURL    string
	sourceSize   string
}

func (f *libraryShareTestClient) GetShareInfo(ctx context.Context, _, _, _ string) (*pikpak.ShareListResponse, error) {
	f.stateMu.Lock()
	f.shareCalls++
	block, entered := f.block, f.entered
	f.stateMu.Unlock()
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &pikpak.ShareListResponse{PassCodeToken: "fresh-share-passcode", Files: []pikpak.FileEntry{
		{ID: "first-folder", Name: "first", Kind: "drive#folder"},
		{ID: "second-folder", Name: "second", Kind: "drive#folder"},
	}}, nil
}

func (f *libraryShareTestClient) GetShareFolder(_ context.Context, _, _, parent string) (*pikpak.ShareListResponse, error) {
	f.stateMu.Lock()
	size := f.sourceSize
	f.stateMu.Unlock()
	if size == "" {
		size = "20"
	}
	return &pikpak.ShareListResponse{Files: []pikpak.FileEntry{{ID: parent + "-file", Name: "video.mp4", Kind: "drive#file", Size: size}}}, nil
}

func (f *libraryShareTestClient) RestoreShare(_ context.Context, _, _ string, ids []string) (*pikpak.RestoreShareResponse, error) {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	f.restoreCalls++
	f.restored = append([]string(nil), ids...)
	return &pikpak.RestoreShareResponse{FileID: "restored-file"}, nil
}

func (f *libraryShareTestClient) WaitForFileDownloadURL(_ context.Context, id string, _, _ time.Duration) (*pikpak.FileEntry, error) {
	f.stateMu.Lock()
	expiry, directURL := f.expiry, f.directURL
	f.stateMu.Unlock()
	if directURL == "" {
		directURL = "https://download.example/correct-file"
	}
	file := fakeDownloadFile(id, directURL, expiry)
	file.Name, file.Size = "video.mp4", "20"
	return file, nil
}

func (f *libraryShareTestClient) countsSnapshot() (int, int, []string) {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	return f.shareCalls, f.restoreCalls, append([]string(nil), f.restored...)
}

func newLibraryResolverTestServer(t *testing.T) (*Server, *libraryShareTestClient) {
	t.Helper()
	dbPath := t.TempDir() + "/library-resolver.db"
	db, err := openDatabase(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	cipher := newTestSecretCipher(t, []byte("0123456789abcdef0123456789abcdef"))
	library, err := newLibraryStore(db, cipher)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	client := &libraryShareTestClient{fakePikPakClient: &fakePikPakClient{}, expiry: time.Now().Add(time.Hour)}
	client.getFile = func(_ context.Context, id string) (*pikpak.FileEntry, error) {
		return &pikpak.FileEntry{ID: id, Name: "video.mp4", Size: "20", Kind: "drive#file", Phase: "PHASE_TYPE_COMPLETE"}, nil
	}
	pool := fakeAccountPool("library-account", client)
	pool.store = newAccountStore(db, cipher)
	if err := pool.store.Insert(pool.accounts["library-account"].record); err != nil {
		db.Close()
		t.Fatal(err)
	}
	durable := newSQLJobStore(db, cipher)
	s := &Server{config: Config{DBFile: dbPath, ResolveTimeout: 5 * time.Second, RequestTimeout: time.Second,
		ShareParseTimeout: 3 * time.Second, ShareURLTimeout: 3 * time.Second, SharePollInterval: time.Millisecond,
		PollInterval: time.Millisecond, PublicBaseURL: "https://library.example"},
		db: db, secrets: cipher, library: library, accounts: pool, accountStore: pool.store,
		jobs: newJobStore(100, durable), durableJobs: durable, tempCleanups: newProxyTempCleanupStore(db, cipher),
		logs: newLogStore(100), authSessions: newAuthSessionStore(), mux: http.NewServeMux(), restartCh: make(chan struct{})}
	s.admission = newJobAdmission(s.jobs)
	s.resolver = newResolveQueue(5*time.Second, 5*time.Second, 1, s.failJob)
	go s.resolver.run()
	s.registerLibraryRoutes()
	s.mux.HandleFunc("GET /d/{token}", s.handleLibraryDownload)
	s.mux.HandleFunc("HEAD /d/{token}", s.handleLibraryDownload)
	s.mux.HandleFunc("GET /proxy/{id}", s.handleProxy)
	s.mux.HandleFunc("HEAD /proxy/{id}", s.handleProxy)
	s.proxyLimit = newDefaultProxyLimiter()
	t.Cleanup(func() { s.Close() })
	return s, client
}

func discoverLibraryTestShare(t *testing.T, s *Server) *LibraryEntry {
	t.Helper()
	entry := &LibraryEntry{Kind: "resource", Name: "Test share", ResourceKind: ResourceShare, Input: "https://mypikpak.com/s/test-share"}
	if err := s.library.create(entry); err != nil {
		t.Fatal(err)
	}
	s.startLibraryDiscovery(entry.ID)
	return waitLibraryTestDiscovery(t, s, entry.ID)
}

func waitLibraryTestDiscovery(t *testing.T, s *Server, entryID string) *LibraryEntry {
	t.Helper()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		loaded, err := s.library.get(entryID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Status != "pending" {
			if loaded.Status != "ready" || loaded.FileCount != 2 {
				t.Fatalf("discovered library = %#v", loaded)
			}
			return loaded
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for file discovery")
		case <-ticker.C:
		}
	}
}

type libraryMagnetTestClient struct {
	*fakePikPakClient
	stateMu      sync.Mutex
	inputs       []string
	folders      []string
	downloadedID []string
}

func (f *libraryMagnetTestClient) EnsureRootFolder(context.Context) (string, error) {
	return "root", nil
}

func (f *libraryMagnetTestClient) CreateFolder(_ context.Context, _, parent string) (*pikpak.FileEntry, error) {
	if parent != "root" {
		return nil, fmt.Errorf("unexpected folder parent %q", parent)
	}
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	id := fmt.Sprintf("temporary-%d", len(f.folders)+1)
	f.folders = append(f.folders, id)
	return &pikpak.FileEntry{ID: id, Kind: "drive#folder"}, nil
}

func (f *libraryMagnetTestClient) CreateOfflineTask(_ context.Context, input, parent, _ string) (*pikpak.TaskEntry, error) {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	f.inputs = append(f.inputs, input)
	return &pikpak.TaskEntry{ID: parent, Phase: "PHASE_TYPE_COMPLETE"}, nil
}

func (f *libraryMagnetTestClient) ListFiles(_ context.Context, parent string) ([]pikpak.FileEntry, error) {
	if strings.HasSuffix(parent, "-first") || strings.HasSuffix(parent, "-second") {
		return []pikpak.FileEntry{{ID: parent + "-file", Kind: "drive#file", Name: "video.mp4", Size: "20"}}, nil
	}
	return []pikpak.FileEntry{
		{ID: parent + "-first", Kind: "drive#folder", Name: "first"},
		{ID: parent + "-second", Kind: "drive#folder", Name: "second"},
	}, nil
}

func (f *libraryMagnetTestClient) ListOfflineTasks(context.Context, []string) ([]pikpak.TaskEntry, error) {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	tasks := make([]pikpak.TaskEntry, 0, len(f.folders))
	for _, id := range f.folders {
		tasks = append(tasks, pikpak.TaskEntry{ID: id, Phase: "PHASE_TYPE_COMPLETE"})
	}
	return tasks, nil
}

func (f *libraryMagnetTestClient) GetFile(_ context.Context, id string) (*pikpak.FileEntry, error) {
	f.stateMu.Lock()
	f.downloadedID = append(f.downloadedID, id)
	f.stateMu.Unlock()
	file := fakeDownloadFile(id, "https://download.example/"+id, time.Now().Add(time.Hour))
	file.Name, file.Size, file.Kind = "video.mp4", "20", "drive#file"
	return file, nil
}

func TestLibraryMagnetDiscoveryThenLazyDownloadUsesNewAccountLocalFileID(t *testing.T) {
	s, _ := newLibraryResolverTestServer(t)
	client := &libraryMagnetTestClient{fakePikPakClient: &fakePikPakClient{}}
	s.accounts.mu.Lock()
	s.accounts.accounts["library-account"].client = client
	s.accounts.mu.Unlock()
	entry := &LibraryEntry{Kind: "resource", Name: "Test magnet", ResourceKind: ResourceMagnet,
		Input: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=video"}
	if err := s.library.create(entry); err != nil {
		t.Fatal(err)
	}
	s.startLibraryDiscovery(entry.ID)
	entry = waitLibraryTestDiscovery(t, s, entry.ID)
	_, deleted := client.counts()
	if deleted != 1 || s.accounts.List()[0].TrafficUsed != 0 {
		t.Fatalf("discovery must clean temporary folder without traffic charge: deletes=%d account=%#v", deleted, s.accounts.List()[0])
	}
	var file LibraryFile
	for _, candidate := range entry.Files {
		if candidate.Path == "second/video.mp4" {
			file = candidate
		}
	}
	if file.Item.ID != "temporary-1-second-file" {
		t.Fatalf("discovery file selector=%#v", file)
	}
	token, err := s.library.link(entry.ID, file.ID, "direct", false)
	if err != nil {
		t.Fatal(err)
	}
	rec := libraryTestRequest(s, http.MethodGet, token)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://download.example/temporary-2-second-file" {
		t.Fatalf("magnet lazy download: status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	client.stateMu.Lock()
	inputs, downloaded := append([]string(nil), client.inputs...), append([]string(nil), client.downloadedID...)
	client.stateMu.Unlock()
	if len(inputs) != 2 || inputs[0] != inputs[1] || !reflect.DeepEqual(downloaded, []string{"temporary-2-second-file"}) {
		t.Fatalf("offline requests=%v downloaded file IDs=%v", inputs, downloaded)
	}
	if used := s.accounts.List()[0].TrafficUsed; used != 20 {
		t.Fatalf("magnet lazy download traffic=%d, want 20", used)
	}
}

func libraryTestRequest(s *Server, method, token string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "https://library.example/d/"+token, nil)
	req.Header.Set("Range", "bytes=10-19")
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestLibraryShareDiscoveryAndLazyDownload(t *testing.T) {
	s, client := newLibraryResolverTestServer(t)
	entry := discoverLibraryTestShare(t, s)
	_, restores, _ := client.countsSnapshot()
	if restores != 0 {
		t.Fatal("share discovery must not restore or generate download links")
	}
	if used := s.accounts.List()[0].TrafficUsed; used != 0 {
		t.Fatalf("discovery charged %d bytes", used)
	}
	var selected LibraryFile
	for _, file := range entry.Files {
		if file.Path == "second/video.mp4" {
			selected = file
		}
	}
	if selected.ID == "" {
		t.Fatalf("missing second file in %#v", entry.Files)
	}
	token, err := s.library.link(entry.ID, selected.ID, "direct", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rec := libraryTestRequest(s, method, token)
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://download.example/correct-file" {
			t.Fatalf("%s returned %d: %s", method, rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("public redirect must not be cached")
		}
		if method == http.MethodHead && rec.Body.Len() != 0 {
			t.Fatal("HEAD returned response body")
		}
	}
	_, restores, restored := client.countsSnapshot()
	if restores != 1 || !reflect.DeepEqual(restored, []string{"second-folder-file"}) {
		t.Fatalf("restore calls=%d IDs=%v; want selected file only, reused by HEAD", restores, restored)
	}
	if used := s.accounts.List()[0].TrafficUsed; used != 20 {
		t.Fatalf("download charged %d bytes, want one resolution of 20 bytes", used)
	}
	request := httptest.NewRequest(http.MethodGet, "/d/"+token+"?format=json", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, request)
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil || payload["url"] != "https://download.example/correct-file" {
		t.Fatalf("JSON download response=%s, err=%v", rec.Body.String(), err)
	}
	// Expiring the shared result forces a fresh queued resolution on next access.
	now := s.now()
	s.setNowFunc(func() time.Time { return now.Add(3 * time.Minute) })
	if rec := libraryTestRequest(s, http.MethodGet, token); rec.Code != http.StatusFound {
		t.Fatalf("expired-cache response=%d %s", rec.Code, rec.Body.String())
	}
	_, restores, _ = client.countsSnapshot()
	if restores != 2 {
		t.Fatalf("expired cache restore calls=%d, want 2", restores)
	}
}

func TestLibraryChangedSourceDoesNotDownloadOrDisableAccount(t *testing.T) {
	s, client := newLibraryResolverTestServer(t)
	entry := discoverLibraryTestShare(t, s)
	token, err := s.library.link(entry.ID, entry.Files[0].ID, "direct", false)
	if err != nil {
		t.Fatal(err)
	}
	client.stateMu.Lock()
	client.sourceSize = "21"
	client.stateMu.Unlock()
	rec := libraryTestRequest(s, http.MethodGet, token)
	if rec.Code != http.StatusBadGateway || rec.Header().Get("Location") != "" {
		t.Fatalf("changed source download: status=%d location=%q body=%s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	_, restores, _ := client.countsSnapshot()
	if restores != 0 {
		t.Fatalf("changed source restored %d files", restores)
	}
	account := s.accounts.List()[0]
	if account.Status != AccountAvailable || account.TrafficUsed != 0 {
		t.Fatalf("changed source altered account health/accounting: %#v", account)
	}
}

func TestLibraryConcurrentDownloadsShareWorkAndRevocationWins(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		name := "reuse"
		if revoke {
			name = "revoked-while-resolving"
		}
		t.Run(name, func(t *testing.T) {
			s, client := newLibraryResolverTestServer(t)
			entry := discoverLibraryTestShare(t, s)
			file := entry.Files[0]
			token, err := s.library.link(entry.ID, file.ID, "proxy", false)
			if err != nil {
				t.Fatal(err)
			}
			release, entered := make(chan struct{}), make(chan struct{}, 1)
			client.stateMu.Lock()
			client.block, client.entered = release, entered
			client.stateMu.Unlock()
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			responses := make(chan *httptest.ResponseRecorder, 2)
			go func() { responses <- libraryTestRequest(s, http.MethodGet, token) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("download did not enter resolver")
			}
			go func() { responses <- libraryTestRequest(s, http.MethodHead, token) }()
			if revoke {
				if err := s.library.revokeLink(entry.ID, file.ID); err != nil {
					t.Fatal(err)
				}
			}
			once.Do(func() { close(release) })
			for range 2 {
				select {
				case rec := <-responses:
					if revoke {
						if rec.Code != http.StatusNotFound || rec.Header().Get("Location") != "" {
							t.Fatalf("revoked download returned %d, location=%q", rec.Code, rec.Header().Get("Location"))
						}
					} else if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/proxy/") {
						t.Fatalf("download returned %d: %s", rec.Code, rec.Body.String())
					}
				case <-time.After(5 * time.Second):
					t.Fatal("download response timed out")
				}
			}
			_, restores, _ := client.countsSnapshot()
			if restores != 1 {
				t.Fatalf("concurrent downloads generated %d restores, want 1", restores)
			}
		})
	}
}

func TestLibraryDownloadRedirectAndProxyPreserveHTTPRange(t *testing.T) {
	for _, mode := range []string{"direct", "proxy"} {
		t.Run(mode, func(t *testing.T) {
			ranges := make(chan string, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ranges <- r.Header.Get("Range")
				w.Header().Set("Content-Range", "bytes 10-19/20")
				w.Header().Set("Content-Length", "10")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("abcdefghij"))
			}))
			defer upstream.Close()
			s, fake := newLibraryResolverTestServer(t)
			fake.stateMu.Lock()
			fake.directURL = upstream.URL + "/file"
			fake.stateMu.Unlock()
			entry := discoverLibraryTestShare(t, s)
			token, err := s.library.link(entry.ID, entry.Files[0].ID, mode, false)
			if err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(s.Handler())
			defer server.Close()
			req, err := http.NewRequest(http.MethodGet, server.URL+"/d/"+token, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Range", "bytes=10-19")
			client := server.Client()
			client.Timeout = 5 * time.Second
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(res.Body)
			res.Body.Close()
			if err != nil || res.StatusCode != http.StatusPartialContent || string(body) != "abcdefghij" || res.Header.Get("Content-Range") != "bytes 10-19/20" {
				t.Fatalf("range download: status=%d headers=%v body=%q err=%v", res.StatusCode, res.Header, body, err)
			}
			select {
			case got := <-ranges:
				if got != "bytes=10-19" {
					t.Fatalf("upstream Range=%q", got)
				}
			default:
				t.Fatal("download did not reach upstream")
			}
		})
	}
}
