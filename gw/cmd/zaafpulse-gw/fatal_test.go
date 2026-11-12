package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// hasFatalLine reports whether the log holds a line that starts with "fatal: ".
func hasFatalLine(log string) bool {
	for line := range strings.Lines(log) {
		if strings.HasPrefix(line, "fatal: ") {
			return true
		}
	}
	return false
}

// Every exit a restart cannot fix (1, 2 and 5 here; 4 is the front's) comes with a "fatal: " line, which the supervisor
// needs before it stops restarting (P4a E8). A stdin read error exits 1 without one: the control plane is gone.
func TestPermanentExitsComeWithAFatalLine(t *testing.T) {
	held := t.TempDir()
	box, err := ipc.OpenOutbox(held)
	if err != nil {
		t.Fatal(err)
	}
	defer box.Close()
	damaged := t.TempDir()
	if err := os.WriteFile(filepath.Join(damaged, "00000000000000000001.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	refused := `{"v":1,"type":"hello.refused","seq":1,"ts":0,"re":"h1","body":{"reason":"no"}}` + "\n"
	for _, c := range []struct {
		name  string
		stdin string
		args  []string
		code  int
	}{
		{"outbox in use", "", []string{"--outbox", held}, ipc.ExitOutboxInUse},
		{"damaged outbox", "", []string{"--outbox", damaged}, ipc.ExitDamaged},
		{"refused", refused, []string{"--outbox", t.TempDir()}, ipc.ExitRefused},
		{"bad command line", "", []string{"--no-such-flag"}, ipc.ExitRefused},
	} {
		code, log := runGw(t, strings.NewReader(c.stdin), c.args...)
		if code != c.code || !hasFatalLine(log) {
			t.Errorf("%s: exit %d (want %d), log %q", c.name, code, c.code, log)
		}
	}
	if code, log := runGw(t, failingReader{}, "--outbox", t.TempDir()); code != ipc.ExitDamaged || hasFatalLine(log) {
		t.Errorf("stdin read error: exit %d, log %q", code, log)
	}
}
