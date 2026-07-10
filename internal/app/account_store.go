package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"pikpak2directlink/internal/pikpak"
)

const (
	accountPasswordSecretPurpose = "pikpak-account-password"
	accountSessionSecretPurpose  = "pikpak-account-session"
)

const accountColumns = `
	id, username, password_encrypted, session_file, status, premium,
	premium_type, premium_until, premium_error, premium_checked_at,
	traffic_limit, traffic_used, traffic_period, last_error, last_failed_at,
	credential_checked_at, credential_next_check_at, credential_check_error,
	parse_errors_json, created_at, updated_at`

type accountStore struct {
	db     *sql.DB
	cipher *SecretCipher
	now    func() time.Time
}

func newAccountStore(db *sql.DB, cipher *SecretCipher) *accountStore {
	return &accountStore{db: db, cipher: cipher, now: time.Now}
}

// List returns all persisted accounts in their configured order.
func (s *accountStore) List() ([]accountRecord, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}

	rows, err := s.db.Query(`SELECT ` + accountColumns + ` FROM pikpak_accounts ORDER BY sort_order`)
	if err != nil {
		return nil, fmt.Errorf("list PikPak accounts: %w", err)
	}
	defer rows.Close()

	records := make([]accountRecord, 0)
	for rows.Next() {
		record, err := s.scanAccount(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list PikPak accounts: %w", err)
	}
	return records, nil
}

// Replace atomically makes the database match records and their slice order.
// Existing account rows are updated in place so their session rows survive.
func (s *accountStore) Replace(records []accountRecord) error {
	if err := s.validate(); err != nil {
		return err
	}

	prepared := make([]storedAccountRecord, 0, len(records))
	wanted := make(map[string]struct{}, len(records))
	for _, record := range records {
		if _, duplicate := wanted[record.ID]; duplicate {
			return fmt.Errorf("replace PikPak accounts: duplicate account id %q", record.ID)
		}
		item, err := s.prepareAccount(record)
		if err != nil {
			return err
		}
		wanted[record.ID] = struct{}{}
		prepared = append(prepared, item)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("replace PikPak accounts: begin transaction: %w", err)
	}
	defer tx.Rollback()

	var maxOrder int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(sort_order), -1) FROM pikpak_accounts`).Scan(&maxOrder); err != nil {
		return fmt.Errorf("replace PikPak accounts: find maximum order: %w", err)
	}
	shift := maxOrder + int64(len(prepared)) + 2
	if _, err := tx.Exec(`UPDATE pikpak_accounts SET sort_order = sort_order + ?`, shift); err != nil {
		return fmt.Errorf("replace PikPak accounts: reserve account order: %w", err)
	}

	for order, item := range prepared {
		if err := upsertAccount(tx, item, order); err != nil {
			return fmt.Errorf("replace PikPak account %q: %w", item.record.ID, err)
		}
	}

	rows, err := tx.Query(`SELECT id FROM pikpak_accounts`)
	if err != nil {
		return fmt.Errorf("replace PikPak accounts: list obsolete rows: %w", err)
	}
	var obsolete []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("replace PikPak accounts: scan obsolete row: %w", err)
		}
		if _, keep := wanted[id]; !keep {
			obsolete = append(obsolete, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("replace PikPak accounts: iterate obsolete rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("replace PikPak accounts: close obsolete rows: %w", err)
	}
	for _, id := range obsolete {
		if _, err := tx.Exec(`DELETE FROM pikpak_accounts WHERE id = ?`, id); err != nil {
			return fmt.Errorf("replace PikPak accounts: delete %q: %w", id, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("replace PikPak accounts: commit: %w", err)
	}
	return nil
}

// ImportLegacy inserts a complete legacy account set and its opaque sessions
// in one transaction. It refuses a non-empty destination so a failed or
// repeated migration can never merge two sources silently.
func (s *accountStore) ImportLegacy(records []accountRecord, sessions map[string][]byte) error {
	if err := s.validate(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.importLegacyTx(tx, records, sessions); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *accountStore) importLegacyTx(tx *sql.Tx, records []accountRecord, sessions map[string][]byte) error {
	prepared := make([]storedAccountRecord, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if _, exists := seen[record.ID]; exists {
			return fmt.Errorf("import legacy accounts: duplicate account id %q", record.ID)
		}
		item, err := s.prepareAccount(record)
		if err != nil {
			return err
		}
		seen[record.ID] = struct{}{}
		prepared = append(prepared, item)
	}

	var existing int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM pikpak_accounts`).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return errors.New("import legacy accounts: destination is not empty")
	}
	for order, item := range prepared {
		if err := insertAccount(tx, item, order); err != nil {
			return err
		}
		data, ok := sessions[item.record.ID]
		if !ok {
			continue
		}
		encrypted, err := s.cipher.Encrypt(accountSessionSecretPurpose, item.record.ID, data)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(
			`INSERT INTO pikpak_account_sessions(account_id,session_encrypted,updated_at) VALUES(?,?,?)`,
			item.record.ID, encrypted, s.now().Unix(),
		); err != nil {
			return err
		}
	}
	return nil
}

// Insert appends an account to the current order.
func (s *accountStore) Insert(record accountRecord) error {
	if err := s.validate(); err != nil {
		return err
	}
	item, err := s.prepareAccount(record)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("insert PikPak account %q: begin transaction: %w", record.ID, err)
	}
	defer tx.Rollback()

	var order int
	if err := tx.QueryRow(`SELECT COALESCE(MAX(sort_order), -1) + 1 FROM pikpak_accounts`).Scan(&order); err != nil {
		return fmt.Errorf("insert PikPak account %q: find order: %w", record.ID, err)
	}
	if err := insertAccount(tx, item, order); err != nil {
		return fmt.Errorf("insert PikPak account %q: %w", record.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("insert PikPak account %q: commit: %w", record.ID, err)
	}
	return nil
}

func (s *accountStore) UpsertWithSession(record accountRecord, session []byte) error {
	if err := s.validate(); err != nil {
		return err
	}
	item, err := s.prepareAccount(record)
	if err != nil {
		return err
	}
	encryptedSession, err := s.cipher.Encrypt(accountSessionSecretPurpose, record.ID, session)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var order int
	err = tx.QueryRow(`SELECT sort_order FROM pikpak_accounts WHERE id=?`, record.ID).Scan(&order)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.QueryRow(`SELECT COALESCE(MAX(sort_order), -1) + 1 FROM pikpak_accounts`).Scan(&order); err != nil {
			return err
		}
		if err := insertAccount(tx, item, order); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else if err := upsertAccount(tx, item, order); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO pikpak_account_sessions(account_id,session_encrypted,updated_at) VALUES(?,?,?)
		 ON CONFLICT(account_id) DO UPDATE SET session_encrypted=excluded.session_encrypted,updated_at=excluded.updated_at`,
		record.ID, encryptedSession, s.now().Unix(),
	); err != nil {
		return err
	}
	return tx.Commit()
}

// Update changes an account without changing its order or session.
func (s *accountStore) Update(record accountRecord) error {
	if err := s.validate(); err != nil {
		return err
	}
	return s.updateWithExecutor(s.db, record)
}

func (s *accountStore) updateTx(tx *sql.Tx, record accountRecord) error {
	if tx == nil {
		return errors.New("account transaction is nil")
	}
	return s.updateWithExecutor(tx, record)
}

type accountUpdateExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func (s *accountStore) updateWithExecutor(executor accountUpdateExecutor, record accountRecord) error {
	item, err := s.prepareAccount(record)
	if err != nil {
		return err
	}

	result, err := executor.Exec(`
		UPDATE pikpak_accounts SET
			username = ?, password_encrypted = ?, session_file = ?, status = ?, premium = ?,
			premium_type = ?, premium_until = ?, premium_error = ?, premium_checked_at = ?,
			traffic_limit = ?, traffic_used = ?, traffic_period = ?, last_error = ?, last_failed_at = ?,
			credential_checked_at = ?, credential_next_check_at = ?, credential_check_error = ?,
			parse_errors_json = ?, created_at = ?, updated_at = ?
		WHERE id = ?`, accountValueArgs(item, false)...)
	if err != nil {
		return fmt.Errorf("update PikPak account %q: %w", record.ID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("update PikPak account %q: rows affected: %w", record.ID, err)
	}
	if changed == 0 {
		return fmt.Errorf("update PikPak account %q: %w", record.ID, sql.ErrNoRows)
	}
	return nil
}

// Delete removes an account. Its session is removed by the schema's cascade.
func (s *accountStore) Delete(id string) error {
	if err := s.validate(); err != nil {
		return err
	}
	result, err := s.db.Exec(`DELETE FROM pikpak_accounts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete PikPak account %q: %w", id, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete PikPak account %q: rows affected: %w", id, err)
	}
	if changed == 0 {
		return fmt.Errorf("delete PikPak account %q: %w", id, sql.ErrNoRows)
	}
	return nil
}

// SessionStore returns a PikPak session adapter scoped to one account.
func (s *accountStore) SessionStore(accountID string) pikpak.SessionStore {
	return &databasePikPakSessionStore{
		db:        s.db,
		cipher:    s.cipher,
		accountID: accountID,
		now:       s.now,
	}
}

// RotateSecrets validates every account secret before atomically rewriting all
// envelopes that were encrypted with a configured previous key.
func (s *accountStore) RotateSecrets() error {
	if err := s.validate(); err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("rotate PikPak secrets: begin transaction: %w", err)
	}
	defer tx.Rollback()

	updates, err := s.collectRotationUpdates(tx)
	if err != nil {
		return err
	}
	for i := range updates {
		updates[i].replacement, err = s.cipher.Encrypt(updates[i].purpose, updates[i].accountID, updates[i].plaintext)
		if err != nil {
			return fmt.Errorf("rotate PikPak %s for account %q: %w", updates[i].kind, updates[i].accountID, err)
		}
	}
	for _, update := range updates {
		var result sql.Result
		switch update.kind {
		case "password":
			result, err = tx.Exec(
				`UPDATE pikpak_accounts SET password_encrypted = ? WHERE id = ? AND password_encrypted = ?`,
				update.replacement, update.accountID, update.original,
			)
		case "session":
			result, err = tx.Exec(
				`UPDATE pikpak_account_sessions SET session_encrypted = ? WHERE account_id = ? AND session_encrypted = ?`,
				update.replacement, update.accountID, update.original,
			)
		default:
			return fmt.Errorf("rotate PikPak secrets: unsupported secret kind %q", update.kind)
		}
		if err != nil {
			return fmt.Errorf("rotate PikPak %s for account %q: %w", update.kind, update.accountID, err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("rotate PikPak %s for account %q: rows affected: %w", update.kind, update.accountID, err)
		}
		if changed != 1 {
			return fmt.Errorf("rotate PikPak %s for account %q: secret changed concurrently", update.kind, update.accountID)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("rotate PikPak secrets: commit: %w", err)
	}
	return nil
}

func (s *accountStore) validate() error {
	if s == nil || s.db == nil {
		return errors.New("account store database is not initialized")
	}
	if s.cipher == nil {
		return errors.New("account store secret cipher is not initialized")
	}
	return nil
}

type storedAccountRecord struct {
	record            accountRecord
	passwordEncrypted string
	parseErrorsJSON   string
}

func (s *accountStore) prepareAccount(record accountRecord) (storedAccountRecord, error) {
	if record.ID == "" {
		return storedAccountRecord{}, errors.New("PikPak account id is required")
	}
	passwordEncrypted, err := s.cipher.Encrypt(accountPasswordSecretPurpose, record.ID, []byte(record.Password))
	if err != nil {
		return storedAccountRecord{}, fmt.Errorf("encrypt PikPak password for account %q: %w", record.ID, err)
	}
	parseErrors, err := json.Marshal(record.ParseErrors)
	if err != nil {
		return storedAccountRecord{}, fmt.Errorf("encode PikPak parse errors for account %q: %w", record.ID, err)
	}
	return storedAccountRecord{
		record:            record,
		passwordEncrypted: passwordEncrypted,
		parseErrorsJSON:   string(parseErrors),
	}, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *accountStore) scanAccount(scanner rowScanner) (accountRecord, error) {
	var (
		record            accountRecord
		passwordEncrypted string
		premium           int
		parseErrorsJSON   string
		createdAt         int64
		updatedAt         int64
	)
	if err := scanner.Scan(
		&record.ID, &record.Username, &passwordEncrypted, &record.SessionFile,
		&record.Status, &premium, &record.PremiumType, &record.PremiumUntil,
		&record.PremiumError, &record.PremiumCheckedAt, &record.TrafficLimit,
		&record.TrafficUsed, &record.TrafficPeriod, &record.LastError,
		&record.LastFailedAt, &record.CredentialCheckedAt,
		&record.CredentialNextCheckAt, &record.CredentialCheckError,
		&parseErrorsJSON, &createdAt, &updatedAt,
	); err != nil {
		return accountRecord{}, fmt.Errorf("scan PikPak account: %w", err)
	}

	password, err := s.cipher.Decrypt(accountPasswordSecretPurpose, record.ID, passwordEncrypted)
	if err != nil {
		return accountRecord{}, fmt.Errorf("decrypt PikPak password for account %q: %w", record.ID, err)
	}
	if err := json.Unmarshal([]byte(parseErrorsJSON), &record.ParseErrors); err != nil {
		return accountRecord{}, fmt.Errorf("decode PikPak parse errors for account %q: %w", record.ID, err)
	}
	record.Password = string(password)
	record.Premium = premium != 0
	record.CreatedAt = timeFromUnix(createdAt)
	record.UpdatedAt = timeFromUnix(updatedAt)
	return record, nil
}

func insertAccount(tx *sql.Tx, item storedAccountRecord, order int) error {
	_, err := tx.Exec(`
		INSERT INTO pikpak_accounts(
			id, sort_order, username, password_encrypted, session_file, status, premium,
			premium_type, premium_until, premium_error, premium_checked_at,
			traffic_limit, traffic_used, traffic_period, last_error, last_failed_at,
			credential_checked_at, credential_next_check_at, credential_check_error,
			parse_errors_json, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		accountValueArgs(item, true, order)...,
	)
	return err
}

func upsertAccount(tx *sql.Tx, item storedAccountRecord, order int) error {
	_, err := tx.Exec(`
		INSERT INTO pikpak_accounts(
			id, sort_order, username, password_encrypted, session_file, status, premium,
			premium_type, premium_until, premium_error, premium_checked_at,
			traffic_limit, traffic_used, traffic_period, last_error, last_failed_at,
			credential_checked_at, credential_next_check_at, credential_check_error,
			parse_errors_json, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			sort_order = excluded.sort_order,
			username = excluded.username,
			password_encrypted = excluded.password_encrypted,
			session_file = excluded.session_file,
			status = excluded.status,
			premium = excluded.premium,
			premium_type = excluded.premium_type,
			premium_until = excluded.premium_until,
			premium_error = excluded.premium_error,
			premium_checked_at = excluded.premium_checked_at,
			traffic_limit = excluded.traffic_limit,
			traffic_used = excluded.traffic_used,
			traffic_period = excluded.traffic_period,
			last_error = excluded.last_error,
			last_failed_at = excluded.last_failed_at,
			credential_checked_at = excluded.credential_checked_at,
			credential_next_check_at = excluded.credential_next_check_at,
			credential_check_error = excluded.credential_check_error,
			parse_errors_json = excluded.parse_errors_json,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at`,
		accountValueArgs(item, true, order)...,
	)
	return err
}

func accountValueArgs(item storedAccountRecord, includeIdentity bool, order ...int) []any {
	record := item.record
	values := []any{
		record.Username,
		item.passwordEncrypted,
		record.SessionFile,
		string(record.Status),
		boolInt(record.Premium),
		record.PremiumType,
		record.PremiumUntil,
		record.PremiumError,
		record.PremiumCheckedAt,
		record.TrafficLimit,
		record.TrafficUsed,
		record.TrafficPeriod,
		record.LastError,
		record.LastFailedAt,
		record.CredentialCheckedAt,
		record.CredentialNextCheckAt,
		record.CredentialCheckError,
		item.parseErrorsJSON,
		timeToUnix(record.CreatedAt),
		timeToUnix(record.UpdatedAt),
	}
	if includeIdentity {
		values = append([]any{record.ID, order[0]}, values...)
	} else {
		values = append(values, record.ID)
	}
	return values
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func timeToUnix(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.Unix()
}

func timeFromUnix(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
}

type secretRotationUpdate struct {
	kind        string
	purpose     string
	accountID   string
	original    string
	plaintext   []byte
	replacement string
}

func (s *accountStore) collectRotationUpdates(tx *sql.Tx) ([]secretRotationUpdate, error) {
	var secrets []secretRotationUpdate
	passwords, err := tx.Query(`SELECT id, password_encrypted FROM pikpak_accounts ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("rotate PikPak secrets: list passwords: %w", err)
	}
	for passwords.Next() {
		var secret secretRotationUpdate
		secret.kind = "password"
		secret.purpose = accountPasswordSecretPurpose
		if err := passwords.Scan(&secret.accountID, &secret.original); err != nil {
			passwords.Close()
			return nil, fmt.Errorf("rotate PikPak secrets: scan password: %w", err)
		}
		secrets = append(secrets, secret)
	}
	if err := passwords.Err(); err != nil {
		passwords.Close()
		return nil, fmt.Errorf("rotate PikPak secrets: iterate passwords: %w", err)
	}
	if err := passwords.Close(); err != nil {
		return nil, fmt.Errorf("rotate PikPak secrets: close passwords: %w", err)
	}

	sessions, err := tx.Query(`SELECT account_id, session_encrypted FROM pikpak_account_sessions ORDER BY account_id`)
	if err != nil {
		return nil, fmt.Errorf("rotate PikPak secrets: list sessions: %w", err)
	}
	for sessions.Next() {
		var secret secretRotationUpdate
		secret.kind = "session"
		secret.purpose = accountSessionSecretPurpose
		if err := sessions.Scan(&secret.accountID, &secret.original); err != nil {
			sessions.Close()
			return nil, fmt.Errorf("rotate PikPak secrets: scan session: %w", err)
		}
		secrets = append(secrets, secret)
	}
	if err := sessions.Err(); err != nil {
		sessions.Close()
		return nil, fmt.Errorf("rotate PikPak secrets: iterate sessions: %w", err)
	}
	if err := sessions.Close(); err != nil {
		return nil, fmt.Errorf("rotate PikPak secrets: close sessions: %w", err)
	}

	updates := make([]secretRotationUpdate, 0, len(secrets))
	for _, secret := range secrets {
		plaintext, err := s.cipher.Decrypt(secret.purpose, secret.accountID, secret.original)
		if err != nil {
			return nil, fmt.Errorf("validate PikPak %s for account %q: %w", secret.kind, secret.accountID, err)
		}
		needsRotation, err := s.cipher.NeedsRotation(secret.original)
		if err != nil {
			return nil, fmt.Errorf("inspect PikPak %s for account %q: %w", secret.kind, secret.accountID, err)
		}
		if needsRotation {
			secret.plaintext = plaintext
			updates = append(updates, secret)
		}
	}
	return updates, nil
}

type databasePikPakSessionStore struct {
	db        *sql.DB
	cipher    *SecretCipher
	accountID string
	now       func() time.Time
}

func (s *databasePikPakSessionStore) Load() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	var envelope string
	if err := s.db.QueryRow(
		`SELECT session_encrypted FROM pikpak_account_sessions WHERE account_id = ?`, s.accountID,
	).Scan(&envelope); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, &os.PathError{Op: "load PikPak session", Path: s.accountID, Err: os.ErrNotExist}
		}
		return nil, fmt.Errorf("load PikPak session for account %q: %w", s.accountID, err)
	}
	plaintext, err := s.cipher.Decrypt(accountSessionSecretPurpose, s.accountID, envelope)
	if err != nil {
		return nil, fmt.Errorf("decrypt PikPak session for account %q: %w", s.accountID, err)
	}
	return plaintext, nil
}

func (s *databasePikPakSessionStore) Save(data []byte) error {
	if err := s.validate(); err != nil {
		return err
	}
	envelope, err := s.cipher.Encrypt(accountSessionSecretPurpose, s.accountID, data)
	if err != nil {
		return fmt.Errorf("encrypt PikPak session for account %q: %w", s.accountID, err)
	}
	_, err = s.db.Exec(`
		INSERT INTO pikpak_account_sessions(account_id, session_encrypted, updated_at)
		VALUES(?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET
			session_encrypted = excluded.session_encrypted,
			updated_at = excluded.updated_at`,
		s.accountID, envelope, s.now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("save PikPak session for account %q: %w", s.accountID, err)
	}
	return nil
}

func (s *databasePikPakSessionStore) Delete() error {
	if err := s.validate(); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM pikpak_account_sessions WHERE account_id = ?`, s.accountID); err != nil {
		return fmt.Errorf("delete PikPak session for account %q: %w", s.accountID, err)
	}
	return nil
}

func (s *databasePikPakSessionStore) validate() error {
	if s == nil || s.db == nil {
		return errors.New("PikPak session store database is not initialized")
	}
	if s.cipher == nil {
		return errors.New("PikPak session store secret cipher is not initialized")
	}
	if s.accountID == "" {
		return errors.New("PikPak session store account id is required")
	}
	if s.now == nil {
		s.now = time.Now
	}
	return nil
}

// stagingSessionStore holds a just-authenticated session until its account and
// session can be committed together by the application layer.
type stagingSessionStore struct {
	mu   sync.RWMutex
	data []byte
	set  bool
}

func newStagingSessionStore() *stagingSessionStore {
	return &stagingSessionStore{}
}

func (s *stagingSessionStore) Load() ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.set {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), s.data...), nil
}

func (s *stagingSessionStore) Save(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = append(s.data[:0], data...)
	s.set = true
	return nil
}

func (s *stagingSessionStore) Delete() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data = nil
	s.set = false
	return nil
}

var _ pikpak.SessionStore = (*databasePikPakSessionStore)(nil)
var _ pikpak.SessionStore = (*stagingSessionStore)(nil)
