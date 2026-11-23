package egress

import (
	"net"
	"strings"
)

// IsPrivate is lib/net.js isPrivateAddress for an address the dialer is about to connect to: true for the addresses a
// public URL must never reach (this network, private, loopback, carrier-grade NAT, link-local and cloud metadata,
// IETF protocol assignments, benchmarking, multicast and reserved; for IPv6 ::/96 (unspecified, loopback and the
// deprecated IPv4-compatible ::a.b.c.d), unique-local, link-local, the deprecated site-local fec0::/10, multicast,
// NAT64 64:ff9b::/32 (the well-known /96 and the local-use 64:ff9b:1::/48), Teredo 2001:0::/32, and 6to4 2002::/16
// when the IPv4 address it embeds is private). An IPv4-mapped IPv6 address is judged as its IPv4 address. Anything
// that is not an address is private. The JS function's text tests are made on the canonical text (RFC 5952,
// net.IP.String(), the form a resolver gives) and its group tests on the bytes, so the two agree on every address;
// proven by gw/testdata/egress/private.json, which the JS function writes (scripts/fixtures/gen-egress.js).
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
	if a == "::" || a == "::1" ||
		strings.HasPrefix(a, "fc") || strings.HasPrefix(a, "fd") ||
		strings.HasPrefix(a, "fe8") || strings.HasPrefix(a, "fe9") ||
		strings.HasPrefix(a, "fea") || strings.HasPrefix(a, "feb") ||
		strings.HasPrefix(a, "ff") || strings.HasPrefix(a, "64:ff9b:") {
		return true
	}
	h0 := uint16(ip[0])<<8 | uint16(ip[1])
	h1 := uint16(ip[2])<<8 | uint16(ip[3])
	return isZero(ip[:12]) || // ::/96 (a mapped address returned above)
		(h0 == 0x64 && h1 == 0xff9b) ||
		h0&0xfe00 == 0xfc00 ||
		h0&0xffc0 == 0xfe80 ||
		h0&0xffc0 == 0xfec0 ||
		h0&0xff00 == 0xff00 ||
		(h0 == 0x2001 && h1 == 0) ||
		(h0 == 0x2002 && IsPrivate(net.IPv4(ip[2], ip[3], ip[4], ip[5])))
}

func isZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
