package egress

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestIsPrivateFixtures replays lib/net.js isPrivateAddress (gw/testdata/egress/private.json, written by
// scripts/fixtures/gen-egress.js) against IsPrivate.
func TestIsPrivateFixtures(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "egress", "private.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Fn  string `json:"fn"`
		Out []struct {
			IP      string `json:"ip"`
			Private bool   `json:"private"`
		} `json:"out"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if fx.Fn != "isPrivateAddress" || len(fx.Out) < 80 {
		t.Fatalf("fixture: fn %q, %d cases", fx.Fn, len(fx.Out))
	}
	for _, c := range fx.Out {
		ip := net.ParseIP(c.IP)
		// The corpus holds canonical text only (the form the dialer sees); a mapped address prints as its IPv4 form.
		if ip != nil && ip.String() != c.IP && "::ffff:"+ip.String() != c.IP {
			t.Errorf("%q is not canonical (Go prints %q)", c.IP, ip.String())
		}
		if got := IsPrivate(ip); got != c.Private {
			t.Errorf("IsPrivate(%q) = %v, lib/net.js says %v", c.IP, got, c.Private)
		}
	}
}

func TestIsPrivateByteForms(t *testing.T) {
	// The 16-byte form of an IPv4 address (what the resolver hands the dialer) is judged as the IPv4 address.
	if !IsPrivate(net.IPv4(127, 0, 0, 1).To16()) || IsPrivate(net.IPv4(8, 8, 8, 8).To16()) {
		t.Fatal("16-byte IPv4")
	}
	if !IsPrivate(nil) || !IsPrivate(net.IP{1, 2, 3}) {
		t.Fatal("a non-address must be private")
	}
}
