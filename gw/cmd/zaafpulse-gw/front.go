package main

// The gateway in front (P4a, docs/superpowers/specs/2026-11-12-p4a-go-front-design.md D1, D2): the config.push front
// section, the main port opened on ready, closed on shutdown or when the control plane is gone.

import (
	"context"
	"errors"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/front"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

// applyFront checks a config.push front section: a route class on the go engine is refused (P4a has none: exit 2). The
// section of the first config.push is kept and opened on ready; a later one changes only the engines. Listen, TLS and
// upstream, and a front section that first appears after the start, apply after a restart (P4b entry condition 3: the
// control plane builds the section once per gateway start, lib/front/index.js). It returns an exit code, or -1 to go on.
func (g *gateway) applyFront(f *ipc.FrontConfig) int {
	if f == nil {
		return -1
	}
	engines := make(map[string]string, len(f.Engines))
	for class, e := range f.Engines {
		if e != "js" && !(e == "go" && front.GoClasses[class]) {
			return exitWith(g.log, ipc.ExitRefused, "config.push: route class %q is on engine %q, which this gateway does not have", class, e)
		}
		engines[class] = string(e)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	shadow := make([]string, 0, len(f.Shadow.Classes))
	for _, c := range f.Shadow.Classes {
		shadow = append(shadow, string(c))
	}
	if g.front != nil {
		g.front.SetEngines(engines)
		g.front.SetShadow(shadow)
		if f.Listen != g.frontCfg.Listen || !sameTLS(f.TLS, g.frontCfg.TLS) || f.Upstream != g.frontCfg.Upstream {
			g.log.Print("config.push: a new listen, tls or upstream applies after a restart")
		}
		return -1
	}
	if g.ready {
		g.log.Print("config.push: a front section after the start applies after a restart")
		return -1
	}
	if g.frontCfg == nil {
		g.frontCfg = f
	}
	return -1
}

func sameTLS(a, b *ipc.FrontTLS) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// openFront reads the TLS files (only now, after the handshake: D1.3), opens the main port and serves it. It returns
// an exit code, or -1 to go on.
func (g *gateway) openFront(f *ipc.FrontConfig, token string) int {
	if token == "" {
		return exitWith(g.log, ipc.ExitRefused, "config.push has a front section but secrets.handoff no frontToken")
	}
	engines := make(map[string]string, len(f.Engines))
	for class, e := range f.Engines {
		engines[class] = string(e)
	}
	cfg := front.Config{Host: f.Listen.Host, Port: int(f.Listen.Port), Upstream: int(f.Upstream.Port), Token: token,
		Engines: engines}
	if f.TLS != nil {
		cfg.CertFile, cfg.KeyFile = f.TLS.CertPath, f.TLS.KeyPath
	}
	srv, err := front.Listen(cfg, g.log)
	if err == nil {
		srv.SetGoEngine(g.engine)
		shadow := make([]string, 0, len(f.Shadow.Classes))
		for _, c := range f.Shadow.Classes {
			shadow = append(shadow, string(c))
		}
		srv.SetShadow(shadow)
	}
	if errors.Is(err, front.ErrAddrInUse) {
		return exitWith(g.log, ipc.ExitPortInUse, "port %d on %s is in use", f.Listen.Port, f.Listen.Host)
	}
	if err != nil {
		return exitWith(g.log, ipc.ExitPortInUse, "the main port could not be opened: %v", err)
	}
	g.mu.Lock()
	g.front = srv
	g.mu.Unlock()
	go func() {
		if err := srv.Serve(); err != nil {
			g.log.Printf("front: %v", err)
		}
	}()
	g.log.Printf("front: listening on %s, forwarding to 127.0.0.1:%d", srv.Addr(), f.Upstream.Port)
	return -1
}

// closeFront stops the main port: gracefully within grace, or at once when grace is 0.
func (g *gateway) closeFront(grace time.Duration) {
	g.mu.Lock()
	srv := g.front
	g.mu.Unlock()
	if srv == nil {
		return
	}
	if grace <= 0 {
		_ = srv.Close()
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), grace)
		defer cancel()
		_ = srv.Shutdown(ctx) // closes what is left when the grace ends
	}
	// The streams the go engine was serving end with their connections; their usage lines go to the outbox before it
	// closes (P4c D4).
	if g.engine != nil && !g.engine.Wait(2*time.Second) {
		g.log.Printf("shutdown: some stream usage lines were not stored within 2 s")
	}
}
