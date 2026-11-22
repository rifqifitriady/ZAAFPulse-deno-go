package main

// The egress endpoint (P5, the p5-egress design Decisions 2, 3, 9): the first
// config.push with an egress section creates it, from the engine's provider client; it listens on 127.0.0.1 before
// ready, whose body names its port; a later push changes only its allow-list. Every connection it makes or refuses,
// and every call of the engine's own provider client, is an egress.event stored in the outbox and sent. It closes on
// shutdown after the front, or at once when the control plane is gone.

import (
	"net/http"
	"slices"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/egress"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// policyOf is the endpoint's allow-list from a config.push egress section.
func policyOf(c *ipc.EgressConfig) egress.Policy {
	p := egress.Policy{Mode: egress.ModeReport, Allow: make([]egress.Rule, 0, len(c.Allow))}
	if c.AllowList == string(egress.ModeEnforce) {
		p.Mode = egress.ModeEnforce
	}
	for _, r := range c.Allow {
		p.Allow = append(p.Allow, egress.Rule{Host: r.Host, Port: r.Port})
	}
	return p
}

// applyEgress takes a config.push egress section: the first one creates the endpoint (opened on ready), a later one
// replaces its allow-list. A section that first appears after ready applies after a restart (its port could only be
// given in ready).
func (g *gateway) applyEgress(c *ipc.EgressConfig) {
	if c == nil {
		return
	}
	p := policyOf(c)
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.egress != nil {
		g.egress.SetPolicy(p)
		return
	}
	if g.ready {
		g.log.Print("config.push: an egress section after the start applies after a restart")
		return
	}
	// The engine's provider client's settings (proxy environment, trusted roots, connect limit); egress makes its own
	// redirects, so the client's redirect rule does not apply.
	g.egress = egress.New(&http.Client{Transport: g.upstream.Transport()}, g.recordEgress)
	g.egress.SetPolicy(p)
	g.egressNow.Store(g.egress)
	if g.frontToken != "" {
		g.egress.SetToken(egress.DeriveToken(g.frontToken))
	}
}

// openEgress opens the endpoint with the token derived from the front token; it returns the ready body's egress field,
// nil when there is no endpoint or it could not open (the control plane then sends every class direct).
func (g *gateway) openEgress(token string) *ipc.ReadyEgress {
	g.mu.Lock()
	srv := g.egress
	g.mu.Unlock()
	if srv == nil {
		return nil
	}
	if token == "" {
		g.log.Print("egress: config.push has an egress section but secrets.handoff no frontToken; no egress endpoint")
		g.closeEgress(0)
		return nil
	}
	srv.SetToken(egress.DeriveToken(token))
	port, err := srv.Listen()
	if err != nil {
		g.log.Printf("egress: the endpoint could not be opened: %v", err)
		g.closeEgress(0)
		return nil
	}
	g.log.Printf("egress: listening on 127.0.0.1:%d", port)
	return &ipc.ReadyEgress{Port: port}
}

// closeEgress stops the endpoint: requests in flight get up to grace, then every connection is closed.
func (g *gateway) closeEgress(grace time.Duration) {
	g.mu.Lock()
	srv := g.egress
	g.egress = nil
	g.egressNow.Store(nil)
	g.mu.Unlock()
	if srv != nil {
		srv.Close(grace)
	}
}

// egressPolicy is the allow-list the engine's own calls are checked against (P5 Decision 13): the endpoint's, nil when
// there is none (no egress section, or it could not open).
func (g *gateway) egressPolicy() *egress.Policy {
	if srv := g.egressNow.Load(); srv != nil {
		return srv.Policy()
	}
	return nil
}

// invalidEgress names why a record breaks ipc/v1/egress.event.schema.json, "" when it is valid. A record that breaks
// it would be refused by the control plane every time the outbox replays it, so it is never stored.
func invalidEgress(ev *ipc.EgressEvent) string {
	switch {
	case ev.TS < 0:
		return "ts is negative"
	case ev.Purpose == "":
		return "purpose is empty"
	case ev.Method == "":
		return "method is empty"
	case ev.Scheme != "http" && ev.Scheme != "https" && ev.Scheme != "tcp":
		return "scheme is not http, https or tcp"
	case ev.Host == "":
		return "host is empty"
	case ev.Port < 1 || ev.Port > 65535:
		return "port is not 1..65535"
	case ev.Status != 0 && (ev.Status < 100 || ev.Status > 999):
		return "status is not 100..999"
	case !slices.Contains([]string{egress.OutcomeOK, egress.OutcomeError, egress.OutcomeRefused,
		egress.OutcomeWouldRefuse, egress.OutcomeChecked}, ev.Outcome):
		return "outcome is not one of the five"
	case ev.BytesOut < 0 || ev.BytesIn < 0:
		return "a byte count is negative"
	case !(ev.MS >= 0):
		return "ms is negative"
	case ev.Via != egress.ViaProxy && ev.Via != egress.ViaTunnel && ev.Via != egress.ViaEngine:
		return "via is not proxy, tunnel or engine"
	}
	return ""
}

// recordEgress stores one egress record in the outbox and hands it to the sender (an event, as usage.report): it is
// called from many goroutines at once, and never waits for the control plane. A record that breaks the event's schema
// is dropped (logged once per cause).
func (g *gateway) recordEgress(r egress.Record) {
	ev := ipc.EgressEvent{TS: r.TS, Purpose: r.Purpose, Method: r.Method, Scheme: r.Scheme, Host: r.Host, Port: r.Port,
		Status: r.Status, Outcome: r.Outcome, Error: r.Error, BytesOut: r.BytesOut, BytesIn: r.BytesIn, MS: r.MS,
		Via: r.Via}
	if why := invalidEgress(&ev); why != "" {
		if _, seen := g.dropped.LoadOrStore(why, true); !seen {
			g.log.Printf("egress: a record was dropped, its %s (logged once per cause)", why)
		}
		return
	}
	if err := g.appendAndSend(ipc.TypeEgressEvent, &ev); err != nil {
		g.log.Printf("egress: a record could not be stored: %v", err)
	}
}
