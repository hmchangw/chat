package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/scenario"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/verbs"
)

// Dispatcher resolves a scenario's input + placeholders into a concrete
// verb call, fires the verb, and pushes the reply into the NATSReplyReader.
type Dispatcher struct {
	VerbReg  *verbs.Registry
	ReplyRdr *readers.NATSReplyReader
}

// FireResult is what Fire returns.
type FireResult struct {
	Traceparent string
	StartTime   time.Time
	Outcome     verbs.Outcome
}

// Fire executes the verb and returns the timing + outcome. The subCtx
// MUST have Site and Placeholders populated; Fire substitutes them
// into input.subject + input.payload and then writes the resolved
// subject/payload/requestId back into subCtx.Input so the classifier
// can use ${input.…} tokens in reads[].expected. `traceparent` is the
// scenario's pre-generated trace (also handed to the observer so
// non-trace-propagating readers can stamp their events with it).
func (d *Dispatcher) Fire(ctx context.Context, s *scenario.Scenario, res *scenario.Resolution, subCtx *Context, traceparent string) (*FireResult, error) {
	tp := traceparent
	startTime := time.Now()

	executor, err := d.VerbReg.Get(verbExecutorName(s.Input.Verb))
	if err != nil {
		return nil, fmt.Errorf("dispatcher: %w", err)
	}

	cred := pickCredential(s.Input.Credential, res, subCtx)

	subjectV, err := Substitute(s.Input.Subject, *subCtx)
	if err != nil {
		return nil, fmt.Errorf("dispatcher: substitute subject: %w", err)
	}
	subject, ok := subjectV.(string)
	if !ok {
		return nil, fmt.Errorf("dispatcher: subject must resolve to a string, got %T", subjectV)
	}

	payloadV, err := Substitute(toAnyMap(s.Input.Payload), *subCtx)
	if err != nil {
		return nil, fmt.Errorf("dispatcher: substitute payload: %w", err)
	}
	payloadMap, ok := payloadV.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("dispatcher: payload must resolve to a map, got %T", payloadV)
	}
	payloadBytes, err := json.Marshal(payloadMap)
	if err != nil {
		return nil, fmt.Errorf("dispatcher: payload marshal: %w", err)
	}

	fireMoment := time.Now()
	out := executor.Execute(ctx, &verbs.Input{
		Subject:     subject,
		Payload:     payloadBytes,
		Credential:  cred,
		Traceparent: tp,
	})
	latency := time.Since(fireMoment)

	// Populate subCtx.Input so reads can assert against ${input.…}.
	subCtx.Input = InputSnapshot{
		Subject:   subject,
		Payload:   payloadMap,
		RequestID: out.RequestID,
	}

	// Only inject into the reply reader for verbs that have a meaningful
	// request/reply shape. JetStream publishes return an ack — not a
	// "reply" in the verdict-mechanism sense — and emitting a reply
	// Event for them would surface as unexpected-cascade in pure
	// jetstream_publish scenarios that don't (and shouldn't) declare a
	// reply read. Hardcoded list mirrors verbExecutorName above; the
	// catalog-driven version is the §9.4 validator-rebuild bundle.
	if d.ReplyRdr != nil && verbProducesReply(s.Input.Verb) {
		d.ReplyRdr.Inject(&out, latency, tp, time.Now(), "")
	}

	return &FireResult{Traceparent: tp, StartTime: startTime, Outcome: out}, nil
}

// verbExecutorName maps a scenario's `input.verb` to the registry key
// the executor was registered under. Currently hardcoded — Audit-B in
// the corrections-spec §9.4 (validator-rebuild bundle) calls out that
// this should be registry-driven via the catalog's `executor:` field
// once the catalog grows. As of 2026-05-24, two verbs in the catalog
// (nats_request, jetstream_publish) — the hardcoded switch is exactly
// at the size where it starts to drag.
func verbExecutorName(verb string) string {
	switch verb {
	case "nats_request":
		return "NATSRequestExecutor"
	case "jetstream_publish":
		return "JetStreamPublishExecutor"
	}
	return verb
}

// verbProducesReply reports whether the given verb has a synchronous
// request/reply shape whose outcome should flow into the reply reader.
// Verbs like jetstream_publish are fire-with-ack — their outcome is an
// ack, not a "reply" the verdict mechanism cares about. Same hardcoded
// pattern as verbExecutorName; see §9.4 for the registry-driven fix.
func verbProducesReply(verb string) bool {
	return verb == "nats_request"
}

// toAnyMap copies a map[string]any so Substitute returns a fresh map
// without mutating the scenario's parsed payload.
func toAnyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// pickCredential resolves a scenario's `input.credential` reference
// to a concrete verbs.Credential. Recognises two namespaces:
//
//	${<placeholder>.credential}  → cast user JWT/NkeySeed (user-level)
//	${service.<name>}            → service .creds file (service-level)
//
// The service namespace is needed for scenarios that publish to
// service-internal subjects (e.g. JetStream canonical subjects only
// services have pub permission for, per the chatapp account's
// permission boundary). See verbs.Credential's godoc.
func pickCredential(ref string, res *scenario.Resolution, subCtx *Context) verbs.Credential {
	trimmed := strings.TrimPrefix(ref, "$")
	trimmed = strings.TrimPrefix(trimmed, "{")
	trimmed = strings.TrimSuffix(trimmed, "}")
	parts := strings.SplitN(trimmed, ".", 2)
	if len(parts) == 0 {
		return verbs.Credential{}
	}
	if parts[0] == "service" && len(parts) == 2 && subCtx != nil {
		if c, ok := subCtx.Services[parts[1]]; ok {
			return verbs.Credential{
				Account:   c.Account,
				JWT:       c.JWT,
				NkeySeed:  c.NkeySeed,
				CredsFile: c.CredsFile,
			}
		}
		return verbs.Credential{}
	}
	u, ok := res.Users[parts[0]]
	if !ok {
		return verbs.Credential{}
	}
	return verbs.Credential{
		Account:  u.Account,
		JWT:      u.JWT,
		NkeySeed: u.NkeySeed,
	}
}

var currentRunIDValue string

func currentRunID() string { return currentRunIDValue }

// SetRunID is called by the runner before dispatch.
func SetRunID(id string) { currentRunIDValue = id }
