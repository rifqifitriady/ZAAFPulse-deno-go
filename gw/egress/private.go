package egress

import (
	"net"
	"strings"
)

// IsPrivate is lib/net.js isPrivateAddress for an address the dialer is about to connect to: true for the addresses a
// public URL must never reach (this network, private, loopback, carrier-grade NAT, link-local and cloud metadata,
// IETF protocol assignments, benchmarking, multicast and reserved; for IPv6 the unspecified and loopback addresses,
// unique-local, link-local, multicast and NAT64). An IPv4-mapped IPv6 address is judged as its IPv4 address. Anything
// that is not an address is private. The IPv6 tests are the JS function's own, made on the canonical text (RFC 5952,
// net.IP.String(), the form a resolver gives), so the two agree on every address; proven by
// gw/testdata/egress/private.json, which the JS function writes (scripts/fixtures/gen-egress.js).
func IsPrivate(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		p, q := v4[0], v4[1]
		return p == 0 || p == 10 || p == 127 ||
			(p == 100 && q >= 64 && q <= 127) ||
			(p == 169 && q == 254) ||
			(p == 172 && q >= 16 && q <= 31) ||
			(p == 192 && q == 168) ||
			(p == 192 && q == 0) ||
			(p == 198 && (q == 18 || q == 19)) ||
			p >= 224
	}
	if len(ip) != net.IPv6len {
		return true // not an address at all
	}
	a := ip.String()
	return a == "::" || a == "::1" ||
		strings.HasPrefix(a, "fc") || strings.HasPrefix(a, "fd") ||
		strings.HasPrefix(a, "fe8") || strings.HasPrefix(a, "fe9") ||
		strings.HasPrefix(a, "fea") || strings.HasPrefix(a, "feb") ||
		strings.HasPrefix(a, "ff") || strings.HasPrefix(a, "64:ff9b:")
}
