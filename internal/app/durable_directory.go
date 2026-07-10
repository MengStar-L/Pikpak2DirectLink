package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func durableMkdirAll(path string, perm os.FileMode) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	var missing []string
	current := absolute
	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return fmt.Errorf("path is not a directory: %s", current)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return fmt.Errorf("cannot find an existing parent for %s", path)
		}
		missing = append(missing, current)
		current = parent
	}
	for index := len(missing) - 1; index >= 0; index-- {
		directory := missing[index]
		if err := os.Mkdir(directory, perm); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return err
			}
			info, statErr := os.Lstat(directory)
			if statErr != nil {
				return statErr
			}
			if !info.IsDir() {
				return fmt.Errorf("path is not a directory: %s", directory)
			}
		}
		if err := syncDirectory(filepath.Dir(directory)); err != nil {
			return err
		}
	}
	return nil
}
