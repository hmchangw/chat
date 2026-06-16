package runtime

import (
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/matchers"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
)

// TestMatchShape_SingleEventMatchingExpectedMatches and
// TestMatchShape_NegatedFailureMessageNamesMatchingEvent (below)
// together form the regression-guard for the §2.9 corollary in
// docs/integration-suite-plan-ahead.md — the "matcher must
// correctly identify a present event under not:true" property the
// retired `logs-tail-regression-guard-*` YAML scenario was
// protecting end-to-end. The YAML was deleted (commit f218bd7,
// tester's cycle commit) because asserting matcher behavior via
// a perpetually-red scenario was the wrong surface; these Go
// tests carry the property forward. Do NOT re-introduce the YAML
// without first deleting these tests.
func TestMatchShape_SingleEventMatchingExpectedMatches(t *testing.T) {
	reg := matchers.NewRegistry()
	m := MatchShape(map[string]any{"status": "accepted"}, reg)
	events := []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "accepted", "roomId": "r1"}},
	}
	ok, err := m.Match(events)
	require.NoError(t, err)
	assert.True(t, ok, "subset shape match must succeed — this true return is what Gomega's ShouldNot flips into a failure under not:true assertions")
}

func TestMatchShape_ZeroEventsFailsWithPolledZero(t *testing.T) {
	reg := matchers.NewRegistry()
	m := MatchShape(map[string]any{"status": "accepted"}, reg)
	ok, err := m.Match([]readers.Event{})
	require.NoError(t, err)
	require.False(t, ok)
	msg := m.FailureMessage([]readers.Event{})
	assert.Contains(t, msg, "events polled:     0")
}

func TestMatchShape_MultipleEventsOneMatches(t *testing.T) {
	reg := matchers.NewRegistry()
	m := MatchShape(map[string]any{"status": "accepted"}, reg)
	events := []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "rejected"}},
		{Location: "reply", Payload: map[string]any{"status": "accepted", "roomId": "r1"}},
		{Location: "reply", Payload: map[string]any{"foo": "bar"}},
	}
	ok, err := m.Match(events)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestMatchShape_MultipleEventsNoneMatchFailureNamesClosest(t *testing.T) {
	reg := matchers.NewRegistry()
	m := MatchShape(map[string]any{"status": "accepted"}, reg)
	events := []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "rejected"}},
		{Location: "reply", Payload: map[string]any{"foo": "bar"}},
	}
	ok, err := m.Match(events)
	require.NoError(t, err)
	require.False(t, ok)
	msg := m.FailureMessage(events)
	assert.Contains(t, msg, "events polled:     2")
	// Must surface at least one of the mismatch reasons so the operator
	// can triage which assertion failed.
	assert.Contains(t, msg, "status")
	// And must surface the actual reply payload verbatim, separate from
	// the tool's framing.
	assert.Contains(t, msg, "reply from system:")
}

func TestMatchShape_NilRegistryFallsBackToBuiltins(t *testing.T) {
	// Passing nil reg should default to the built-in matchers.NewRegistry
	// so callers can use MatchShape inline without wiring a registry.
	m := MatchShape(map[string]any{"status": "accepted"}, nil)
	events := []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "accepted"}},
	}
	ok, err := m.Match(events)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestMatchShape_ActualMustBeEventSlice(t *testing.T) {
	m := MatchShape(map[string]any{"x": 1}, nil)
	_, err := m.Match("not an event slice")
	require.Error(t, err)
}

func TestMatchShape_NegatedFailureMessageNamesMatchingEvent(t *testing.T) {
	// For Consistently().ShouldNot(...) — when an event DOES match,
	// the negated message must point at the matching payload so the
	// operator sees why the "must not happen" assertion fired.
	m := MatchShape(map[string]any{"status": "accepted"}, nil)
	events := []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "accepted", "roomId": "r1"}},
	}
	// Drive Match first so the matcher captures matchedIdx for the
	// negated message — mirrors how Gomega itself sequences Match →
	// NegatedFailureMessage on a Consistently().ShouldNot(...) fail.
	_, _ = m.Match(events)
	msg := m.NegatedFailureMessage(events)
	assert.Contains(t, msg, "expected no event to match")
	assert.Contains(t, msg, "r1", "must surface the matching payload for triage")
}

// outbox_payload tests — the matcher directive for cross-site
// payload-content assertions (cycle report feature request #2).
// Production wraps every cross-site event in pkg/model.OutboxEvent
// where the inner payload is `[]byte` (base64 on the JSON wire),
// so a normal subset-match can only reach the envelope (type,
// siteId, destSiteId), never the inner fields the test cares about.

// outboxEventPayload constructs an event payload that mimics what
// jetstream_consume / nats_subscribe build for a real OUTBOX event:
//
//	{
//	  body_json: { type: ..., siteId: ..., destSiteId: ..., payload: <b64> }
//	  body_raw:  ...
//	}
//
// Used by every test below so the fixture stays in one place.
func outboxEventPayload(envelope map[string]any, innerJSON string) map[string]any {
	encoded := base64.StdEncoding.EncodeToString([]byte(innerJSON))
	bodyJSON := map[string]any{}
	for k, v := range envelope {
		bodyJSON[k] = v
	}
	bodyJSON["payload"] = encoded
	return map[string]any{"body_json": bodyJSON}
}

// TestMatchShape_OutboxPayload_DecodedSubsetMatches covers the happy
// path: envelope matches AND the decoded inner subset matches.
func TestMatchShape_OutboxPayload_DecodedSubsetMatches(t *testing.T) {
	envelope := map[string]any{"type": "room_renamed", "siteId": "site-a", "destSiteId": "site-b"}
	inner := `{"roomId":"r-shared","newName":"SharedChannelRenamed","by":"alice"}`
	events := []readers.Event{
		{Location: "jetstream_consume", Payload: outboxEventPayload(envelope, inner)},
	}
	m := MatchShape(map[string]any{
		"body_json": map[string]any{
			"type":       "room_renamed",
			"destSiteId": "site-b",
		},
		outboxPayloadKey: map[string]any{
			"roomId":  "r-shared",
			"newName": "SharedChannelRenamed",
		},
	}, nil)

	ok, err := m.Match(events)
	require.NoError(t, err)
	assert.True(t, ok, "envelope subset + decoded inner subset both match")
}

// TestMatchShape_OutboxPayload_EnvelopeMatchesButInnerMismatches —
// the body_json envelope is right but the decoded inner content
// doesn't subset-match. The matcher must REJECT (not silently
// pass on the envelope alone).
func TestMatchShape_OutboxPayload_EnvelopeMatchesButInnerMismatches(t *testing.T) {
	envelope := map[string]any{"type": "room_renamed", "siteId": "site-a"}
	inner := `{"roomId":"r-something-else","newName":"NotWhatWasExpected"}`
	events := []readers.Event{
		{Location: "jetstream_consume", Payload: outboxEventPayload(envelope, inner)},
	}
	m := MatchShape(map[string]any{
		"body_json": map[string]any{"type": "room_renamed"},
		outboxPayloadKey: map[string]any{
			"roomId":  "r-shared", // expected DIFFERENT from actual
			"newName": "SharedChannelRenamed",
		},
	}, nil)

	ok, err := m.Match(events)
	require.NoError(t, err)
	require.False(t, ok, "inner mismatch must reject — envelope alone is not enough")

	msg := m.FailureMessage(events)
	assert.Contains(t, msg, outboxPayloadKey)
	assert.Contains(t, msg, "decoded subset mismatch")
}

// TestMatchShape_OutboxPayload_EnvelopeMismatchSkipsInner — when
// the envelope doesn't subset-match, the matcher rejects WITHOUT
// attempting to decode the inner payload. Reason should reflect the
// envelope-level mismatch, not the inner.
func TestMatchShape_OutboxPayload_EnvelopeMismatchSkipsInner(t *testing.T) {
	envelope := map[string]any{"type": "room_renamed", "siteId": "site-a"}
	inner := `{"roomId":"r-shared"}`
	events := []readers.Event{
		{Location: "jetstream_consume", Payload: outboxEventPayload(envelope, inner)},
	}
	m := MatchShape(map[string]any{
		"body_json":      map[string]any{"type": "member_added"}, // envelope mismatch
		outboxPayloadKey: map[string]any{"roomId": "r-shared"},   // would have matched
	}, nil)

	ok, err := m.Match(events)
	require.NoError(t, err)
	require.False(t, ok)

	msg := m.FailureMessage(events)
	assert.NotContains(t, msg, "decoded subset mismatch",
		"envelope-level mismatch must short-circuit before inner decode")
}

// TestMatchShape_OutboxPayload_MissingBodyJSON — event payload has
// no body_json at all (perhaps a poller-shape regression). Matcher
// rejects with a precise reason naming the missing field.
func TestMatchShape_OutboxPayload_MissingBodyJSON(t *testing.T) {
	events := []readers.Event{
		{Location: "jetstream_consume", Payload: map[string]any{"body_raw": "not json"}},
	}
	m := MatchShape(map[string]any{
		outboxPayloadKey: map[string]any{"roomId": "r-shared"},
	}, nil)

	ok, err := m.Match(events)
	require.NoError(t, err)
	require.False(t, ok)
	assert.Contains(t, m.FailureMessage(events), "body_json is missing or not a map")
}

// TestMatchShape_OutboxPayload_MissingPayloadField — body_json
// exists but lacks the `payload` field. Producer skipped its encode
// step.
func TestMatchShape_OutboxPayload_MissingPayloadField(t *testing.T) {
	events := []readers.Event{
		{Location: "jetstream_consume", Payload: map[string]any{
			"body_json": map[string]any{"type": "room_renamed"},
		}},
	}
	m := MatchShape(map[string]any{
		outboxPayloadKey: map[string]any{"roomId": "r-shared"},
	}, nil)

	ok, err := m.Match(events)
	require.NoError(t, err)
	require.False(t, ok)
	assert.Contains(t, m.FailureMessage(events), "body_json.payload is missing or not a string")
}

// TestMatchShape_OutboxPayload_InvalidBase64 — body_json.payload is
// a string but not valid base64. Producer wire-format regression.
func TestMatchShape_OutboxPayload_InvalidBase64(t *testing.T) {
	events := []readers.Event{
		{Location: "jetstream_consume", Payload: map[string]any{
			"body_json": map[string]any{"payload": "!!! not base64 !!!"},
		}},
	}
	m := MatchShape(map[string]any{
		outboxPayloadKey: map[string]any{"roomId": "r-shared"},
	}, nil)

	ok, err := m.Match(events)
	require.NoError(t, err)
	require.False(t, ok)
	assert.Contains(t, m.FailureMessage(events), "base64 decode of body_json.payload failed")
}

// TestMatchShape_OutboxPayload_DecodedNotJSON — base64 decodes but
// the bytes aren't valid JSON. Schema shape regression.
func TestMatchShape_OutboxPayload_DecodedNotJSON(t *testing.T) {
	notJSON := base64.StdEncoding.EncodeToString([]byte("this is not json"))
	events := []readers.Event{
		{Location: "jetstream_consume", Payload: map[string]any{
			"body_json": map[string]any{"payload": notJSON},
		}},
	}
	m := MatchShape(map[string]any{
		outboxPayloadKey: map[string]any{"roomId": "r-shared"},
	}, nil)

	ok, err := m.Match(events)
	require.NoError(t, err)
	require.False(t, ok)
	assert.Contains(t, m.FailureMessage(events), "JSON parse of decoded payload failed")
}

// TestMatchShape_OutboxPayload_NonMapDirectiveValue — author wrote
// the directive with a non-map value (e.g. a string). Programmer/
// author error; surface immediately as a hard matcher error rather
// than silently mismatching every event.
func TestMatchShape_OutboxPayload_NonMapDirectiveValue(t *testing.T) {
	events := []readers.Event{
		{Location: "jetstream_consume", Payload: map[string]any{}},
	}
	m := MatchShape(map[string]any{
		outboxPayloadKey: "this should be a map",
	}, nil)

	_, err := m.Match(events)
	require.Error(t, err)
	assert.Contains(t, err.Error(), outboxPayloadKey)
	assert.Contains(t, err.Error(), "must be a map")
}

// TestMatchShape_OutboxPayload_OnlyDirectiveNoEnvelope — author
// uses only outbox_payload (no body_json envelope assertions). The
// matcher must still decode + subset-match.
func TestMatchShape_OutboxPayload_OnlyDirectiveNoEnvelope(t *testing.T) {
	inner := `{"roomId":"r-shared","newName":"X"}`
	events := []readers.Event{
		{Location: "jetstream_consume", Payload: outboxEventPayload(map[string]any{"type": "room_renamed"}, inner)},
	}
	m := MatchShape(map[string]any{
		outboxPayloadKey: map[string]any{"roomId": "r-shared"},
	}, nil)

	ok, err := m.Match(events)
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestMatchShape_OutboxPayload_FirstEventMissesSecondHits — the
// matcher must scan ALL events, not just stop at the first
// envelope-mismatch. Mirrors how scenarios work in practice:
// multiple events may land on a stream and only one is the target.
func TestMatchShape_OutboxPayload_FirstEventMissesSecondHits(t *testing.T) {
	events := []readers.Event{
		{Location: "jetstream_consume", Payload: outboxEventPayload(
			map[string]any{"type": "member_added"},
			`{"roomId":"r-other"}`,
		)},
		{Location: "jetstream_consume", Payload: outboxEventPayload(
			map[string]any{"type": "room_renamed"},
			`{"roomId":"r-shared","newName":"Renamed"}`,
		)},
	}
	m := MatchShape(map[string]any{
		"body_json": map[string]any{"type": "room_renamed"},
		outboxPayloadKey: map[string]any{
			"roomId":  "r-shared",
			"newName": "Renamed",
		},
	}, nil)

	ok, err := m.Match(events)
	require.NoError(t, err)
	assert.True(t, ok, "matcher must scan past the first envelope mismatch to find the second event")
}

// --- match.task: selector (multi-input, reply-only) ---

// taskScopeEvents: two reply events from different tasks. create's
// reply is accepted; join's is rejected. Without a task filter, the
// shape {body_json:{status:accepted}} would match create's event
// regardless of which task an assertion is about — the filter is what
// scopes the assertion to one task's reply.
func taskScopeEvents() []readers.Event {
	return []readers.Event{
		{Location: "reply", Task: "create", Payload: map[string]any{"body_json": map[string]any{"status": "accepted", "roomId": "r1"}}},
		{Location: "reply", Task: "join", Payload: map[string]any{"body_json": map[string]any{"status": "rejected"}}},
	}
}

func TestMatchShape_TaskSelector_FilterPass(t *testing.T) {
	reg := matchers.NewRegistry()
	m := MatchShape(map[string]any{"task": "create", "body_json": map[string]any{"status": "accepted"}}, reg)
	ok, err := m.Match(taskScopeEvents())
	require.NoError(t, err)
	assert.True(t, ok, "create's reply is accepted and matches the create-scoped shape")
}

func TestMatchShape_TaskSelector_FilterMismatch(t *testing.T) {
	reg := matchers.NewRegistry()
	// Scoped to join, whose reply is rejected — must NOT match, even
	// though create's accepted reply would match the same shape
	// unscoped. This proves the filter excludes other tasks' events.
	m := MatchShape(map[string]any{"task": "join", "body_json": map[string]any{"status": "accepted"}}, reg)
	ok, err := m.Match(taskScopeEvents())
	require.NoError(t, err)
	assert.False(t, ok, "join's reply is rejected; the create reply must not leak through the join-scoped filter")
}

func TestMatchShape_UnscopedReply_StillMatches(t *testing.T) {
	reg := matchers.NewRegistry()
	// No task: key — accepts any reply (backward-compatible).
	m := MatchShape(map[string]any{"body_json": map[string]any{"status": "accepted"}}, reg)
	ok, err := m.Match(taskScopeEvents())
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestMatchShape_TaskInsideOutboxPayload_Rejected(t *testing.T) {
	reg := matchers.NewRegistry()
	m := MatchShape(map[string]any{
		"outbox_payload": map[string]any{"task": "t1", "roomId": "r-shared"},
	}, reg)
	_, err := m.Match([]readers.Event{{Location: "reply", Payload: map[string]any{}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "task")
	assert.Contains(t, err.Error(), "outbox_payload")
}

// --- ReplyScope: flow-driven reply scoping (Task 6) ---

func TestMatchShape_ReplyScope_FilterPass(t *testing.T) {
	reg := matchers.NewRegistry()
	m := MatchShapeScoped(map[string]any{"body_json": map[string]any{"status": "accepted"}}, reg,
		ReplyScope{InputID: "create"})
	ok, err := m.Match(taskScopeEvents())
	require.NoError(t, err)
	assert.True(t, ok, "scope=create filters to the create-tagged event; its accepted reply matches")
}

func TestMatchShape_ReplyScope_FilterMismatch(t *testing.T) {
	reg := matchers.NewRegistry()
	m := MatchShapeScoped(map[string]any{"body_json": map[string]any{"status": "accepted"}}, reg,
		ReplyScope{InputID: "join"})
	ok, err := m.Match(taskScopeEvents())
	require.NoError(t, err)
	assert.False(t, ok, "scope=join filters to join's reply (rejected); the accepted create-reply must not leak")
}

func TestMatchShape_ReplyScope_Empty_AcceptsAny(t *testing.T) {
	reg := matchers.NewRegistry()
	m := MatchShapeScoped(map[string]any{"body_json": map[string]any{"status": "accepted"}}, reg,
		ReplyScope{}) // empty scope = no filter
	ok, err := m.Match(taskScopeEvents())
	require.NoError(t, err)
	assert.True(t, ok, "empty ReplyScope behaves like today's MatchShape — accepts any matching event")
}

func TestMatchShape_LegacyTaskDirective_StillWorks(t *testing.T) {
	// Regression: the legacy match.task: directive on the legacy path
	// continues to work — same behavior as before Task 6.
	reg := matchers.NewRegistry()
	m := MatchShape(map[string]any{"task": "create",
		"body_json": map[string]any{"status": "accepted"}}, reg)
	ok, err := m.Match(taskScopeEvents())
	require.NoError(t, err)
	assert.True(t, ok)
}
