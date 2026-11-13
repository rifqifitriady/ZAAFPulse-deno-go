//go:build !unix

package guard

import "time"

// processCPU is not measured here (-1): the CPU bound is checked on Unix only.
func processCPU() time.Duration { return -1 }
