package egress

import "testing"

func TestPolicyAllows(t *testing.T) {
	p := &Policy{Mode: ModeEnforce, Allow: []Rule{
		{Host: "API.OpenAI.com"},
		{Host: "*.pinecone.io"},
		{Host: "10.0.0.5", Port: 5432},
		{Host: "[2001:db8::1]"},
		{Host: "::ffff:192.0.2.9"},
		{Host: "example.org.", Port: 8443},
	}}
	cases := []struct {
		host string
		port int
		want bool
	}{
		{"api.openai.com", 443, true},
		{"API.OPENAI.COM", 80, true},
		{"api.openai.com.", 443, true},
		{"openai.com", 443, false},
		{"x.api.openai.com", 443, false},
		{"idx-1.svc.pinecone.io", 443, true},
		{"a.pinecone.io", 443, true},
		{"pinecone.io", 443, false},
		{"evilpinecone.io", 443, false},
		{".pinecone.io", 443, false},
		{"10.0.0.5", 5432, true},
		{"10.0.0.5", 5433, false},
		{"10.0.0.6", 5432, false},
		{"2001:db8::1", 80, true},
		{"[2001:db8::1]", 80, true},
		{"2001:0db8:0:0:0:0:0:1", 443, true},
		{"2001:db8::2", 80, false},
		{"192.0.2.9", 80, true},
		{"example.org", 8443, true},
		{"example.org", 443, false},
		{"", 443, false},
	}
	for _, c := range cases {
		if got := p.Allows(c.host, c.port); got != c.want {
			t.Errorf("Allows(%q, %d) = %v, want %v", c.host, c.port, got, c.want)
		}
	}
	var nilP *Policy
	if nilP.Allows("api.openai.com", 443) {
		t.Error("nil policy allowed")
	}
	if (&Policy{}).Allows("api.openai.com", 443) {
		t.Error("empty policy allowed")
	}
	// A name rule never matches an address and a suffix rule never matches an address.
	q := &Policy{Allow: []Rule{{Host: "*.0.0.1"}, {Host: "localhost"}}}
	if q.Allows("127.0.0.1", 80) {
		t.Error("suffix matched an address")
	}
}
