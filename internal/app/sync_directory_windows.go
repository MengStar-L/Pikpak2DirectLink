//go:build windows

package app

import "golang.org/x/sys/windows"

func syncDirectory(path string) error {
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(
		pathUTF16,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	if err := windows.FlushFileBuffers(handle); err != nil {
		// Windows can reject flushing directory handles even though file data was
		// flushed and MoveFileEx made the rename atomic.
		if err == windows.ERROR_ACCESS_DENIED || err == windows.ERROR_INVALID_HANDLE || err == windows.ERROR_INVALID_FUNCTION {
			return nil
		}
		return err
	}
	return nil
}
