//go:build race

package guard

// raceEnabled: the race detector slows the matcher about tenfold, so the 20 MB inputs run without it only.
const raceEnabled = true
