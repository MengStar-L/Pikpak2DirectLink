package app

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func openLibraryStoreForTest(t *testing.T, path string, cipher *SecretCipher) (*sql.DB, *libraryStore) {
	t.Helper()
	db, err := openDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := newLibraryStore(db, cipher)
	if err != nil {
		t.Fatal(err)
	}
	return db, store
}

func createLibraryEntryForTest(t *testing.T, store *libraryStore, kind, parent string) *LibraryEntry {
	t.Helper()
	entry := &LibraryEntry{Kind: kind, Name: "测试资源", ParentID: parent}
	if kind == "resource" {
		entry.ResourceKind, entry.Input, entry.PassCode = ResourceShare, "https://mypikpak.com/s/private-share-id?pwd=private-code", "private-code"
	}
	if err := store.create(entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func completeLibraryEntryForTest(t *testing.T, store *libraryStore, id string) *LibraryEntry {
	t.Helper()
	files := []LibraryFile{
		{Item: DownloadItem{ID: "private-selector-1", Name: "a.zip", Path: "folder/a.zip", Size: "1024", MimeType: "application/zip"}},
		{Item: DownloadItem{ID: "private-selector-2", Name: "b.zip", Path: "folder/b.zip", Size: "2048"}},
	}
	if err := store.complete(id, files); err != nil {
		t.Fatal(err)
	}
	entry, err := store.get(id)
	if err != nil {
		t.Fatal(err)
	}
	if entry.FileCount != 2 || len(entry.Files) != 2 || entry.Files[0].ID == "" || entry.Files[0].Name != "a.zip" {
		t.Fatalf("completed entry = %+v", entry)
	}
	return entry
}

func TestLibraryPersistenceEncryptionAndKeyRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.sqlite")
	oldKey := bytes.Repeat([]byte{0x41}, secretKeySize)
	cipher := newTestSecretCipher(t, oldKey)
	db, store := openLibraryStoreForTest(t, path, cipher)
	folder := createLibraryEntryForTest(t, store, "folder", "")
	entry := createLibraryEntryForTest(t, store, "resource", folder.ID)
	entry = completeLibraryEntryForTest(t, store, entry.ID)
	token, err := store.link(entry.ID, entry.Files[0].ID, "proxy", false)
	if err != nil {
		t.Fatal(err)
	}
	var payload, encryptedToken, digest string
	if err := db.QueryRow(`SELECT payload_encrypted FROM library_entries WHERE id=?`, entry.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT token_encrypted,token_hash FROM library_links`).Scan(&encryptedToken, &digest); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{entry.Input, entry.PassCode, "private-selector-1", "folder/a.zip", token} {
		if strings.Contains(payload, secret) || strings.Contains(encryptedToken, secret) || digest == secret {
			t.Fatalf("library secrets stored in plaintext: %q", secret)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	newKey := bytes.Repeat([]byte{0x42}, secretKeySize)
	rotatedCipher, err := NewSecretCipher(base64.StdEncoding.EncodeToString(newKey), []string{base64.StdEncoding.EncodeToString(oldKey)})
	if err != nil {
		t.Fatal(err)
	}
	db, store = openLibraryStoreForTest(t, path, rotatedCipher)
	gotEntry, gotFile, mode, err := store.lookupToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if gotEntry.ID != entry.ID || gotEntry.Input != entry.Input || gotEntry.PassCode != entry.PassCode || gotFile.Item.ID != "private-selector-1" || mode != "proxy" {
		t.Fatalf("restart did not preserve private selector or capability: %+v %+v %q", gotEntry, gotFile, mode)
	}
	if err := db.QueryRow(`SELECT payload_encrypted FROM library_entries WHERE id=?`, entry.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(payload, "v1."+rotatedCipher.CurrentKeyID()+".") {
		t.Fatal("library entry was not rotated")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, store = openLibraryStoreForTest(t, path, newTestSecretCipher(t, newKey))
	if _, _, _, err := store.lookupToken(token); err != nil {
		t.Fatalf("library requires obsolete key after rotation: %v", err)
	}
}

func TestLibraryLinksStayStableAndRevocationPersists(t *testing.T) {
	_, store := openLibraryStoreForTest(t, ":memory:", newTestSecretCipher(t, bytes.Repeat([]byte{0x43}, secretKeySize)))
	entry := completeLibraryEntryForTest(t, store, createLibraryEntryForTest(t, store, "resource", "").ID)
	fileID := entry.Files[0].ID
	token, err := store.link(entry.ID, fileID, "direct", false)
	if err != nil {
		t.Fatal(err)
	}
	again, err := store.link(entry.ID, fileID, "proxy", false)
	if err != nil || again != token {
		t.Fatalf("changing mode changed stable URL: %v", err)
	}
	_, file, mode, err := store.lookupToken(token)
	if err != nil || file.ID != fileID || mode != "proxy" {
		t.Fatalf("mode update = %+v %q %v", file, mode, err)
	}
	reset, err := store.link(entry.ID, fileID, "direct", true)
	if err != nil || reset == token {
		t.Fatalf("reset did not rotate token: %v", err)
	}
	if _, _, _, err := store.lookupToken(token); !errors.Is(err, errLibraryNotFound) {
		t.Fatalf("old token remains valid after reset: %v", err)
	}
	if err := store.revokeLink(entry.ID, fileID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.lookupToken(reset); !errors.Is(err, errLibraryNotFound) {
		t.Fatalf("token remains valid after revocation: %v", err)
	}
	if _, err := store.link(entry.ID, "missing-file", "direct", false); !errors.Is(err, errLibraryNotFound) {
		t.Fatalf("generated capability for absent file: %v", err)
	}
}

func TestLibraryMovesPreventCyclesAndDeletionRevokesDescendants(t *testing.T) {
	db, store := openLibraryStoreForTest(t, ":memory:", newTestSecretCipher(t, bytes.Repeat([]byte{0x44}, secretKeySize)))
	a := createLibraryEntryForTest(t, store, "folder", "")
	b := createLibraryEntryForTest(t, store, "folder", "")
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, move := range [][2]string{{a.ID, b.ID}, {b.ID, a.ID}} {
		wg.Add(1)
		go func(id, parent string) {
			defer wg.Done()
			_, err := store.update(id, nil, &parent)
			results <- err
		}(move[0], move[1])
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent inverse moves: %d succeeded, want exactly 1", successes)
	}
	a, _ = store.get(a.ID)
	root, child := a.ID, b.ID
	if a.ParentID != "" {
		root, child = b.ID, a.ID
	}
	entry := completeLibraryEntryForTest(t, store, createLibraryEntryForTest(t, store, "resource", child).ID)
	token, err := store.link(entry.ID, entry.Files[0].ID, "direct", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.update(root, nil, &entry.ID); err == nil {
		t.Fatal("moved a folder into a resource")
	}
	if err := store.remove(root); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.lookupToken(token); !errors.Is(err, errLibraryNotFound) {
		t.Fatalf("recursive delete retained public capability: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM library_links`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan capability rows = %d: %v", count, err)
	}
	if err := store.complete(entry.ID, []LibraryFile{{Item: DownloadItem{ID: "late", Name: "late"}}}); !errors.Is(err, errLibraryNotFound) {
		t.Fatalf("late discovery revived deleted resource: %v", err)
	}
}

func TestLibraryInterruptedDiscoveryRequiresExplicitRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "interrupted.sqlite")
	cipher := newTestSecretCipher(t, bytes.Repeat([]byte{0x45}, secretKeySize))
	db, store := openLibraryStoreForTest(t, path, cipher)
	entry := createLibraryEntryForTest(t, store, "resource", "")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, store = openLibraryStoreForTest(t, path, cipher)
	got, err := store.get(entry.ID)
	if err != nil || got.Status != "failed" || got.Error == "" {
		t.Fatalf("interrupted entry = %+v: %v", got, err)
	}
	if _, err := store.markPending(entry.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.markPending(entry.ID); !errors.Is(err, errLibraryConflict) {
		t.Fatalf("duplicate retry accepted: %v", err)
	}
	failure := "upstream echoed https://mypikpak.com/s/private-share-id?pwd=private-code"
	if err := store.fail(entry.ID, failure); err != nil {
		t.Fatal(err)
	}
	got, err = store.get(entry.ID)
	if err != nil || got.Status != "failed" || got.Error != failure {
		t.Fatalf("failed entry = %+v: %v", got, err)
	}
	var rawError, payload string
	if err := store.db.QueryRow(`SELECT error,payload_encrypted FROM library_entries WHERE id=?`, entry.ID).Scan(&rawError, &payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rawError, "private-code") || strings.Contains(payload, "private-code") {
		t.Fatal("upstream failure text exposed source credentials in storage")
	}
	got, err = store.markPending(entry.ID)
	if err != nil || got.Error != "" {
		t.Fatalf("retry retained obsolete failure: %+v: %v", got, err)
	}
}

func TestLibraryAdminResponsesHideSecretsAndShowStableLinks(t *testing.T) {
	_, store := openLibraryStoreForTest(t, ":memory:", newTestSecretCipher(t, bytes.Repeat([]byte{0x46}, secretKeySize)))
	entry := completeLibraryEntryForTest(t, store, createLibraryEntryForTest(t, store, "resource", "").ID)
	token, err := store.link(entry.ID, entry.Files[0].ID, "direct", false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{library: store, config: Config{PublicBaseURL: "https://downloads.example.com"}}
	recorder := httptest.NewRecorder()
	s.handleLibraryList(recorder, httptest.NewRequest(http.MethodGet, "/api/library", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, secret := range []string{entry.Input, entry.PassCode, "private-selector-1", `"item"`, `"token"`} {
		if strings.Contains(body, secret) {
			t.Fatalf("API exposed private source or selector: %q", secret)
		}
	}
	if !strings.Contains(body, "https://downloads.example.com/d/"+token) {
		t.Fatal("API did not project stable public link")
	}
	var list struct {
		Entries []LibraryEntry `json:"entries"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil || len(list.Entries) != 1 || list.Entries[0].FileCount != 2 {
		t.Fatalf("API file count = %+v: %v", list, err)
	}
}
