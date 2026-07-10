//go:build !windows

package app

import (
	"os"
	"syscall"
)

func tryLockStorageFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

func unlockStorageFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}
