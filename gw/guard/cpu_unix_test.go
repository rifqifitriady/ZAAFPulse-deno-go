//go:build unix

package guard

import (
	"syscall"
	"time"
)

// processCPU is the user plus system CPU time of this process so far.
func processCPU() time.Duration {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return -1
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}
