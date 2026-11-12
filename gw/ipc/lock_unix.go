//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package ipc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// lockFile takes an exclusive lock on path (created when missing) for as long as the returned file stays open. flock
// locks belong to the open file, so a second open in the same process is refused as well, and the kernel drops the
// lock when the process dies.
func lockFile(path string) (io.Closer, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (%s is locked)", ErrOutboxInUse, path)
		}
		return nil, fmt.Errorf("outbox: lock %s: %w", path, err)
	}
	return f, nil
}
