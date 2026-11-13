//go:build race

package guard

import "time"

// raceEnabled: the race detector slows the matcher about tenfold, so the 20 MB inputs run without it only.
const raceEnabled = true

// The deadline is wall-clock time; under the race detector the 1 MB bodies of the tests take longer than 2 s, so it is
// widened there. The tests of the deadline itself use an injected clock.
func init() { requestDeadline = 30 * time.Second }
