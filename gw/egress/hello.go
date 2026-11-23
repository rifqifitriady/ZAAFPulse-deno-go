package egress

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
)

// HelloLabel starts the message the endpoint's proof is computed over.
const HelloLabel = "zaafpulse egress hello "

// HelloProof is the endpoint's answer to GET /hello?n=<nonce> (P7 review L1): hex HMAC-SHA256 keyed with the egress
// token (its text as bytes) over HelloLabel + "<port> <nonce>", port being the port the endpoint listens on. mcp.js
// (lib/egress-discovery.js proveEndpoint) computes the same and sends the token only to an endpoint that answers it:
// a process that took the port after the gateway died never held the token, and one that relays the nonce to the
// live endpoint gets a proof for that endpoint's port, not its own.
func HelloProof(token string, port int, nonce string) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte(HelloLabel + strconv.Itoa(port) + " " + nonce))
	return hex.EncodeToString(m.Sum(nil))
}

// validNonce is 64 lower-case hex characters (32 random bytes).
func validNonce(n string) bool {
	if len(n) != 64 {
		return false
	}
	for i := 0; i < len(n); i++ {
		if c := n[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// hello is GET /hello?n=<nonce>, answered without the token (the caller proves nothing; the endpoint does): the proof
// as text, 400 for a nonce that is not exactly one 64-hex value. Until the token is set the connection is dropped, as
// every request is. Nothing is connected and nothing is recorded.
func (s *Server) hello(w http.ResponseWriter, r *http.Request) {
	tok := s.token.Load()
	if tok == nil || *tok == "" {
		drop(w)
		return
	}
	ns := r.URL.Query()["n"]
	if len(ns) != 1 || !validNonce(ns[0]) {
		writeError(w, http.StatusBadRequest, CodeBadRequest, "/hello takes one nonce n of 64 lower-case hex characters")
		return
	}
	port := 0
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if ta, ok := a.(*net.TCPAddr); ok {
			port = ta.Port
		}
	}
	if port == 0 {
		drop(w)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(HelloProof(*tok, port, ns[0])))
}
