package front

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"strings"
)

// ClientHeader is the header the front puts on every request it forwards (P4a spec D3, header design): the client's
// address and port, whether it came over TLS, and an HMAC that proves the front wrote it. The control plane's internal
// listener (lib/front/client.js parseClientHeader) refuses a request without a valid one.
const ClientHeader = "X-Zp-Client"

// ClientValue is "<ip> <port> <tls:0|1> <hmac>", where hmac is the lowercase hex HMAC-SHA256 of "<ip> <port> <tls>"
// keyed with the frontToken's hex text as bytes. remoteAddr is http.Request.RemoteAddr ("ip:port", "[ipv6]:port").
// An IPv6 zone is dropped (the control plane refuses one); an IPv4-mapped address is written as IPv4.
func ClientValue(token, remoteAddr string, tls bool) string {
	host, port, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host, port = remoteAddr, "0"
	}
	host, _, _ = strings.Cut(host, "%")
	if ip := net.ParseIP(host); ip != nil {
		host = ip.String()
	}
	t := "0"
	if tls {
		t = "1"
	}
	text := host + " " + port + " " + t
	mac := hmac.New(sha256.New, []byte(token))
	mac.Write([]byte(text))
	return text + " " + hex.EncodeToString(mac.Sum(nil))
}
