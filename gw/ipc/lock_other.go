//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly || windows)

package ipc

import (
	"fmt"
	"io"
	"runtime"
)

// lockFile: no outbox lock on this system, so the gateway refuses to run rather than risk two writers.
func lockFile(path string) (io.Closer, error) {
	return nil, fmt.Errorf("outbox: no lock for %s on %s", path, runtime.GOOS)
}
