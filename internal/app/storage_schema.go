package app

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	storageSchemaVersion       = 1
	storageSchemaMigrationName = "secure_storage_foundation"
)

const schemaMigrationsDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    applied_at INTEGER NOT NULL
);`

const storageSchemaV1 = `
CREATE TABLE pikpak_accounts (
    id                       TEXT PRIMARY KEY,
    sort_order               INTEGER NOT NULL UNIQUE CHECK(sort_order >= 0),
    username                 TEXT NOT NULL,
    password_encrypted       TEXT NOT NULL,
    session_file             TEXT NOT NULL DEFAULT '',
    status                   TEXT NOT NULL DEFAULT 'available',
    premium                  INTEGER NOT NULL DEFAULT 0 CHECK(premium IN (0, 1)),
    premium_type             TEXT NOT NULL DEFAULT '',
    premium_until            TEXT NOT NULL DEFAULT '',
    premium_error            TEXT NOT NULL DEFAULT '',
    premium_checked_at       TEXT NOT NULL DEFAULT '',
    traffic_limit            INTEGER NOT NULL DEFAULT 0 CHECK(traffic_limit >= 0),
    traffic_used             INTEGER NOT NULL DEFAULT 0 CHECK(traffic_used >= 0),
    traffic_period           TEXT NOT NULL DEFAULT '',
    last_error               TEXT NOT NULL DEFAULT '',
    last_failed_at           TEXT NOT NULL DEFAULT '',
    credential_checked_at    TEXT NOT NULL DEFAULT '',
    credential_next_check_at TEXT NOT NULL DEFAULT '',
    credential_check_error   TEXT NOT NULL DEFAULT '',
    parse_errors_json        TEXT NOT NULL DEFAULT '[]',
    created_at               INTEGER NOT NULL,
    updated_at               INTEGER NOT NULL
);

CREATE TABLE pikpak_account_sessions (
    account_id        TEXT PRIMARY KEY,
    session_encrypted TEXT NOT NULL,
    updated_at        INTEGER NOT NULL,
    FOREIGN KEY(account_id) REFERENCES pikpak_accounts(id) ON DELETE CASCADE
);

CREATE TABLE admin_credentials (
    id            INTEGER PRIMARY KEY CHECK(id = 1),
    password_hash TEXT NOT NULL,
    updated_at    INTEGER NOT NULL
);

CREATE TABLE admin_sessions (
    token_hash TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);
CREATE INDEX idx_admin_sessions_expires
ON admin_sessions(expires_at);

CREATE TABLE app_secrets (
    key        TEXT PRIMARY KEY,
    ciphertext TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE resolve_jobs (
    id                 TEXT PRIMARY KEY,
    parent_job_id      TEXT,
    child_index        INTEGER CHECK(child_index >= 0),
    owner_type         TEXT NOT NULL,
    owner_id           TEXT NOT NULL,
    kind               TEXT NOT NULL,
    mode               TEXT NOT NULL,
    status             TEXT NOT NULL,
    phase              TEXT NOT NULL DEFAULT '',
    failure_code       TEXT NOT NULL DEFAULT '',
    error_category     TEXT NOT NULL DEFAULT '',
    error_message      TEXT NOT NULL DEFAULT '',
    result_count       INTEGER NOT NULL DEFAULT 0 CHECK(result_count >= 0),
    charged_bytes      INTEGER NOT NULL DEFAULT 0 CHECK(charged_bytes >= 0),
    payload_encrypted  TEXT,
    details_expires_at INTEGER,
    record_expires_at  INTEGER NOT NULL,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL,
    completed_at       INTEGER,
    FOREIGN KEY(parent_job_id) REFERENCES resolve_jobs(id) ON DELETE CASCADE,
    CHECK(
        (parent_job_id IS NULL AND child_index IS NULL) OR
        (parent_job_id IS NOT NULL AND child_index IS NOT NULL)
    ),
    UNIQUE(parent_job_id, child_index)
);
CREATE INDEX idx_resolve_jobs_owner_created
ON resolve_jobs(owner_type, owner_id, created_at DESC);
CREATE INDEX idx_resolve_jobs_parent_child
ON resolve_jobs(parent_job_id, child_index);
CREATE INDEX idx_resolve_jobs_status
ON resolve_jobs(status);
CREATE INDEX idx_resolve_jobs_details_expiry
ON resolve_jobs(details_expires_at)
WHERE details_expires_at IS NOT NULL;
CREATE INDEX idx_resolve_jobs_record_expiry
ON resolve_jobs(record_expires_at);

CREATE TABLE storage_migration_state (
    id           INTEGER PRIMARY KEY CHECK(id = 1),
    status       TEXT NOT NULL,
    phase        TEXT NOT NULL DEFAULT '',
    backup_id    TEXT NOT NULL DEFAULT '',
    backup_path  TEXT NOT NULL DEFAULT '',
    last_error   TEXT NOT NULL DEFAULT '',
    started_at   INTEGER,
    completed_at INTEGER,
    updated_at   INTEGER NOT NULL
);

CREATE TABLE backup_runs (
    id           TEXT PRIMARY KEY,
    kind         TEXT NOT NULL,
    status       TEXT NOT NULL,
    path         TEXT NOT NULL DEFAULT '',
    size_bytes   INTEGER NOT NULL DEFAULT 0 CHECK(size_bytes >= 0),
    sha256       TEXT NOT NULL DEFAULT '',
    error        TEXT NOT NULL DEFAULT '',
    started_at   INTEGER NOT NULL,
    completed_at INTEGER
);
CREATE INDEX idx_backup_runs_started
ON backup_runs(started_at DESC);`

func migrateStorageSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schemaMigrationsDDL); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	var name string
	err = tx.QueryRow(
		`SELECT name FROM schema_migrations WHERE version = ?`, storageSchemaVersion,
	).Scan(&name)
	switch {
	case err == nil:
		if name != storageSchemaMigrationName {
			return fmt.Errorf("schema migration %d is %q, want %q", storageSchemaVersion, name, storageSchemaMigrationName)
		}
		return tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}

	if _, err := tx.Exec(storageSchemaV1); err != nil {
		return fmt.Errorf("apply migration %d: %w", storageSchemaVersion, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations(version, name, applied_at) VALUES(?, ?, ?)`,
		storageSchemaVersion, storageSchemaMigrationName, time.Now().Unix(),
	); err != nil {
		return fmt.Errorf("record migration %d: %w", storageSchemaVersion, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", storageSchemaVersion, err)
	}
	return nil
}
