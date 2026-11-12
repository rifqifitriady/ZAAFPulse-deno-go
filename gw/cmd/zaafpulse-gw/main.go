// Command zaafpulse-gw is the gateway process of ZAAFPulse v2. It speaks the whole v1 IPC protocol with the control
// plane over stdin and stdout (ipc/v1; docs/superpowers/specs/2026-11-11-p3-ipc-contract-design.md). When config.push
// carries a front section (P4a, docs/superpowers/specs/2026-11-12-p4a-go-front-design.md) it holds the main port and
// forwards every request to the control plane (package front, front.go); without one it serves no HTTP, as in P3. It
// logs to stderr; the control plane forwards each line with a "gw: " prefix. When stdin reaches end of file (the
// control plane is gone) it exits 0.
//
// Exit codes (the constants in package ipc): 0 done (shutdown or end of input); 1 ExitDamaged, an I/O failure, a
// damaged outbox or a stdin read error; 2 ExitRefused, refused, a config it cannot apply, or a bad command line; 3
// ExitProtocol, a protocol error (after an "error" message) or --crash-after-ready-ms; 4 ExitPortInUse, the main port
// could not be opened (in use, refused, or its TLS files unreadable); 5 ExitOutboxInUse, another gateway holds the
// outbox. Exits 1 (but a stdin read error), 2, 4 and 5 log a "fatal: " line first (exitWith, exit.go).
//
// Test-only flags make it misbehave for the supervisor tests: --protocol, --hang-after-ready, --crash-after-ready-ms,
// --bad-line and --ignore-shutdown; --emit N sends N rounds of synthetic events (ipc.Synthetic) for the ingest and
// kill tests.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/engine"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/front"
	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

const version = "0.4.0-front"

type options struct {
	outbox            string
	healthMs          int
	protocol          string
	hangAfterReady    bool
	crashAfterReadyMs int
	badLine           bool
	ignoreShutdown    bool
	emit              int64
	emitEveryMs       int
	emitBase          int64
	segmentEvents     int
	staleMs           int64
	healthOnSnapshot  bool
}

func main() {
	// A write to a closed stdout (the control plane died) returns EPIPE instead of killing the process.
	signal.Ignore(syscall.SIGPIPE)
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	logger := log.New(stderr, "", 0)
	var o options
	fs := flag.NewFlagSet("zaafpulse-gw", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.outbox, "outbox", "", "the outbox folder (default $ZAAFPULSE_DATA/.data/gw-outbox)")
	fs.IntVar(&o.healthMs, "health-ms", 1000, "how often health is sent, in ms")
	fs.StringVar(&o.protocol, "protocol", "1", "test only: the protocol versions hello offers, comma-separated")
	fs.BoolVar(&o.hangAfterReady, "hang-after-ready", false, "test only: stop sending health once ready")
	fs.IntVar(&o.crashAfterReadyMs, "crash-after-ready-ms", 0, "test only: exit 3 this many ms after ready")
	fs.BoolVar(&o.badLine, "bad-line", false, "test only: write a line that is not JSON once ready")
	fs.BoolVar(&o.ignoreShutdown, "ignore-shutdown", false, "test only: ignore shutdown")
	fs.Int64Var(&o.emit, "emit", 0, "test only: send this many rounds of synthetic usage, audit and trace events")
	fs.IntVar(&o.emitEveryMs, "emit-every-ms", 0, "test only: pause between synthetic events, in ms")
	fs.Int64Var(&o.emitBase, "emit-base", ipc.DefaultEmitBase, "test only: ts of the first synthetic round")
	fs.IntVar(&o.segmentEvents, "segment-events", 0, "test only: events per outbox segment (default 1000)")
	fs.Int64Var(&o.staleMs, "stale-ms", ipc.StaleAfterMs, "test only: age at which a budget snapshot is stale, in ms")
	fs.BoolVar(&o.healthOnSnapshot, "health-on-snapshot", false, "test only: send health after every budget snapshot")
	if err := fs.Parse(args); err != nil {
		return exitWith(logger, ipc.ExitRefused, "bad command line: %v", err)
	}
	dir := o.outbox
	if dir == "" {
		data := os.Getenv("ZAAFPULSE_DATA")
		if data == "" {
			return exitWith(logger, ipc.ExitRefused, "no outbox folder: pass --outbox or set ZAAFPULSE_DATA")
		}
		dir = filepath.Join(data, ".data", "gw-outbox")
	}
	protocols, err := parseProtocols(o.protocol)
	if err != nil {
		return exitWith(logger, ipc.ExitRefused, "%v", err)
	}
	box, err := ipc.OpenOutbox(dir)
	if errors.Is(err, ipc.ErrOutboxInUse) {
		return exitWith(logger, ipc.ExitOutboxInUse, "%v", err)
	}
	if err != nil {
		return exitWith(logger, ipc.ExitDamaged, "%v", err)
	}
	defer box.Close()
	box.SetPerSegment(o.segmentEvents)
	g := &gateway{
		o:       o,
		box:     box,
		ch:      ipc.NewChannel(stdin, stdout, false),
		log:     logger,
		started: time.Now(),
		done:    make(chan struct{}),
		unknown: map[string]bool{},
	}
	g.engine = engine.New(engine.NewUpstream(os.Getenv), g.emitUsage, g.log.Printf)
	return g.serve(protocols)
}

func parseProtocols(s string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil || n < 1 {
			return nil, fmt.Errorf("--protocol %q: a comma-separated list of versions", s)
		}
		out = append(out, n)
	}
	return out, nil
}

type gateway struct {
	o       options
	box     *ipc.Outbox
	ch      *ipc.Channel
	log     *log.Logger
	started time.Time
	done    chan struct{}
	unknown map[string]bool

	stopEmit chan struct{}
	emitDone chan struct{}

	mu      sync.Mutex
	cfgGen  int64
	secGen  int64
	secrets map[string]ipc.ProviderSecret // in memory only: never logged, never written to the outbox
	mask    []string
	snap    *ipc.BudgetSnapshot
	snapAt  time.Time
	ready   bool
	quiet   bool // --hang-after-ready

	engine     *engine.Engine   // the go engine (P4b): its state is the newest generation with the newest snapshot
	cfgBody    *ipc.ConfigPush  // under mu: the newest config.push, until its secrets arrive
	frontCfg   *ipc.FrontConfig // under mu: the first config.push's front section, opened on ready
	frontToken string           // under mu: from secrets.handoff, never logged
	front      *front.Server    // under mu: the main port, once open
}

func (g *gateway) serve(protocols []int) int {
	defer close(g.done)
	hello := ipc.Hello{Protocol: protocols, GwVersion: version, Stream: g.box.Stream(), OutboxFrom: g.box.Acked() + 1}
	if err := g.ch.Send(ipc.TypeHello, hello, "h1", ""); err != nil {
		g.log.Printf("hello: %v", err)
		return ipc.ExitDamaged
	}
	handshaken := false
	for {
		msg, err := g.ch.Recv()
		if errors.Is(err, io.EOF) || errors.Is(err, ipc.ErrPartialLine) {
			g.drain()
			return 0
		}
		var perr *ipc.ProtocolError
		if errors.As(err, &perr) {
			return g.protocolError(err)
		}
		if err != nil {
			// Reading stdin failed (E11): an I/O failure, not a protocol error, and nothing to report on the channel.
			g.log.Printf("stdin: %v", err)
			g.stopEmitter(time.Second)
			return ipc.ExitDamaged
		}
		if msg.Unknown {
			if !g.unknown[msg.Type] {
				g.unknown[msg.Type] = true
				g.log.Printf("unknown message type %q dropped", msg.Type)
			}
			continue
		}
		if !handshaken {
			switch b := msg.Body.(type) {
			case *ipc.HelloOK:
				if b.Protocol != ipc.Version {
					return exitWith(g.log, ipc.ExitRefused, "the control plane chose protocol %d; this gateway speaks %d",
						b.Protocol, ipc.Version)
				}
				handshaken = true
				// Everything not acknowledged goes out again, before any new event (D4), under the outbox's send lock.
				if err := g.box.Replay(g.box.Acked()+1, g.ch.SendLine); err != nil {
					return exitWith(g.log, ipc.ExitDamaged, "outbox replay: %v", err)
				}
				go g.healthLoop()
			case *ipc.HelloRefused:
				return exitWith(g.log, ipc.ExitRefused, "refused by the control plane: %s", b.Reason)
			default:
				return g.protocolError(fmt.Errorf("%s before the handshake", msg.Type))
			}
			continue
		}
		if code := g.handle(msg); code >= 0 {
			return code
		}
	}
}

// handle applies one message after the handshake; it returns an exit code, or -1 to go on.
func (g *gateway) handle(msg ipc.Message) int {
	switch b := msg.Body.(type) {
	case *ipc.ConfigPush:
		if code := g.applyFront(b.Front); code >= 0 {
			return code
		}
		g.mu.Lock()
		g.cfgGen, g.cfgBody = b.Gen, b
		g.mu.Unlock()
		return g.applied()
	case *ipc.SecretsHandoff:
		m := make(map[string]ipc.ProviderSecret, len(b.Providers))
		for _, p := range b.Providers {
			m[p.ID] = p
		}
		g.mu.Lock()
		g.secGen, g.secrets, g.mask, g.frontToken = b.Gen, m, b.Mask, b.FrontToken
		if g.cfgBody != nil && g.cfgBody.Gen == b.Gen {
			st := engine.NewState(g.cfgBody, b)
			if g.snap != nil {
				st = st.WithSnapshot(g.snap)
			}
			g.engine.SetState(st)
		}
		g.mu.Unlock()
		return g.applied()
	case *ipc.BudgetSnapshot:
		g.mu.Lock()
		g.snap, g.snapAt = b, time.Now()
		if st := g.engine.State(); st != nil {
			g.engine.SetState(st.WithSnapshot(b))
		}
		g.mu.Unlock()
		if g.o.healthOnSnapshot {
			g.sendHealth()
		}
		return g.maybeReady()
	case *ipc.Ack:
		if b.Stream != g.box.Stream() {
			g.log.Printf("ack for another stream %s, ignored", b.Stream)
			return -1
		}
		if err := g.box.Ack(b.UpTo); err != nil {
			g.log.Printf("outbox ack: %v", err)
		}
	case *ipc.Shutdown:
		if g.o.ignoreShutdown {
			g.log.Print("shutdown ignored (--ignore-shutdown)")
			return -1
		}
		g.shutdown(time.Duration(b.GraceMs) * time.Millisecond)
		return 0
	default:
		g.log.Printf("unexpected %s from the control plane, dropped", msg.Type)
	}
	return -1
}

// applied runs after a config.push or a secrets.handoff: before ready it may complete the start; after it, once both of
// a generation are in, a health names that generation at once, so the control plane need not wait for the next tick
// (lib/ipc/supervisor.js synced: an API key revoked or a provider changed is in force when the save answers).
func (g *gateway) applied() int {
	g.mu.Lock()
	done := g.ready && g.cfgGen == g.secGen
	g.mu.Unlock()
	if !done {
		return g.maybeReady()
	}
	g.sendHealth()
	return -1
}

func (g *gateway) protocolError(err error) int {
	g.log.Print(err)
	_ = g.ch.Send(ipc.TypeError, ipc.ErrorBody{Code: "protocol", Message: err.Error()}, "", "")
	return ipc.ExitProtocol
}

// maybeReady sends ready once the first config, its secrets and a snapshot are applied, after opening the main port
// when the config has a front section. It returns an exit code, or -1 to go on.
func (g *gateway) maybeReady() int {
	g.mu.Lock()
	if g.ready || g.cfgGen == 0 || g.secGen != g.cfgGen || g.snap == nil {
		g.mu.Unlock()
		return -1
	}
	g.ready = true
	gen := g.cfgGen
	f, tok := g.frontCfg, g.frontToken
	g.mu.Unlock()
	if f != nil {
		if code := g.openFront(f, tok); code >= 0 {
			return code
		}
	}
	if err := g.ch.Send(ipc.TypeReady, ipc.Ready{Gen: gen}, "", ""); err != nil {
		g.log.Printf("ready: %v", err)
		return -1
	}
	g.log.Printf("ready (config generation %d)", gen)
	g.afterReady()
	return -1
}

func (g *gateway) afterReady() {
	if g.o.hangAfterReady {
		g.mu.Lock()
		g.quiet = true
		g.mu.Unlock()
	}
	if g.o.crashAfterReadyMs > 0 {
		time.AfterFunc(time.Duration(g.o.crashAfterReadyMs)*time.Millisecond, func() {
			g.log.Print("crashing on purpose (--crash-after-ready-ms)")
			os.Exit(ipc.ExitProtocol)
		})
	}
	if g.o.badLine {
		_ = g.ch.SendLine([]byte("this line is not JSON"))
	}
	g.startEmitter()
}

// startEmitter sends the synthetic events of --emit from the outbox's next seq up to 3*emit: a restarted stub picks up
// where the killed one stopped, so a run is exactly 3*emit events whatever the kills.
func (g *gateway) startEmitter() {
	if g.o.emit <= 0 {
		return
	}
	g.stopEmit, g.emitDone = make(chan struct{}), make(chan struct{})
	go func() {
		defer close(g.emitDone)
		total := 3 * g.o.emit
		for g.box.Next() <= total {
			select {
			case <-g.stopEmit:
				return
			default:
			}
			typ, body := ipc.Synthetic(g.box.Next(), g.o.emitBase)
			g.checkBudget(body)
			if err := g.appendAndSend(typ, body); err != nil {
				g.log.Printf("emit: %v", err)
				return
			}
			if g.o.emitEveryMs > 0 {
				select {
				case <-g.stopEmit:
					return
				case <-time.After(time.Duration(g.o.emitEveryMs) * time.Millisecond):
				}
			}
		}
		g.log.Printf("emitted every synthetic event up to seq %d", total)
	}()
}

// checkBudget applies the budget decision to a synthetic call, as the gateway will before a call goes upstream
// (P4): refused, the record is a 402 with the guard's message and no cost; unchecked, it carries budget "unchecked".
func (g *gateway) checkBudget(body any) {
	r, ok := body.(*ipc.UsageReport)
	if !ok {
		return
	}
	g.mu.Lock()
	snap := g.snap
	g.mu.Unlock()
	// "stub/model-a" normalized as lib/pricing.js normalizeModel does.
	in := ipc.CheckInput{KeyID: r.Record.KeyID, Model: "model-a", Provider: r.Record.Provider}
	switch d := ipc.Decide(snap, time.Now().UnixMilli(), g.o.staleMs, in); d.Action {
	case ipc.Unchecked:
		u := ipc.Unchecked
		r.Budget = &u
	case ipc.Refuse:
		r.Record.Status, r.Record.Error, r.Record.Cost = 402, &d.Message, 0
	}
}

// emitUsage stores a go-engine call in the outbox and sends it (P4b; the control plane stores it through the JS
// gateway's own usage writer, lib/ipc/server-sinks.js).
func (g *gateway) emitUsage(r *ipc.UsageReport) error { return g.appendAndSend(ipc.TypeUsageReport, r) }

// appendAndSend writes an event to the outbox, then sends it (D4: never the other way round), under the outbox's send
// lock, which the replay holds too (E10).
func (g *gateway) appendAndSend(typ string, body any) error {
	_, err := g.box.AppendSend(typ, body, time.Now().UnixMilli(), g.ch.SendLine)
	return err
}

func (g *gateway) stopEmitter(grace time.Duration) {
	if g.stopEmit == nil {
		return
	}
	close(g.stopEmit)
	select {
	case <-g.emitDone:
	case <-time.After(grace):
		g.log.Print("the emitter did not stop within the grace period")
	}
}

func (g *gateway) healthLoop() {
	t := time.NewTicker(time.Duration(g.o.healthMs) * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-g.done:
			return
		case <-t.C:
			g.sendHealth()
		}
	}
}

func (g *gateway) sendHealth() {
	g.mu.Lock()
	if g.quiet {
		g.mu.Unlock()
		return
	}
	h := ipc.Health{OutboxDepth: g.box.Depth(), UptimeMs: time.Since(g.started).Milliseconds(), Gen: g.cfgGen}
	if g.front != nil {
		h.Inflight = g.front.Inflight()
	}
	if g.snap != nil {
		age := time.Since(g.snapAt).Milliseconds()
		h.SnapshotAgeMs = &age
	}
	g.mu.Unlock()
	if err := g.ch.Send(ipc.TypeHealth, h, "", ""); err != nil {
		g.log.Printf("health: %v", err)
	}
}

// drain: the control plane closed the channel.
func (g *gateway) drain() {
	g.log.Print("the control plane closed the channel: exiting")
	g.closeFront(0) // nothing to forward to any more
	g.stopEmitter(time.Second)
}

func (g *gateway) shutdown(grace time.Duration) {
	g.log.Printf("shutting down (grace %s)", grace)
	g.closeFront(grace)
	g.stopEmitter(grace)
}
