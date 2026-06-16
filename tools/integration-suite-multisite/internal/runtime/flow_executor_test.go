package runtime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/verbs"
)

// fakeFlowFirer scripts replies per task id and records call order.
// Reused as the flow executor's dispatcher.
type fakeFlowFirer struct {
	calls   []string                  // order of TaskIDs fired
	replies map[string]map[string]any // taskID → body_json to inject
	errOn   map[string]error          // taskID → Fire error (simulates unresolved sub at dispatch)
}

func (f *fakeFlowFirer) Fire(_ context.Context, in *InputSpec, subCtx *Context, _ verbs.Credential, _ string) error {
	// Mirror the real Dispatcher.Fire's substitute step so an unresolved
	// ${...} surfaces here (the executor relies on this to halt).
	if _, err := Substitute(in.Subject, *subCtx); err != nil {
		return fmt.Errorf("substitute subject: %w", err)
	}
	if _, err := Substitute(toAnyMap(in.Payload), *subCtx); err != nil {
		return fmt.Errorf("substitute payload: %w", err)
	}
	f.calls = append(f.calls, in.TaskID)
	if err, ok := f.errOn[in.TaskID]; ok {
		return err
	}
	if body, ok := f.replies[in.TaskID]; ok {
		if subCtx.Replies == nil {
			subCtx.Replies = map[string]ReplyData{}
		}
		subCtx.Replies[in.TaskID] = ReplyData{BodyJSON: body}
	}
	return nil
}

// fakePoller emits a fixed event slice on every poll. Backs every
// observation step in flow executor unit tests.
type fakePoller struct {
	events []readers.Event
}

func (p *fakePoller) PollFn(_ string, _ map[string]any, _ string) func() []readers.Event {
	return func() []readers.Event { return p.events }
}

// stubFlowDeps builds the minimum FlowDeps for unit tests with
// per-id reply / observation fixtures.
func stubFlowDeps(firer flowFirer, observations map[string][]readers.Event) *flowDeps {
	return &flowDeps{
		firer:      firer,
		pollerFor:  func(loc string) flowPoller { return &fakePoller{events: observations[loc]} },
		matcherReg: nil, // MatchShape falls back to default registry
	}
}

func tinyCtx() Context {
	return Context{Placeholders: map[string]map[string]any{}}
}

// ── Behavior tests ─────────────────────────────────────────────────

func TestRunFlow_SinglePassThrough(t *testing.T) {
	plan := &scenario.FlowPlan{Barriers: []scenario.FlowBarrier{
		{IDs: []string{"a"}, Kind: "fire"},
	}}
	s := &scenario.Scenario{
		Input: scenario.TaskList{{ID: "a", Site: "site-a", Verb: "nats_request", Subject: "chat.x"}},
	}
	firer := &fakeFlowFirer{replies: map[string]map[string]any{"a": {"status": "accepted"}}}
	v, err := RunFlow(context.Background(), s, plan, stubFlowDeps(firer, nil), tinyCtx())
	require.NoError(t, err)
	assert.True(t, v.Pass, "single-fire flow should pass")
	require.Len(t, v.StepVerdicts, 1)
	assert.Equal(t, "a", v.StepVerdicts[0].ID)
	assert.Equal(t, "fire", v.StepVerdicts[0].Kind)
	assert.True(t, v.StepVerdicts[0].Pass)
}

func TestRunFlow_TwoFireChain_OrderPreserved(t *testing.T) {
	plan := &scenario.FlowPlan{Barriers: []scenario.FlowBarrier{
		{IDs: []string{"a"}, Kind: "fire"},
		{IDs: []string{"b"}, Kind: "fire"},
	}}
	s := &scenario.Scenario{
		Input: scenario.TaskList{
			{ID: "a", Site: "site-a", Verb: "nats_request", Subject: "chat.a"},
			{ID: "b", Site: "site-a", Verb: "nats_request", Subject: "chat.b"},
		},
	}
	firer := &fakeFlowFirer{}
	v, err := RunFlow(context.Background(), s, plan, stubFlowDeps(firer, nil), tinyCtx())
	require.NoError(t, err)
	assert.True(t, v.Pass)
	assert.Equal(t, []string{"a", "b"}, firer.calls)
}

func TestRunFlow_GatePass(t *testing.T) {
	// fire → observe gate that matches → fire reading from the gate's
	// captured event in its subject.
	plan := &scenario.FlowPlan{Barriers: []scenario.FlowBarrier{
		{IDs: []string{"fire_a"}, Kind: "fire"},
		{IDs: []string{"gate"}, Kind: "observe"},
		{IDs: []string{"fire_b"}, Kind: "fire"},
	}}
	s := &scenario.Scenario{
		Input: scenario.TaskList{
			{ID: "fire_a", Site: "site-a", Verb: "nats_request", Subject: "chat.create"},
			{ID: "fire_b", Site: "site-a", Verb: "nats_request",
				Subject: "chat.${gate.body_json.roomId}.rename"},
		},
		Expected: []scenario.Expected{
			{ID: "gate", Location: "mongo_find", Site: "site-a",
				Args:    map[string]any{"collection": "rooms"},
				Match:   map[string]any{"name": "Engineering"},
				Timeout: scenario.Duration(2 * time.Second), Polling: scenario.Duration(20 * time.Millisecond)},
		},
	}
	firer := &fakeFlowFirer{}
	obs := map[string][]readers.Event{
		"mongo_find": {{Location: "mongo_find",
			Payload: map[string]any{"name": "Engineering", "roomId": "r-789"}}},
	}
	v, err := RunFlow(context.Background(), s, plan, stubFlowDeps(firer, obs), tinyCtx())
	require.NoError(t, err)
	require.True(t, v.Pass, "all steps must pass: %v", v.StepVerdicts)
	// fire_b's subject must have been substituted with the gate's roomId.
	require.Len(t, firer.calls, 2)
}

func TestRunFlow_GateTimesOut_HaltsDownstream(t *testing.T) {
	plan := &scenario.FlowPlan{Barriers: []scenario.FlowBarrier{
		{IDs: []string{"fire_a"}, Kind: "fire"},
		{IDs: []string{"gate"}, Kind: "observe"},
		{IDs: []string{"fire_b"}, Kind: "fire"},
	}}
	s := &scenario.Scenario{
		Input: scenario.TaskList{
			{ID: "fire_a", Site: "site-a", Verb: "nats_request", Subject: "chat.x"},
			{ID: "fire_b", Site: "site-a", Verb: "nats_request", Subject: "chat.y"},
		},
		Expected: []scenario.Expected{
			{ID: "gate", Location: "mongo_find", Site: "site-a",
				Args:    map[string]any{"collection": "rooms"},
				Match:   map[string]any{"name": "NeverArrives"},
				Timeout: scenario.Duration(50 * time.Millisecond),
				Polling: scenario.Duration(10 * time.Millisecond)},
		},
	}
	firer := &fakeFlowFirer{}
	v, err := RunFlow(context.Background(), s, plan, stubFlowDeps(firer, nil), tinyCtx())
	require.NoError(t, err)
	assert.False(t, v.Pass)
	require.Len(t, firer.calls, 1, "fire_b must not execute after the gate times out")
	assert.Equal(t, "fire_a", firer.calls[0])
	// Halted-upstream marking
	var gateVerdict, fireBVerdict *StepVerdict
	for i := range v.StepVerdicts {
		switch v.StepVerdicts[i].ID {
		case "gate":
			gateVerdict = &v.StepVerdicts[i]
		case "fire_b":
			fireBVerdict = &v.StepVerdicts[i]
		}
	}
	require.NotNil(t, gateVerdict)
	assert.False(t, gateVerdict.Pass)
	assert.Contains(t, gateVerdict.Reason, "gate")
	require.NotNil(t, fireBVerdict)
	assert.True(t, fireBVerdict.HaltedUpstream)
}

func TestRunFlow_FireAnyway_RejectedReplyDoesNotHalt(t *testing.T) {
	plan := &scenario.FlowPlan{Barriers: []scenario.FlowBarrier{
		{IDs: []string{"a"}, Kind: "fire"},
		{IDs: []string{"b"}, Kind: "fire"},
	}}
	s := &scenario.Scenario{
		Input: scenario.TaskList{
			{ID: "a", Site: "site-a", Verb: "nats_request", Subject: "chat.a"},
			{ID: "b", Site: "site-a", Verb: "nats_request", Subject: "chat.b"},
		},
	}
	firer := &fakeFlowFirer{replies: map[string]map[string]any{
		"a": {"status": "rejected"}, // rejected, but no error from Fire
	}}
	v, err := RunFlow(context.Background(), s, plan, stubFlowDeps(firer, nil), tinyCtx())
	require.NoError(t, err)
	assert.True(t, v.Pass, "rejected reply does not halt; both fires execute")
	assert.Equal(t, []string{"a", "b"}, firer.calls)
}

func TestRunFlow_UnresolvedSubstitution_Halts(t *testing.T) {
	// fire_b references fire_a.reply.body_json.missing — fire_a's reply
	// has no such field; substitution fails when fire_b is dispatched.
	plan := &scenario.FlowPlan{Barriers: []scenario.FlowBarrier{
		{IDs: []string{"a"}, Kind: "fire"},
		{IDs: []string{"b"}, Kind: "fire"},
	}}
	s := &scenario.Scenario{
		Input: scenario.TaskList{
			{ID: "a", Site: "site-a", Verb: "nats_request", Subject: "chat.a"},
			{ID: "b", Site: "site-a", Verb: "nats_request",
				Subject: "chat.${a.reply.body_json.missingField}.x"},
		},
	}
	firer := &fakeFlowFirer{replies: map[string]map[string]any{"a": {"status": "accepted"}}}
	v, err := RunFlow(context.Background(), s, plan, stubFlowDeps(firer, nil), tinyCtx())
	require.NoError(t, err)
	assert.False(t, v.Pass)
	require.Len(t, firer.calls, 1, "fire_b must not execute when its substitution can't resolve")
	// Find b's verdict and confirm the error mentions both ids.
	var bVerdict *StepVerdict
	for i := range v.StepVerdicts {
		if v.StepVerdicts[i].ID == "b" {
			bVerdict = &v.StepVerdicts[i]
		}
	}
	require.NotNil(t, bVerdict)
	assert.False(t, bVerdict.Pass)
	assert.Contains(t, bVerdict.Reason, "a.reply.body_json.missingField")
}

func TestRunFlow_NegativePass(t *testing.T) {
	plan := &scenario.FlowPlan{Barriers: []scenario.FlowBarrier{
		{IDs: []string{"a"}, Kind: "fire"},
		{IDs: []string{"no_err"}, Kind: "observe"},
	}}
	s := &scenario.Scenario{
		Input: scenario.TaskList{{ID: "a", Site: "site-a", Verb: "nats_request", Subject: "chat.x"}},
		Expected: []scenario.Expected{
			{ID: "no_err", Location: "logs_tail", Site: "site-a",
				Args:    map[string]any{"service": "room-worker"},
				Match:   map[string]any{"level": "error"},
				Not:     true,
				Timeout: scenario.Duration(50 * time.Millisecond),
				Polling: scenario.Duration(10 * time.Millisecond)},
		},
	}
	firer := &fakeFlowFirer{}
	// no events for logs_tail — absence is satisfied
	v, err := RunFlow(context.Background(), s, plan, stubFlowDeps(firer, nil), tinyCtx())
	require.NoError(t, err)
	assert.True(t, v.Pass)
}

func TestRunFlow_NegativeFails(t *testing.T) {
	plan := &scenario.FlowPlan{Barriers: []scenario.FlowBarrier{
		{IDs: []string{"a"}, Kind: "fire"},
		{IDs: []string{"no_err"}, Kind: "observe"},
	}}
	s := &scenario.Scenario{
		Input: scenario.TaskList{{ID: "a", Site: "site-a", Verb: "nats_request", Subject: "chat.x"}},
		Expected: []scenario.Expected{
			{ID: "no_err", Location: "logs_tail", Site: "site-a",
				Args:    map[string]any{"service": "room-worker"},
				Match:   map[string]any{"level": "error"},
				Not:     true,
				Timeout: scenario.Duration(50 * time.Millisecond),
				Polling: scenario.Duration(10 * time.Millisecond)},
		},
	}
	firer := &fakeFlowFirer{}
	obs := map[string][]readers.Event{
		"logs_tail": {{Location: "logs_tail", Payload: map[string]any{"level": "error", "msg": "boom"}}},
	}
	v, err := RunFlow(context.Background(), s, plan, stubFlowDeps(firer, obs), tinyCtx())
	require.NoError(t, err)
	assert.False(t, v.Pass)
}

func TestRunFlow_ParallelObserveBarrier_AllPass(t *testing.T) {
	plan := &scenario.FlowPlan{Barriers: []scenario.FlowBarrier{
		{IDs: []string{"a"}, Kind: "fire"},
		{IDs: []string{"obs_a", "obs_b"}, Kind: "observe"},
	}}
	s := &scenario.Scenario{
		Input: scenario.TaskList{{ID: "a", Site: "site-a", Verb: "nats_request", Subject: "chat.x"}},
		Expected: []scenario.Expected{
			{ID: "obs_a", Location: "mongo_find", Site: "site-a",
				Args:    map[string]any{"collection": "rooms"},
				Match:   map[string]any{"name": "Engineering"},
				Timeout: scenario.Duration(200 * time.Millisecond),
				Polling: scenario.Duration(20 * time.Millisecond)},
			{ID: "obs_b", Location: "jetstream_consume", Site: "site-a",
				Args:    map[string]any{"stream": "ROOMS_site-a"},
				Match:   map[string]any{"body_json": map[string]any{"type": "room_created"}},
				Timeout: scenario.Duration(200 * time.Millisecond),
				Polling: scenario.Duration(20 * time.Millisecond)},
		},
	}
	firer := &fakeFlowFirer{}
	obs := map[string][]readers.Event{
		"mongo_find":        {{Payload: map[string]any{"name": "Engineering"}}},
		"jetstream_consume": {{Payload: map[string]any{"body_json": map[string]any{"type": "room_created"}}}},
	}
	v, err := RunFlow(context.Background(), s, plan, stubFlowDeps(firer, obs), tinyCtx())
	require.NoError(t, err)
	assert.True(t, v.Pass, "all parallel observations match: %+v", v.StepVerdicts)
}

func TestRunFlow_OfReplyScope_FilterCorrectFire(t *testing.T) {
	plan := &scenario.FlowPlan{Barriers: []scenario.FlowBarrier{
		{IDs: []string{"t1"}, Kind: "fire"},
		{IDs: []string{"t2"}, Kind: "fire"},
		{IDs: []string{"reply_t1", "reply_t2"}, Kind: "observe"},
	}}
	s := &scenario.Scenario{
		Input: scenario.TaskList{
			{ID: "t1", Site: "site-a", Verb: "nats_request", Subject: "chat.a"},
			{ID: "t2", Site: "site-a", Verb: "nats_request", Subject: "chat.b"},
		},
		Expected: []scenario.Expected{
			{ID: "reply_t1", Location: "reply", Of: "t1",
				Match:   map[string]any{"body_json": map[string]any{"status": "ok-t1"}},
				Timeout: scenario.Duration(100 * time.Millisecond),
				Polling: scenario.Duration(20 * time.Millisecond)},
			{ID: "reply_t2", Location: "reply", Of: "t2",
				Match:   map[string]any{"body_json": map[string]any{"status": "ok-t2"}},
				Timeout: scenario.Duration(100 * time.Millisecond),
				Polling: scenario.Duration(20 * time.Millisecond)},
		},
	}
	firer := &fakeFlowFirer{}
	obs := map[string][]readers.Event{
		"reply": {
			{Location: "reply", Task: "t1", Payload: map[string]any{"body_json": map[string]any{"status": "ok-t1"}}},
			{Location: "reply", Task: "t2", Payload: map[string]any{"body_json": map[string]any{"status": "ok-t2"}}},
		},
	}
	v, err := RunFlow(context.Background(), s, plan, stubFlowDeps(firer, obs), tinyCtx())
	require.NoError(t, err)
	assert.True(t, v.Pass, "of: filters each reply observation to its named fire; both pass: %+v", v.StepVerdicts)
}

// ── Race-repro soundness benchmark (BLOCKING gate) ─────────────────

// TestAdjacentFires_FlowMatchesMultiInputLatency is the §10 Critical
// soundness gate from the spec. It compares the wall-clock latency
// of N adjacent fires through the flow path vs the multi-input
// fireTasks path. The flow path may add at most 1ms per inter-fire
// gap; a regression here silently weakens every existing race finding
// (F-006, F-011, F-012, F-013, F-014, F-015) and is a blocking
// merge criterion.
func TestAdjacentFires_FlowMatchesMultiInputLatency(t *testing.T) {
	const n = 100

	tasks := make([]scenario.Task, n)
	for i := range tasks {
		tasks[i] = scenario.Task{
			ID:      fmt.Sprintf("t%d", i),
			Site:    "site-a",
			Verb:    "nats_request",
			Subject: "chat.x",
		}
	}
	barriers := make([]scenario.FlowBarrier, n)
	for i := range barriers {
		barriers[i] = scenario.FlowBarrier{IDs: []string{tasks[i].ID}, Kind: "fire"}
	}
	plan := &scenario.FlowPlan{Barriers: barriers}
	s := &scenario.Scenario{Input: scenario.TaskList(tasks)}

	// --- Flow path ---
	flowFirer := &fakeFlowFirer{}
	flowCtx := tinyCtx()
	tFlowStart := time.Now()
	v, err := RunFlow(context.Background(), s, plan, stubFlowDeps(flowFirer, nil), flowCtx)
	flowElapsed := time.Since(tFlowStart)
	require.NoError(t, err)
	require.True(t, v.Pass)
	require.Len(t, flowFirer.calls, n)

	// --- Multi-input path ---
	miFirer := &fakeFirer{}
	miCtx := tinyCtx()
	tMIStart := time.Now()
	err = fireTasks(context.Background(), tasks, miFirer, &miCtx, nil, nil)
	miElapsed := time.Since(tMIStart)
	require.NoError(t, err)
	require.Len(t, miFirer.calls, n)

	// --- Compare per-gap delta ---
	gaps := n - 1
	if gaps < 1 {
		gaps = 1
	}
	perGapDelta := (flowElapsed - miElapsed) / time.Duration(gaps)
	t.Logf("N=%d adjacent fires: flow=%s multi-input=%s delta=%s per-gap=%s",
		n, flowElapsed, miElapsed, flowElapsed-miElapsed, perGapDelta)

	const tolerance = 1 * time.Millisecond
	if perGapDelta > tolerance {
		t.Fatalf("RACE-REPRO SOUNDNESS GATE VIOLATED: flow path adds %s per inter-fire gap (tolerance %s). Every existing race-finding repro (F-006, F-011, F-012, F-013, F-014, F-015) silently weakens. Find the source — do NOT loosen the tolerance.",
			perGapDelta, tolerance)
	}
}
