// Package ipc is the gateway side of the ZAAFPulse IPC v1 contract (ipc/v1 at the repo root): the envelope, the
// message bodies, strict decoding for tests and lenient decoding for production, framing, the stdio channel, the
// durable outbox and the budget decision.
package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
)

// Version is the major protocol version this package speaks.
const Version = 1

// Envelope wraps every message. Body is the raw JSON of the message's body.
type Envelope struct {
	V    int             `json:"v"`
	Type string          `json:"type" ipc:"minlen=1"`
	Seq  int64           `json:"seq" ipc:"min=1"`
	Ts   int64           `json:"ts" ipc:"min=0"`
	ID   string          `json:"id,omitzero" ipc:"minlen=1"`
	Re   string          `json:"re,omitzero" ipc:"minlen=1"`
	Body json.RawMessage `json:"body"`
}

// Message is a decoded envelope with its typed body (a pointer to one of the structs in messages.go). Unknown is
// true for a type v1 does not know: the receiver logs it once and drops it.
type Message struct {
	Envelope
	Body    any
	Unknown bool
}

var bodies = map[string]func() any{
	TypeHello:          func() any { return new(Hello) },
	TypeHelloOK:        func() any { return new(HelloOK) },
	TypeHelloRefused:   func() any { return new(HelloRefused) },
	TypeConfigPush:     func() any { return new(ConfigPush) },
	TypeSecretsHandoff: func() any { return new(SecretsHandoff) },
	TypeBudgetSnapshot: func() any { return new(BudgetSnapshot) },
	TypeShutdown:       func() any { return new(Shutdown) },
	TypeAck:            func() any { return new(Ack) },
	TypeReady:          func() any { return new(Ready) },
	TypeHealth:         func() any { return new(Health) },
	TypeUsageReport:    func() any { return new(UsageReport) },
	TypeAuditEvent:     func() any { return new(AuditEventBody) },
	TypeTraceSpan:      func() any { return new(TraceSpanBody) },
	TypeError:          func() any { return new(ErrorBody) },
}

// Types lists the v1 message types, sorted.
func Types() []string {
	out := make([]string, 0, len(bodies))
	for t := range bodies {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// postDecoder is a body that decodes part of itself after the first pass (AuditEventBody).
type postDecoder interface{ afterDecode(strict bool) error }

// Decode reads one message. strict (tests) refuses unknown fields, as the schemas do; production decodes leniently
// and ignores them (the v1 version rule). A version other than 1 is always an error.
func Decode(line []byte, strict bool) (Message, error) {
	var generic map[string]any
	if err := json.Unmarshal(line, &generic); err != nil || generic == nil {
		return Message{}, errors.New("envelope: not a JSON object")
	}
	if err := checkShape(generic, reflect.TypeFor[Envelope](), "envelope", strict); err != nil {
		return Message{}, err
	}
	if _, ok := generic["body"].(map[string]any); !ok {
		return Message{}, errors.New("envelope.body: must be an object")
	}
	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return Message{}, fmt.Errorf("envelope: %w", err)
	}
	if env.Type == TypeSecretsHandoff {
		// E6: the raw body holds the secrets. It is decoded below, then zeroed, so no envelope that is logged,
		// formatted or returned with an error carries them (the typed body's formatters redact them).
		raw := env.Body
		env.Body = nil
		defer clear(raw)
		if env.V == Version {
			body := new(SecretsHandoff)
			if err := decodeBody(raw, body, strict, "body"); err != nil {
				return Message{Envelope: env}, fmt.Errorf("%s: %w", env.Type, err)
			}
			return Message{Envelope: env, Body: body}, nil
		}
	}
	if env.V != Version {
		return Message{Envelope: env}, fmt.Errorf("envelope.v: version %d is not %d", env.V, Version)
	}
	ctor, ok := bodies[env.Type]
	if !ok {
		return Message{Envelope: env, Unknown: true}, nil
	}
	body := ctor()
	if err := decodeBody(env.Body, body, strict, "body"); err != nil {
		return Message{Envelope: env}, fmt.Errorf("%s: %w", env.Type, err)
	}
	return Message{Envelope: env, Body: body}, nil
}

func decodeBody(raw json.RawMessage, v any, strict bool, path string) error {
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if err := checkShape(generic, reflect.TypeOf(v), path, strict); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if p, ok := v.(postDecoder); ok {
		return p.afterDecode(strict)
	}
	return nil
}

// Encode writes one message as a single line of JSON, without the newline.
func Encode(env Envelope, body any) ([]byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	env.Body = raw
	return json.Marshal(env)
}
