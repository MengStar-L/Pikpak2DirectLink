//go:build windows

package app

import (
	"os"

	"golang.org/x/sys/windows"
)

func tryLockStorageFile(file *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, overlapped,
	)
}

func unlockStorageFile(file *os.File) error {
	overlapped := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, overlapped)
}
