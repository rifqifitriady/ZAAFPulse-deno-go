package egress

import (
	"net"
	"strings"
)

// Rule allows one destination: Host is a name ("api.openai.com"), an address ("10.0.0.5", "::1", "[::1]") or
// "*.suffix" (any subdomain of suffix, not suffix itself); Port 0 allows any port.
type Rule struct {
	Host string
	Port int
}

// Mode is what a destination off the list gets: "report" (recorded "would-refuse", the call goes on) or "enforce"
// (refused, EGRESS_REFUSED). Anything but "enforce" reports.
type Mode string

const (
	ModeReport  Mode = "report"
	ModeEnforce Mode = "enforce"
)

// Policy is the allow-list the control plane pushes (config.push.egress, spec Decision 13).
type Policy struct {
	Allow []Rule
	Mode  Mode
}

// Allows reports whether host:port is on the list. Names compare case-insensitively (a trailing dot ignored);
// addresses compare by value, so "::1", "[::1]" and "0:0:0:0:0:0:0:1" are one address and an IPv4-mapped IPv6
// address equals its IPv4 address; a "*.suffix" rule never matches an address. A nil policy allows nothing.
func (p *Policy) Allows(host string, port int) bool {
	if p == nil {
		return false
	}
	h := normHost(host)
	hip := net.ParseIP(h)
	for _, r := range p.Allow {
		if r.Port != 0 && r.Port != port {
			continue
		}
		rh := normHost(r.Host)
		if suffix, ok := strings.CutPrefix(rh, "*."); ok {
			if hip == nil && suffix != "" && strings.HasSuffix(h, "."+suffix) && len(h) > len(suffix)+1 {
				return true
			}
			continue
		}
		if rip := net.ParseIP(rh); rip != nil {
			if hip != nil && rip.Equal(hip) {
				return true
			}
			continue
		}
		if hip == nil && rh != "" && h == rh {
			return true
		}
	}
	return false
}

// IsLoopbackHost reports whether host is one the control plane always calls direct and the allow-list leaves out
// (lib/egress.js isLoopbackHost, the hosts the lock-down grants): localhost, 127.0.0.1, ::1 and 127.0.0.1 written as
// an IPv4-mapped IPv6 address; brackets, letter case and one trailing dot ignored. The rest of 127/8 is not.
func IsLoopbackHost(host string) bool {
	h := normHost(host)
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.Equal(net.IPv4(127, 0, 0, 1)) || ip.Equal(net.IPv6loopback))
}

// normHost lowercases a host and removes IPv6 brackets and one trailing dot.
func normHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	return strings.TrimSuffix(h, ".")
}
