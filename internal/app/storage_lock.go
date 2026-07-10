package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var errStorageInUse = errors.New("application storage is in use")

type storageFileLock struct {
	file *os.File
}

func acquireStorageFileLock(databasePath string) (*storageFileLock, error) {
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" {
		return nil, errors.New("database path is empty")
	}
	if databasePath == ":memory:" {
		return nil, nil
	}
	absolute, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, err
	}
	if err := durableMkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, err
	}
	lockPath := absolute + ".lock"
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open storage lock: %w", err)
	}
	if err := tryLockStorageFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("%w: %s: %v", errStorageInUse, absolute, err)
	}
	return &storageFileLock{file: file}, nil
}

func (l *storageFileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	err := errors.Join(unlockStorageFile(l.file), l.file.Close())
	l.file = nil
	return err
}
