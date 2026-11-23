//go:build race

package guard

import "time"

// raceEnabled: the race detector slows the matcher about tenfold, so the 20 MB inputs run without it only.
const raceEnabled = true

// The backstop is wall-clock time and the race detector slows the matcher about tenfold, so it is widened there. The
// tests of the deadline itself use an injected clock.
func init() { requestDeadline = 60 * time.Second }
