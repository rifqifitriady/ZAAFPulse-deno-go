package egress

import (
	"errors"
	"net"
	"strings"
	"syscall"
)

// The cloud metadata service is never reached through Go (P7 review M1), in every mode (report, enforce, public), on
// every path (the first URL, every redirect hop, /check, CONNECT, the dialers, the engine's own client) and whatever
// the allow-list lists. It is a rule of its own, beside the allow-list: before P5 Deno's --deny-net gave every
// outbound call the same refusal (deno.json, the server, cli and mcp-stdio sets), and the Go path must not lose it.

// metadataIPs are the metadata service addresses: AWS, Azure, GCP, Oracle and others (169.254.169.254), AWS over IPv6
// (fd00:ec2::254), the ECS task metadata and credentials endpoint (169.254.170.2) and Alibaba Cloud (100.100.100.200).
var metadataIPs = []net.IP{
	net.ParseIP("169.254.169.254"),
	net.ParseIP("169.254.170.2"),
	net.ParseIP("100.100.100.200"),
	net.ParseIP("fd00:ec2::254"),
}

// metadataNames are the names the metadata service answers on (lower case, no trailing dot): GCP's two names and the
// bare search-domain form, and AWS's instance-data names.
var metadataNames = map[string]bool{
	"metadata.google.internal":   true,
	"metadata.goog":              true,
	"metadata":                   true,
	"instance-data":              true,
	"instance-data.ec2.internal": true,
}

// IsMetadata is true for a metadata service address. An IPv4-mapped (::ffff:a.b.c.d), IPv4-compatible (::a.b.c.d)
// or NAT64 (64:ff9b::a.b.c.d) form of a metadata IPv4 address is the same address.
func IsMetadata(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if len(ip) == net.IPv6len && ip.To4() == nil {
		h := ip[:12]
		if isZero(h) || (h[0] == 0 && h[1] == 0x64 && h[2] == 0xff && h[3] == 0x9b && isZero(h[4:12])) {
			ip = net.IPv4(ip[12], ip[13], ip[14], ip[15])
		}
	}
	for _, m := range metadataIPs {
		if ip.Equal(m) {
			return true
		}
	}
	return false
}

// IsMetadataHost is true for a URL or CONNECT host that names the metadata service: one of its addresses (brackets and
// an IPv6 zone ignored) or one of its names (case and a trailing dot ignored). A name that resolves to a metadata
// address is caught by the dialers (dial, RefuseMetadata).
func IsMetadataHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"))
	if i := strings.IndexByte(h, '%'); i >= 0 {
		h = h[:i]
	}
	if ip := net.ParseIP(h); ip != nil {
		return IsMetadata(ip)
	}
	return metadataNames[strings.TrimSuffix(h, ".")]
}

// metadataError is a request or a connect refused because its target is the metadata service. It is answered and
// recorded as EGRESS_PRIVATE (lib/net.js reads that code), with a message that starts with the address or name.
type metadataError struct{ addr string }

func (e metadataError) Error() string {
	return e.addr + " is a cloud metadata address; it is never reached"
}

// EgressCode is the failure's code, EGRESS_PRIVATE (gw/engine ErrorCode reads it).
func (e metadataError) EgressCode() string { return CodePrivate }

// RefuseMetadata is a net.Dialer Control hook that refuses a metadata address: it runs after the address is chosen and
// before the connect, so a name that resolves to a metadata address is refused too. The endpoint's dialers use it in
// every mode; the gateway puts it on the engine's provider client.
func RefuseMetadata(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return metadataError{addr: address}
	}
	if IsMetadataHost(host) {
		return metadataError{addr: host}
	}
	return nil
}

// isMetadataErr is true for a failure that is a metadata refusal (a dialer's, wrapped in the transport's errors).
func isMetadataErr(err error) bool {
	var m metadataError
	return errors.As(err, &m)
}
