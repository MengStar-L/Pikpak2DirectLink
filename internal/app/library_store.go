package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	errLibraryNotFound = errors.New("文件或文件夹不存在")
	errLibraryConflict = errors.New("资源状态已改变，请刷新后重试")
)

type libraryValidationError string

func (e libraryValidationError) Error() string { return string(e) }

func libraryNullableParent(id string) any {
	if id == "" {
		return nil
	}
	return id
}

const librarySchema = `
CREATE TABLE IF NOT EXISTS library_entries (
 id TEXT PRIMARY KEY,
 parent_id TEXT REFERENCES library_entries(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind IN ('folder','resource')),
 name TEXT NOT NULL,
 resource_kind TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL CHECK(status IN ('pending','ready','failed')),
 error TEXT NOT NULL DEFAULT '',
 payload_encrypted TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_library_entries_parent ON library_entries(parent_id);
CREATE TABLE IF NOT EXISTS library_links (
 token_hash TEXT PRIMARY KEY,
 entry_id TEXT NOT NULL REFERENCES library_entries(id) ON DELETE CASCADE,
 file_id TEXT NOT NULL,
 mode TEXT NOT NULL CHECK(mode IN ('direct','proxy')),
 token_encrypted TEXT NOT NULL,
 UNIQUE(entry_id,file_id)
);`

// LibraryEntry is a local catalog record. Moving or deleting it changes the
// catalog, independently of the original resource shared by its owner.
type LibraryEntry struct {
	ID           string        `json:"id"`
	ParentID     string        `json:"parent_id"`
	Kind         string        `json:"kind"`
	Name         string        `json:"name"`
	ResourceKind ResourceKind  `json:"resource_kind"`
	Status       string        `json:"status"`
	FileCount    int           `json:"file_count"`
	Error        string        `json:"error"`
	Files        []LibraryFile `json:"files"`
	CreatedAt    time.Time     `json:"created_at"`
	UpdatedAt    time.Time     `json:"updated_at"`
	Input        string        `json:"-"`
	PassCode     string        `json:"-"`
}

type LibraryFile struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Path      string       `json:"path"`
	Size      string       `json:"size"`
	MimeType  string       `json:"mime_type,omitempty"`
	Mode      string       `json:"mode,omitempty"`
	PublicURL string       `json:"public_url,omitempty"`
	Item      DownloadItem `json:"-"`
	Token     string       `json:"-"`
}

type libraryStoredFile struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Path     string       `json:"path"`
	Size     string       `json:"size"`
	MimeType string       `json:"mime_type,omitempty"`
	Item     DownloadItem `json:"item"`
}

type libraryPayload struct {
	Input    string              `json:"input"`
	PassCode string              `json:"pass_code"`
	Error    string              `json:"error,omitempty"`
	Files    []libraryStoredFile `json:"files"`
}

type libraryStore struct {
	db     *sql.DB
	cipher *SecretCipher
}

func newLibraryStore(db *sql.DB, cipher *SecretCipher) (*libraryStore, error) {
	if _, err := db.Exec(librarySchema); err != nil {
		return nil, fmt.Errorf("initialize file library: %w", err)
	}
	s := &libraryStore{db: db, cipher: cipher}
	if err := s.rotateSecrets(); err != nil {
		return nil, err
	}
	// Discovery does not resume automatically after a restart: its temporary
	// PikPak job is reconciled through the existing durable-job cleanup system.
	_, err := db.Exec(`UPDATE library_entries SET status='failed', error=?, updated_at=? WHERE status='pending'`,
		"文件检查因服务重启而中断，请重试", time.Now().Unix())
	return s, err
}

func libraryPayloadFor(entry *LibraryEntry) libraryPayload {
	payload := libraryPayload{Input: entry.Input, PassCode: entry.PassCode, Error: entry.Error, Files: make([]libraryStoredFile, 0, len(entry.Files))}
	for _, file := range entry.Files {
		payload.Files = append(payload.Files, libraryStoredFile{ID: file.ID, Name: file.Name, Path: file.Path, Size: file.Size, MimeType: file.MimeType, Item: file.Item})
	}
	return payload
}

func (s *libraryStore) encryptEntry(entry *LibraryEntry) (string, error) {
	data, err := json.Marshal(libraryPayloadFor(entry))
	if err != nil {
		return "", err
	}
	return s.cipher.Encrypt("library-entry", entry.ID, data)
}

type libraryScanner interface{ Scan(...any) error }

const libraryEntryColumns = `id, COALESCE(parent_id,''), kind, name, resource_kind, status, error, payload_encrypted, created_at, updated_at`

func (s *libraryStore) scanEntry(row libraryScanner) (*LibraryEntry, error) {
	entry := &LibraryEntry{Files: []LibraryFile{}}
	var encrypted string
	var created, updated int64
	err := row.Scan(&entry.ID, &entry.ParentID, &entry.Kind, &entry.Name, &entry.ResourceKind, &entry.Status, &entry.Error, &encrypted, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errLibraryNotFound
	}
	if err != nil {
		return nil, err
	}
	data, err := s.cipher.Decrypt("library-entry", entry.ID, encrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt library entry: %w", err)
	}
	var payload libraryPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	entry.Input, entry.PassCode = payload.Input, payload.PassCode
	if entry.Status == "failed" && payload.Error != "" {
		entry.Error = payload.Error
	}
	for _, file := range payload.Files {
		entry.Files = append(entry.Files, LibraryFile{ID: file.ID, Name: file.Name, Path: file.Path, Size: file.Size, MimeType: file.MimeType, Item: file.Item})
	}
	entry.FileCount = len(entry.Files)
	entry.CreatedAt, entry.UpdatedAt = time.Unix(created, 0).UTC(), time.Unix(updated, 0).UTC()
	return entry, nil
}

func (s *libraryStore) get(id string) (*LibraryEntry, error) {
	entry, err := s.scanEntry(s.db.QueryRow(`SELECT `+libraryEntryColumns+` FROM library_entries WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	if err := s.loadLinks(entry); err != nil {
		return nil, err
	}
	return entry, nil
}

func (s *libraryStore) list() ([]LibraryEntry, error) {
	rows, err := s.db.Query(`SELECT ` + libraryEntryColumns + ` FROM library_entries ORDER BY created_at DESC, id`)
	if err != nil {
		return nil, err
	}
	entries := []LibraryEntry{}
	for rows.Next() {
		entry, err := s.scanEntry(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		entries = append(entries, *entry)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range entries {
		if err := s.loadLinks(&entries[i]); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func validLibraryName(name string) bool {
	return name != "" && utf8.RuneCountInString(name) <= 255 && !strings.ContainsAny(name, "\x00\r\n")
}

// A transaction holds SQLite's immediate writer lock throughout ancestry
// validation and mutation, so two concurrent moves cannot create a cycle.
func validateLibraryParent(tx *sql.Tx, parentID, entryID string) error {
	seen := map[string]bool{}
	parentDepth := 0
	for depth := 0; parentID != ""; depth++ {
		if parentID == entryID || seen[parentID] || depth >= 100 {
			return libraryValidationError("不能移动到自身或下级文件夹，文件夹层级不能超过 100 层")
		}
		seen[parentID] = true
		parentDepth++
		var kind, ancestor string
		err := tx.QueryRow(`SELECT kind, COALESCE(parent_id,'') FROM library_entries WHERE id=?`, parentID).Scan(&kind, &ancestor)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && kind != "folder") {
			return libraryValidationError("目标文件夹不存在")
		}
		if err != nil {
			return err
		}
		parentID = ancestor
	}
	// Moving a whole subtree must account for its deepest child, not only the
	// destination's ancestors. Otherwise repeated valid moves could exceed
	// SQLite's cascading-delete depth and make a folder impossible to remove.
	var height int
	if err := tx.QueryRow(`WITH RECURSIVE subtree(id,depth) AS (
	 SELECT id,1 FROM library_entries WHERE id=?
	 UNION ALL SELECT child.id,subtree.depth+1 FROM library_entries child JOIN subtree ON child.parent_id=subtree.id
	) SELECT COALESCE(MAX(depth),1) FROM subtree`, entryID).Scan(&height); err != nil {
		return err
	}
	if parentDepth+height > 100 {
		return libraryValidationError("文件夹层级不能超过 100 层")
	}
	return nil
}

func (s *libraryStore) create(entry *LibraryEntry) error {
	if !validLibraryName(entry.Name) {
		return libraryValidationError("名称不能为空或超过 255 个字符")
	}
	id, err := generateSessionID()
	if err != nil {
		return err
	}
	entry.ID = id
	entry.CreatedAt, entry.UpdatedAt = time.Now().UTC(), time.Now().UTC()
	entry.Files = []LibraryFile{}
	entry.Status = "pending"
	if entry.Kind == "folder" {
		entry.Status = "ready"
	}
	encrypted, err := s.encryptEntry(entry)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := validateLibraryParent(tx, entry.ParentID, entry.ID); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO library_entries(id,parent_id,kind,name,resource_kind,status,error,payload_encrypted,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		entry.ID, libraryNullableParent(entry.ParentID), entry.Kind, entry.Name, entry.ResourceKind, entry.Status, "", encrypted, entry.CreatedAt.Unix(), entry.UpdatedAt.Unix())
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *libraryStore) update(id string, name, parentID *string) (*LibraryEntry, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	entry, err := s.scanEntry(tx.QueryRow(`SELECT `+libraryEntryColumns+` FROM library_entries WHERE id=?`, id))
	if err != nil {
		return nil, err
	}
	if name != nil {
		entry.Name = strings.TrimSpace(*name)
		if !validLibraryName(entry.Name) {
			return nil, libraryValidationError("名称不能为空或超过 255 个字符")
		}
	}
	if parentID != nil {
		entry.ParentID = strings.TrimSpace(*parentID)
		if err := validateLibraryParent(tx, entry.ParentID, id); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`UPDATE library_entries SET name=?, parent_id=?, updated_at=? WHERE id=?`, entry.Name, libraryNullableParent(entry.ParentID), time.Now().Unix(), id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.get(id)
}

func (s *libraryStore) remove(id string) error {
	result, err := s.db.Exec(`DELETE FROM library_entries WHERE id=?`, id)
	return libraryRequireChanged(result, err)
}

func libraryRequireChanged(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err == nil && changed == 0 {
		return errLibraryNotFound
	}
	return err
}

func (s *libraryStore) complete(id string, files []LibraryFile) error {
	if len(files) == 0 {
		return errors.New("资源未包含可下载的文件")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	entry, err := s.scanEntry(tx.QueryRow(`SELECT `+libraryEntryColumns+` FROM library_entries WHERE id=? AND kind='resource' AND status='pending'`, id))
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	for i := range files {
		if files[i].ID == "" {
			files[i].ID, err = generateSessionID()
			if err != nil {
				return err
			}
		}
		if ids[files[i].ID] {
			return errors.New("资源返回了重复的文件标识")
		}
		ids[files[i].ID] = true
		if files[i].Name == "" {
			files[i].Name = files[i].Item.Name
		}
		if files[i].Path == "" {
			files[i].Path = files[i].Item.Path
		}
		if files[i].Size == "" {
			files[i].Size = files[i].Item.Size
		}
		if files[i].MimeType == "" {
			files[i].MimeType = files[i].Item.MimeType
		}
	}
	entry.Files = files
	encrypted, err := s.encryptEntry(entry)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE library_entries SET status='ready', error='', payload_encrypted=?, updated_at=? WHERE id=? AND status='pending'`, encrypted, time.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *libraryStore) fail(id, message string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	entry, err := s.scanEntry(tx.QueryRow(`SELECT `+libraryEntryColumns+` FROM library_entries WHERE id=? AND status='pending'`, id))
	if errors.Is(err, errLibraryNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	// API error text can contain an echoed share URL or extraction code. Keep
	// it under the same encryption boundary as the original source.
	entry.Error = message
	encrypted, err := s.encryptEntry(entry)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE library_entries SET status='failed', error='', payload_encrypted=?, updated_at=? WHERE id=? AND status='pending'`, encrypted, time.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *libraryStore) markPending(id string) (*LibraryEntry, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	entry, err := s.scanEntry(tx.QueryRow(`SELECT `+libraryEntryColumns+` FROM library_entries WHERE id=? AND kind='resource' AND status='failed'`, id))
	if errors.Is(err, errLibraryNotFound) {
		return nil, errLibraryConflict
	}
	if err != nil {
		return nil, err
	}
	entry.Error = ""
	encrypted, err := s.encryptEntry(entry)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE library_entries SET status='pending', error='', payload_encrypted=?, updated_at=? WHERE id=? AND kind='resource' AND status='failed'`, encrypted, time.Now().Unix(), id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.get(id)
}

func (s *libraryStore) loadLinks(entry *LibraryEntry) error {
	rows, err := s.db.Query(`SELECT file_id, mode, token_encrypted FROM library_links WHERE entry_id=?`, entry.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	byID := map[string]*LibraryFile{}
	for i := range entry.Files {
		byID[entry.Files[i].ID] = &entry.Files[i]
	}
	for rows.Next() {
		var fileID, mode, encrypted string
		if err := rows.Scan(&fileID, &mode, &encrypted); err != nil {
			return err
		}
		token, err := s.cipher.Decrypt("library-link", entry.ID+"/"+fileID, encrypted)
		if err != nil {
			return err
		}
		if file := byID[fileID]; file != nil {
			file.Token, file.Mode = string(token), mode
		}
	}
	return rows.Err()
}

func (s *libraryStore) link(id, fileID, mode string, reset bool) (string, error) {
	if mode != "direct" && mode != "proxy" {
		return "", libraryValidationError("下载方式必须为 direct 或 proxy")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	entry, err := s.scanEntry(tx.QueryRow(`SELECT `+libraryEntryColumns+` FROM library_entries WHERE id=? AND kind='resource' AND status='ready'`, id))
	if err != nil {
		return "", err
	}
	found := false
	for _, file := range entry.Files {
		found = found || file.ID == fileID
	}
	if !found {
		return "", errLibraryNotFound
	}
	var token string
	var existingEncrypted string
	err = tx.QueryRow(`SELECT token_encrypted FROM library_links WHERE entry_id=? AND file_id=?`, id, fileID).Scan(&existingEncrypted)
	if err == nil && !reset {
		plain, err := s.cipher.Decrypt("library-link", id+"/"+fileID, existingEncrypted)
		if err != nil {
			return "", err
		}
		token = string(plain)
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if token == "" {
		token, err = generateSessionID()
		if err != nil {
			return "", err
		}
	}
	encrypted, err := s.cipher.Encrypt("library-link", id+"/"+fileID, []byte(token))
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(`INSERT INTO library_links(token_hash,entry_id,file_id,mode,token_encrypted) VALUES(?,?,?,?,?)
	 ON CONFLICT(entry_id,file_id) DO UPDATE SET token_hash=excluded.token_hash,mode=excluded.mode,token_encrypted=excluded.token_encrypted`, sessionTokenDigest(token), id, fileID, mode, encrypted)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return token, nil
}

func (s *libraryStore) revokeLink(id, fileID string) error {
	_, err := s.db.Exec(`DELETE FROM library_links WHERE entry_id=? AND file_id=?`, id, fileID)
	return err
}

func (s *libraryStore) lookupToken(token string) (*LibraryEntry, *LibraryFile, string, error) {
	if len(token) != 64 || strings.TrimSpace(token) != token {
		return nil, nil, "", errLibraryNotFound
	}
	var entryID, fileID, mode string
	err := s.db.QueryRow(`SELECT entry_id,file_id,mode FROM library_links WHERE token_hash=?`, sessionTokenDigest(token)).Scan(&entryID, &fileID, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, "", errLibraryNotFound
	}
	if err != nil {
		return nil, nil, "", err
	}
	entry, err := s.get(entryID)
	if err != nil {
		return nil, nil, "", err
	}
	if entry.Status != "ready" {
		return nil, nil, "", errLibraryNotFound
	}
	for i := range entry.Files {
		file := &entry.Files[i]
		// Re-read capability state through get: a concurrent revoke or reset
		// between lookup and payload read must not authorize the old token.
		if file.ID == fileID && constantTimeTokenEqual(file.Token, token) {
			return entry, file, file.Mode, nil
		}
	}
	return nil, nil, "", errLibraryNotFound
}

func (s *libraryStore) rotateSecrets() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []struct{ query, purpose, update string }{
		{`SELECT id,payload_encrypted FROM library_entries`, "library-entry", `UPDATE library_entries SET payload_encrypted=? WHERE id=?`},
		{`SELECT entry_id || '/' || file_id,token_encrypted FROM library_links`, "library-link", `UPDATE library_links SET token_encrypted=? WHERE entry_id || '/' || file_id=?`},
	} {
		rows, err := tx.Query(table.query)
		if err != nil {
			return err
		}
		type rotation struct{ id, encrypted string }
		var updates []rotation
		for rows.Next() {
			var id, encrypted string
			if err := rows.Scan(&id, &encrypted); err != nil {
				rows.Close()
				return err
			}
			plain, err := s.cipher.Decrypt(table.purpose, id, encrypted)
			if err != nil {
				rows.Close()
				return fmt.Errorf("decrypt file library during key rotation: %w", err)
			}
			rotate, err := s.cipher.NeedsRotation(encrypted)
			if err != nil {
				rows.Close()
				return err
			}
			if rotate {
				encrypted, err = s.cipher.Encrypt(table.purpose, id, plain)
				if err != nil {
					rows.Close()
					return err
				}
				updates = append(updates, rotation{id, encrypted})
			}
		}
		if err := rows.Close(); err != nil {
			return err
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, update := range updates {
			if _, err := tx.Exec(table.update, update.encrypted, update.id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
