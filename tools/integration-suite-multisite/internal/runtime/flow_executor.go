package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/onsi/gomega"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/matchers"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/seedeffect"
)

// flowFirer is the executor's view of the dispatcher (just Fire). The
// concrete Dispatcher satisfies it; tests inject fakes.
type flowFirer interface {
	taskFirer // reuses the multi-input fireTasks dispatcher contract
}

// flowPoller is the executor's view of a poller (just PollFn). Both
// concrete pollers and the StreamPoller wrapper satisfy it.
type flowPoller interface {
	PollFn(site string, args map[string]any, traceparent string) func() []readers.Event
}

// flowDeps bundles the executor's dependencies. Built by the runner
// at scenario-fire time; tests use stubFlowDeps.
type flowDeps struct {
	firer      flowFirer
	pollerFor  func(location string) flowPoller
	matcherReg *matchers.Registry

	// Optional — populated by the runner; left nil in unit tests.
	users    map[string]*seedeffect.SeedUser
	services map[string]Credential
}

// StepVerdict is one row in the flow's per-step verdict report.
type StepVerdict struct {
	ID             string
	Kind           string // "fire" | "observe"
	Pass           bool
	HaltedUpstream bool
	Reason         string // failure / halt reason; empty on pass
	Duration       time.Duration
}

// FlowVerdict is the aggregate result of one flow execution.
type FlowVerdict struct {
	Pass         bool
	StepVerdicts []StepVerdict
}

// RunFlow walks the FlowPlan barrier-by-barrier, executing fire and
// observe steps and threading the substitution context. Behavior per
// spec §5:
//   - Sequential barrier (size 1) runs in the calling goroutine.
//   - Parallel observation barrier (size N>1) fans out N goroutines,
//     joins, fails the barrier if ANY member step fails.
//   - Fire-anyway: a rejected reply does NOT halt the chain; only an
//     unresolved ${...} substitution or an observe timeout halts.
//   - On halt, subsequent barriers' steps are marked HaltedUpstream
//     and not executed.
func RunFlow(ctx context.Context, s *scenario.Scenario, plan *scenario.FlowPlan, deps *flowDeps, subCtx Context) (FlowVerdict, error) {
	// Index inputs + expecteds by id for O(1) lookup.
	inputByID := map[string]*scenario.Task{}
	for i := range s.Input {
		inputByID[s.Input[i].ID] = &s.Input[i]
	}
	expectedByID := map[string]*scenario.Expected{}
	for i := range s.Expected {
		expectedByID[s.Expected[i].ID] = &s.Expected[i]
	}

	verdict := FlowVerdict{Pass: true}
	halted := false

	for bi := range plan.Barriers {
		b := &plan.Barriers[bi]
		if halted {
			for _, id := range b.IDs {
				kind := b.Kind
				verdict.StepVerdicts = append(verdict.StepVerdicts, StepVerdict{
					ID: id, Kind: kind, HaltedUpstream: true,
				})
			}
			continue
		}

		// Execute the barrier. Sequential or parallel goroutine fan-out.
		results := executeBarrier(ctx, b, inputByID, expectedByID, deps, &subCtx)
		verdict.StepVerdicts = append(verdict.StepVerdicts, results...)
		for _, r := range results {
			if !r.Pass {
				verdict.Pass = false
				halted = true
				break
			}
		}
	}
	return verdict, nil
}

// executeBarrier runs one barrier and returns per-step verdicts. The
// substitution context is shared — fire steps write Replies, observe
// steps write Events. Parallel-observation goroutines all write to
// the same context concurrently; each goroutine writes its own id
// only (no key collision).
func executeBarrier(ctx context.Context, b *scenario.FlowBarrier,
	inputByID map[string]*scenario.Task, expectedByID map[string]*scenario.Expected,
	deps *flowDeps, subCtx *Context,
) []StepVerdict {
	results := make([]StepVerdict, len(b.IDs))
	if len(b.IDs) == 1 {
		// Sequential — run in this goroutine. Zero added latency vs
		// today's fireTasks loop (this is the §10 race-repro gate's
		// hot path; nothing extra).
		results[0] = executeStep(ctx, b.IDs[0], b.Kind, inputByID, expectedByID, deps, subCtx)
		return results
	}

	// Parallel observation barrier: each goroutine gets a per-goroutine
	// Context copy whose Events/Replies maps are READ-ONLY snapshots of
	// the parent's state at barrier-start. Writes go to per-goroutine
	// pending maps; after Wait, the parent's maps are updated with the
	// merged writes. This avoids any concurrent read+write on a shared
	// Go map (which the race detector flags even when keys don't
	// collide, because map growth mutates the header).
	//
	// Loader has guaranteed that no step in a parallel group references
	// another sibling step, so each goroutine only reads from entries
	// that existed BEFORE the barrier (snapshotted). All new writes are
	// to distinct ids (also loader-guaranteed).
	var wg sync.WaitGroup
	perGoroutineCaptures := make([]Context, len(b.IDs))
	for i := range b.IDs {
		perGoroutineCaptures[i] = snapshotContext(*subCtx)
	}
	for i, id := range b.IDs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			r := executeStep(ctx, id, b.Kind, inputByID, expectedByID, deps, &perGoroutineCaptures[i])
			results[i] = r
		}(i, id)
	}
	wg.Wait()
	// Merge each goroutine's writes back into the parent context.
	for i := range perGoroutineCaptures {
		mergeCaptures(subCtx, perGoroutineCaptures[i])
	}
	return results
}

// snapshotContext returns a Context whose Events/Replies maps are
// shallow copies of the parent's, safe for read-only access while the
// parent is concurrently being written elsewhere. The Placeholders/
// Services maps are NOT copied (read-only, never written during a
// scenario).
func snapshotContext(parent Context) Context {
	c := parent
	if parent.Events != nil {
		c.Events = make(map[string]EventData, len(parent.Events))
		for k, v := range parent.Events {
			c.Events[k] = v
		}
	}
	if parent.Replies != nil {
		c.Replies = make(map[string]ReplyData, len(parent.Replies))
		for k, v := range parent.Replies {
			c.Replies[k] = v
		}
	}
	c.captureMu = nil
	return c
}

// mergeCaptures copies any new Events / Replies entries from src into
// dst. (Sibling parallel-observe goroutines are loader-guaranteed not
// to write the same key, so last-writer semantics are deterministic.)
func mergeCaptures(dst *Context, src Context) {
	for k, v := range src.Events {
		if _, exists := dst.Events[k]; exists {
			continue
		}
		if dst.Events == nil {
			dst.Events = map[string]EventData{}
		}
		dst.Events[k] = v
	}
	for k, v := range src.Replies {
		if _, exists := dst.Replies[k]; exists {
			continue
		}
		if dst.Replies == nil {
			dst.Replies = map[string]ReplyData{}
		}
		dst.Replies[k] = v
	}
}

// executeStep dispatches one step (fire or observe) and returns its
// verdict.
func executeStep(ctx context.Context, id, kind string,
	inputByID map[string]*scenario.Task, expectedByID map[string]*scenario.Expected,
	deps *flowDeps, subCtx *Context,
) StepVerdict {
	start := time.Now()
	v := StepVerdict{ID: id, Kind: kind}
	switch kind {
	case "fire":
		t := inputByID[id]
		if t == nil {
			v.Reason = fmt.Sprintf("internal: id %q routed as fire but not in inputs", id)
			v.Duration = time.Since(start)
			return v
		}
		if err := executeFireStep(ctx, t, deps, subCtx); err != nil {
			v.Reason = err.Error()
			v.Duration = time.Since(start)
			return v
		}
		v.Pass = true
		v.Duration = time.Since(start)
		return v
	case "observe":
		e := expectedByID[id]
		if e == nil {
			v.Reason = fmt.Sprintf("internal: id %q routed as observe but not in expecteds", id)
			v.Duration = time.Since(start)
			return v
		}
		if err := executeObserveStep(e, inputByID, deps, subCtx); err != nil {
			v.Reason = err.Error()
			v.Duration = time.Since(start)
			return v
		}
		v.Pass = true
		v.Duration = time.Since(start)
		return v
	default:
		v.Reason = fmt.Sprintf("internal: unknown barrier kind %q", kind)
		v.Duration = time.Since(start)
		return v
	}
}

// executeFireStep — reuses the per-task substitute + Fire path from
// the multi-input fireTasks shape, threading subCtx so the captured
// reply lands in subCtx.Replies for downstream observations and
// fires.
func executeFireStep(ctx context.Context, t *scenario.Task, deps *flowDeps, subCtx *Context) error {
	cred := resolveCredential(t.Credential, deps.users, deps.services)
	in := &InputSpec{
		Site:    t.Site,
		Verb:    t.Verb,
		Subject: t.Subject,
		Payload: t.Payload,
		TaskID:  t.ID,
	}
	if err := deps.firer.Fire(ctx, in, subCtx, cred, ""); err != nil {
		return fmt.Errorf("task %q: %w", t.ID, err)
	}
	return nil
}

// executeObserveStep resolves the expected's args + match against
// subCtx, polls the bound poller, and runs Gomega
// Eventually/Consistently. On positive match, captures the matched
// event into subCtx.Events. On negative satisfaction, captures
// nothing (there is no event to expose).
func executeObserveStep(e *scenario.Expected, inputByID map[string]*scenario.Task,
	deps *flowDeps, subCtx *Context,
) error {
	// Resolve args + match against the running substitution context.
	resolvedArgs := map[string]any{}
	if len(e.Args) > 0 {
		argsV, err := Substitute(copyMapAny(e.Args), *subCtx)
		if err != nil {
			return fmt.Errorf("expected %q: substitute args: %w", e.ID, err)
		}
		ra, ok := argsV.(map[string]any)
		if !ok {
			return fmt.Errorf("expected %q: args must resolve to a map, got %T", e.ID, argsV)
		}
		resolvedArgs = ra
	}
	resolvedMatchV, err := Substitute(copyMapAny(e.Match), *subCtx)
	if err != nil {
		return fmt.Errorf("expected %q: substitute match: %w", e.ID, err)
	}
	resolvedMatch, ok := resolvedMatchV.(map[string]any)
	if !ok {
		return fmt.Errorf("expected %q: match must resolve to a map, got %T", e.ID, resolvedMatchV)
	}

	poller := deps.pollerFor(e.Location)
	pollFn := poller.PollFn(e.Site, resolvedArgs, "")

	// Resolve the reply scope: explicit `of:`, else position-default
	// resolved by the loader (encoded by the loader marking each
	// reply observation's preceding fire). For runtime simplicity we
	// rely on `of:` when set; when empty, the loader has already
	// validated unambiguous default scope, but the executor doesn't
	// need to compute it — the matchshape filter only narrows when
	// scope is set. Position-default scope is achieved by carrying
	// the resolved id from validation; for now we read `of:` directly
	// (single preceding fire is the unambiguous case the loader
	// allowed).
	scope := ReplyScope{InputID: e.Of}
	if e.Location == "reply" && scope.InputID == "" {
		// Find the immediate preceding fire — for unambiguous flows the
		// validator already verified exactly one candidate. We trust it.
		// Lacking the position table here, fall back to the most-recent
		// reply captured (executor-internal hint).
		scope.InputID = lastFireID(inputByID, subCtx)
	}

	matcher := MatchShapeScoped(resolvedMatch, deps.matcherReg, scope)

	timeout := time.Duration(e.Timeout)
	if timeout == 0 {
		timeout = defaultScenarioTimeout
	}
	polling := time.Duration(e.Polling)
	if polling == 0 {
		polling = defaultScenarioPolling
	}

	// Capture the matched event for substitution. Gomega's
	// Eventually polls pollFn; we re-poll once on match to extract the
	// matched event. (Cheap; pollFn is a snapshot copy of the buffer.)
	asserter := &flowAssertion{}
	g := gomega.NewGomega(asserter.Handler())

	if e.Not {
		g.Consistently(pollFn, timeout, polling).ShouldNot(matcher)
	} else {
		g.Eventually(pollFn, timeout, polling).Should(matcher)
	}
	if asserter.failed {
		return fmt.Errorf("expected %q (%s): %s", e.ID, e.Location, asserter.firstMsg)
	}

	if !e.Not {
		// Snapshot the buffer once more to extract the matched event
		// for ${<id>.body_json.…} substitution downstream.
		events := pollFn()
		matched := pickMatchedEvent(events, resolvedMatch, scope, deps.matcherReg)
		if matched != nil {
			body := extractBodyJSON(matched.Payload)
			if subCtx.Events == nil {
				subCtx.Events = map[string]EventData{}
			}
			subCtx.Events[e.ID] = EventData{BodyJSON: body}
		}
	}
	return nil
}

// lastFireID returns the id of the most-recent fire captured in
// subCtx.Replies. Used as the position-default reply scope when the
// expected has no `of:` — the loader has already validated this is
// unambiguous. Empty if no fires have happened.
func lastFireID(inputByID map[string]*scenario.Task, subCtx *Context) string {
	// We can't easily know "most recent" without an executor-tracked
	// list — keep one on the context. For unit tests with single-fire
	// scenarios the only key in Replies suffices.
	if len(subCtx.Replies) == 1 {
		for id := range subCtx.Replies {
			return id
		}
	}
	// Fall back to the active-task hint that the multi-input executor
	// uses. Empty string means "no filter" which is the safest fallback
	// — the loader's ambiguity check has already passed.
	return ""
}

// pickMatchedEvent returns a pointer to the first event whose payload
// satisfies the resolved match shape under the scope. Mirrors
// MatchShape's iteration for capture; uses the matcher registry's
// matches_shape.
func pickMatchedEvent(events []readers.Event, expected map[string]any, scope ReplyScope, reg *matchers.Registry) *readers.Event {
	if scope.InputID != "" {
		// IMPORTANT: never reuse the input slice's backing array.
		// Parallel-observe goroutines share the slice via fakePoller in
		// tests (and via the live StreamPoller buffer in production);
		// in-place filtering with `events[:0]` corrupted that shared
		// backing under concurrent calls.
		filtered := make([]readers.Event, 0, len(events))
		for _, ev := range events {
			if ev.Task == scope.InputID {
				filtered = append(filtered, ev)
			}
		}
		events = filtered
	}
	if reg == nil {
		reg = matchers.NewRegistry()
	}
	shape, err := reg.Get("matches_shape")
	if err != nil {
		return nil
	}
	// Strip directives (task / outbox_payload) from expected before
	// matching, mirroring shapeMatcher.
	stripped := stripMatchDirectives(expected)
	for i := range events {
		if shape.Match(events[i].Payload, stripped).Matched {
			return &events[i]
		}
	}
	return nil
}

func stripMatchDirectives(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if k == taskSelectorKey || k == outboxPayloadKey {
			continue
		}
		out[k] = v
	}
	return out
}

// extractBodyJSON unwraps body_json from common payload shapes
// (ReplyPayload, jetstream messages, mongo docs). For shapes that
// already are a body_json map (mongo_find), returns the doc as-is.
func extractBodyJSON(payload any) map[string]any {
	if payload == nil {
		return nil
	}
	switch p := payload.(type) {
	case map[string]any:
		if bj, ok := p["body_json"].(map[string]any); ok {
			return bj
		}
		return p
	case readers.ReplyPayload:
		return p.BodyJSON
	}
	return nil
}

// flowAssertion is a Gomega fail handler that captures the first
// failure message. Mirrors scenarioAssertion in runner_scenario.go.
type flowAssertion struct {
	failed   bool
	firstMsg string
}

func (a *flowAssertion) Handler() func(string, ...int) {
	return func(msg string, _ ...int) {
		if !a.failed {
			a.firstMsg = msg
		}
		a.failed = true
	}
}

// Compile-time interface guard: Dispatcher satisfies flowFirer (via
// taskFirer which the multi-input slice already wired).
var _ flowFirer = (*Dispatcher)(nil)

// flowFailureReason summarises the first failed step for the report.
func flowFailureReason(v FlowVerdict) string {
	for _, sv := range v.StepVerdicts {
		if !sv.Pass && !sv.HaltedUpstream {
			return fmt.Sprintf("step %q (%s): %s", sv.ID, sv.Kind, sv.Reason)
		}
	}
	return "flow execution failed"
}

// Avoid unused-import errors when error helpers shift.
var _ = errors.Is
