package main

// The egress endpoint (P5, docs/superpowers/specs/2026-11-22-p5-egress-design.md Decisions 2, 3, 9): the first
// config.push with an egress section creates it, from the engine's provider client; it listens on 127.0.0.1 before
// ready, whose body names its port; a later push changes only its allow-list. Every connection it makes or refuses,
// and every call of the engine's own provider client, is an egress.event stored in the outbox and sent. It closes on
// shutdown after the front, or at once when the control plane is gone.

import (
	"net/http"
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
	g.mu.Unlock()
	if srv != nil {
		srv.Close(grace)
	}
}

// recordEgress stores one egress record in the outbox and hands it to the sender (an event, as usage.report): it is
// called from many goroutines at once, and never waits for the control plane.
func (g *gateway) recordEgress(r egress.Record) {
	ev := ipc.EgressEvent{TS: r.TS, Purpose: r.Purpose, Method: r.Method, Scheme: r.Scheme, Host: r.Host, Port: r.Port,
		Status: r.Status, Outcome: r.Outcome, Error: r.Error, BytesOut: r.BytesOut, BytesIn: r.BytesIn, MS: r.MS,
		Via: r.Via}
	if err := g.appendAndSend(ipc.TypeEgressEvent, &ev); err != nil {
		g.log.Printf("egress: a record could not be stored: %v", err)
	}
}
