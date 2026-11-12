//go:build windows

package ipc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// errSharingViolation is ERROR_SHARING_VIOLATION: another handle has the file open with a share mode that excludes us.
const errSharingViolation = syscall.Errno(32)

// lockFile opens path (created when missing) with share mode 0: no other handle, in this process or another, can open
// it until the returned file is closed, and Windows closes it when the process dies.
func lockFile(path string) (io.Closer, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, errSharingViolation) {
			return nil, fmt.Errorf("%w (%s is open)", ErrOutboxInUse, path)
		}
		return nil, fmt.Errorf("outbox: lock %s: %w", path, err)
	}
	return os.NewFile(uintptr(h), path), nil
}
