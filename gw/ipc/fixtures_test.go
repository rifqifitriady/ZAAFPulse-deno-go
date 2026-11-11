package ipc

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var fixtureRoot = filepath.Join("..", "..", "ipc", "v1", "fixtures")

// Folders of fixtures that are not message types: envelope-level cases and the budget decisions (budget_test.go).
var notTypes = map[string]bool{"envelope": true, "budget-decide": true}

func TestEveryTypeHasFixtures(t *testing.T) {
	entries, err := os.ReadDir(fixtureRoot)
	if err != nil {
		t.Fatal(err)
	}
	var folders []string
	for _, e := range entries {
		if e.IsDir() && !notTypes[e.Name()] {
			folders = append(folders, e.Name())
		}
	}
	if !slices.Equal(folders, Types()) {
		t.Fatalf("fixture folders %v, message types %v", folders, Types())
	}
	for _, typ := range Types() {
		files, _ := os.ReadDir(filepath.Join(fixtureRoot, typ))
		var valid, invalid bool
		for _, f := range files {
			valid = valid || strings.HasPrefix(f.Name(), "valid")
			invalid = invalid || strings.HasPrefix(f.Name(), "invalid")
		}
		if !valid || !invalid {
			t.Errorf("%s: needs a valid and an invalid fixture", typ)
		}
	}
}

// Every valid fixture decodes strictly and encodes to the same JSON value; every invalid one is refused; an
// invalid-unknown-field fixture decodes leniently (the v1 version rule).
func TestFixtures(t *testing.T) {
	entries, _ := os.ReadDir(fixtureRoot)
	n := 0
	for _, d := range entries {
		if !d.IsDir() || d.Name() == "budget-decide" {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(fixtureRoot, d.Name()))
		for _, f := range files {
			folder, name := d.Name(), f.Name()
			n++
			t.Run(folder+"/"+name, func(t *testing.T) {
				data, err := os.ReadFile(filepath.Join(fixtureRoot, folder, name))
				if err != nil {
					t.Fatal(err)
				}
				data = bytes.TrimSpace(data)
				msg, err := Decode(data, true)
				switch {
				case strings.HasPrefix(name, "valid"):
					if err != nil {
						t.Fatalf("refused: %v", err)
					}
					if msg.Unknown || (folder != "envelope" && msg.Type != folder) {
						t.Fatalf("type %q in folder %q", msg.Type, folder)
					}
					out, err := Encode(msg.Envelope, msg.Body)
					if err != nil {
						t.Fatal(err)
					}
					var a, b any
					_ = json.Unmarshal(data, &a)
					_ = json.Unmarshal(out, &b)
					if !reflect.DeepEqual(a, b) {
						t.Fatalf("the round trip changed it:\n in: %s\nout: %s", data, out)
					}
				case strings.HasPrefix(name, "invalid"):
					if err == nil && !msg.Unknown {
						t.Fatal("accepted")
					}
				default:
					t.Fatal("a fixture is named valid[-name].json or invalid[-name].json")
				}
				if strings.HasPrefix(name, "invalid-unknown-field") {
					if _, err := Decode(data, false); err != nil {
						t.Fatalf("the lenient decode refused it: %v", err)
					}
				}
			})
		}
	}
	if n < 57 {
		t.Fatalf("only %d fixtures", n)
	}
}

func TestUnknownTypeIsReportedNotRefused(t *testing.T) {
	msg, err := Decode([]byte(`{"v":1,"type":"telemetry.v9","seq":1,"ts":0,"body":{"x":1}}`), true)
	if err != nil || !msg.Unknown || msg.Type != "telemetry.v9" {
		t.Fatalf("got %+v, %v", msg, err)
	}
}
