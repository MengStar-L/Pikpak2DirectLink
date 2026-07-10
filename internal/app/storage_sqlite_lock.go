package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func ensureSQLiteWriteUnlocked(ctx context.Context, path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	header := make([]byte, len("SQLite format 3\x00"))
	_, readErr := io.ReadFull(file, header)
	closeErr := file.Close()
	if readErr != nil || string(header) != "SQLite format 3\x00" {
		// A corrupt current target is exactly what restore must be able to replace.
		return nil
	}
	if closeErr != nil {
		return closeErr
	}

	dsn, err := sqliteRestoreProbeDSN(path)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		return classifySQLiteRestoreProbeError(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		return classifySQLiteRestoreProbeError(err)
	}
	if _, err := conn.ExecContext(context.Background(), "ROLLBACK"); err != nil {
		return fmt.Errorf("release SQLite restore probe: %w", err)
	}
	return nil
}

func sqliteRestoreProbeDSN(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	uriPath := filepath.ToSlash(absolute)
	if filepath.VolumeName(absolute) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	query := make(url.Values)
	query.Set("mode", "rw")
	query.Add("_pragma", "busy_timeout(0)")
	return (&url.URL{Scheme: "file", Path: uriPath, RawQuery: query.Encode()}).String(), nil
}

func classifySQLiteRestoreProbeError(err error) error {
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "locked") || strings.Contains(lower, "busy") {
		return fmt.Errorf("%w: active SQLite transaction: %v", errStorageInUse, err)
	}
	if strings.Contains(lower, "not a database") || strings.Contains(lower, "malformed") {
		return nil
	}
	return fmt.Errorf("inspect current SQLite database before restore: %w", err)
}
