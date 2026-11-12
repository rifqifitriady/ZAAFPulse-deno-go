package ipc

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// E1: one writer per outbox. A second open of the same folder, in this process or another, is refused while the first
// holds the lock, and succeeds once the first is closed.
func TestOutboxSecondOpenIsRefused(t *testing.T) {
	dir := t.TempDir()
	o, err := openOutbox(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".lock")); err != nil {
		t.Fatalf("no lock file: %v", err)
	}
	o2, err := openOutbox(t, dir)
	if !errors.Is(err, ErrOutboxInUse) || o2 != nil {
		t.Fatalf("second open: %v, %v", o2, err)
	}
	if err := o.Close(); err != nil {
		t.Fatal(err)
	}
	o3, err := openOutbox(t, dir)
	if err != nil {
		t.Fatalf("open after close: %v", err)
	}
	if o3.Stream() != o.Stream() {
		t.Fatal("the stream changed")
	}
}

// A failed open releases the lock: the next open is not told the outbox is in use.
func TestOutboxFailedOpenReleasesTheLock(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "stream.json"), []byte(`{"stream":"x"}`), 0o600)
	if _, err := openOutbox(t, dir); err == nil || errors.Is(err, ErrOutboxInUse) {
		t.Fatalf("first open: %v", err)
	}
	if _, err := openOutbox(t, dir); errors.Is(err, ErrOutboxInUse) {
		t.Fatal("a failed open kept the lock")
	}
}

// E2: segments present and no stream.json means damaged: no new stream is started over old events.
func TestOutboxSegmentsWithoutStreamFileAreDamaged(t *testing.T) {
	dir := t.TempDir()
	o, _ := openOutbox(t, dir)
	appendN(t, o, 3)
	_ = o.Close()
	if err := os.Remove(filepath.Join(dir, "stream.json")); err != nil {
		t.Fatal(err)
	}
	o2, err := openOutbox(t, dir)
	if !errors.Is(err, ErrOutboxDamaged) || o2 != nil {
		t.Fatalf("open: %v, %v", o2, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stream.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a new stream.json was written: %v", err)
	}
}

func TestOutboxDamagedStreamFileIsErrOutboxDamaged(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "stream.json"), []byte(`{"stream":"x"}`), 0o600)
	if _, err := openOutbox(t, dir); !errors.Is(err, ErrOutboxDamaged) {
		t.Fatalf("got %v", err)
	}
}

func TestExitCodes(t *testing.T) {
	got := []int{ExitDamaged, ExitRefused, ExitProtocol, ExitPortInUse, ExitOutboxInUse}
	for i, c := range got {
		if c != i+1 {
			t.Fatalf("exit codes %v", got)
		}
	}
}
