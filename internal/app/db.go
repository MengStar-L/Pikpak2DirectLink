package app

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// openDatabase opens (creating if needed) the SQLite database used for CDK
// storage and applies the schema. modernc.org/sqlite is a pure-Go driver, so the
// binary still cross-compiles with CGO disabled.
func openDatabase(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// A single connection serializes all access, which sidesteps SQLITE_BUSY
	// under the polling UI without needing WAL juggling.
	db.SetMaxOpenConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func migrate(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS cdks (
    code        TEXT PRIMARY KEY,
    remaining   INTEGER NOT NULL,
    used        INTEGER NOT NULL DEFAULT 0,
    expires_at  INTEGER NOT NULL,
    created_at  INTEGER NOT NULL
);`
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate cdks: %w", err)
	}
	return nil
}
