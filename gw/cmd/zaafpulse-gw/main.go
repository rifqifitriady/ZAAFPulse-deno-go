// Command zaafpulse-gw is the gateway process of ZAAFPulse v2. In P3 it is a stub: it speaks the whole v1 IPC protocol
// with the control plane over stdin and stdout (ipc/v1; docs/superpowers/specs/2026-11-11-p3-ipc-contract-design.md)
// and serves no HTTP. It logs to stderr; the control plane forwards each line with a "gw: " prefix. When stdin reaches
// end of file (the control plane is gone) it exits 0.
//
// Exit codes: 0 done (shutdown or end of input), 1 an I/O failure, 2 refused or a bad command line, 3 a protocol
// error (after an "error" message) or --crash-after-ready-ms.
//
// Test-only flags make it misbehave for the supervisor tests: --protocol, --hang-after-ready, --crash-after-ready-ms,
// --bad-line and --ignore-shutdown.
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

	"github.com/rifqifitriady/ZAAFPulse-deno-go/gw/ipc"
)

const version = "0.3.0-stub"

type options struct {
	outbox            string
	healthMs          int
	protocol          string
	hangAfterReady    bool
	crashAfterReadyMs int
	badLine           bool
	ignoreShutdown    bool
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
	if err := fs.Parse(args); err != nil {
		return 2
	}
	dir := o.outbox
	if dir == "" {
		data := os.Getenv("ZAAFPULSE_DATA")
		if data == "" {
			logger.Print("no outbox folder: pass --outbox or set ZAAFPULSE_DATA")
			return 2
		}
		dir = filepath.Join(data, ".data", "gw-outbox")
	}
	protocols, err := parseProtocols(o.protocol)
	if err != nil {
		logger.Print(err)
		return 2
	}
	box, err := ipc.OpenOutbox(dir)
	if err != nil {
		logger.Printf("outbox: %v", err)
		return 1
	}
	defer box.Close()
	g := &gateway{
		o:       o,
		box:     box,
		ch:      ipc.NewChannel(stdin, stdout, false),
		log:     logger,
		started: time.Now(),
		done:    make(chan struct{}),
		unknown: map[string]bool{},
	}
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

	mu      sync.Mutex
	cfgGen  int64
	secGen  int64
	secrets map[string]ipc.ProviderSecret // in memory only: never logged, never written to the outbox
	mask    []string
	snap    *ipc.BudgetSnapshot
	snapAt  time.Time
	ready   bool
	quiet   bool // --hang-after-ready
}

func (g *gateway) serve(protocols []int) int {
	defer close(g.done)
	hello := ipc.Hello{Protocol: protocols, GwVersion: version, Stream: g.box.Stream(), OutboxFrom: g.box.Acked() + 1}
	if err := g.ch.Send(ipc.TypeHello, hello, "h1", ""); err != nil {
		g.log.Printf("hello: %v", err)
		return 1
	}
	handshaken := false
	for {
		msg, err := g.ch.Recv()
		if errors.Is(err, io.EOF) || errors.Is(err, ipc.ErrPartialLine) {
			g.drain()
			return 0
		}
		if err != nil {
			return g.protocolError(err)
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
					g.log.Printf("the control plane chose protocol %d; this gateway speaks %d", b.Protocol, ipc.Version)
					return 2
				}
				handshaken = true
				go g.healthLoop()
			case *ipc.HelloRefused:
				g.log.Printf("refused by the control plane: %s", b.Reason)
				return 2
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
		g.mu.Lock()
		g.cfgGen = b.Gen
		g.mu.Unlock()
		g.maybeReady()
	case *ipc.SecretsHandoff:
		m := make(map[string]ipc.ProviderSecret, len(b.Providers))
		for _, p := range b.Providers {
			m[p.ID] = p
		}
		g.mu.Lock()
		g.secGen, g.secrets, g.mask = b.Gen, m, b.Mask
		g.mu.Unlock()
		g.maybeReady()
	case *ipc.BudgetSnapshot:
		g.mu.Lock()
		g.snap, g.snapAt = b, time.Now()
		g.mu.Unlock()
		g.maybeReady()
	case *ipc.Ack:
		// The outbox (Task 4) deletes what is acknowledged.
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

func (g *gateway) protocolError(err error) int {
	g.log.Print(err)
	_ = g.ch.Send(ipc.TypeError, ipc.ErrorBody{Code: "protocol", Message: err.Error()}, "", "")
	return 3
}

// maybeReady sends ready once the first config, its secrets and a snapshot are applied.
func (g *gateway) maybeReady() {
	g.mu.Lock()
	if g.ready || g.cfgGen == 0 || g.secGen != g.cfgGen || g.snap == nil {
		g.mu.Unlock()
		return
	}
	g.ready = true
	gen := g.cfgGen
	g.mu.Unlock()
	if err := g.ch.Send(ipc.TypeReady, ipc.Ready{Gen: gen}, "", ""); err != nil {
		g.log.Printf("ready: %v", err)
		return
	}
	g.log.Printf("ready (config generation %d)", gen)
	g.afterReady()
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
			os.Exit(3)
		})
	}
	if g.o.badLine {
		_ = g.ch.SendLine([]byte("this line is not JSON"))
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
}

func (g *gateway) shutdown(grace time.Duration) {
	g.log.Printf("shutting down (grace %s)", grace)
}
