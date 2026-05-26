package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/matchers"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/readers"
	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/scenario"
)

func TestClassify_AllExpectedMatched_NoExtra_Passes(t *testing.T) {
	now := time.Now()
	traceID := "trace1"
	s := &scenario.Scenario{
		Sequence: []scenario.Step{
			{Service: "room-service", Reads: []scenario.Read{
				{Location: "reply", Matcher: "matches_shape", Expected: map[string]any{"id": "r1"}},
			}},
		},
	}
	events := []readers.Event{
		{Location: "reply", Timestamp: now, Traceparent: "00-" + traceID + "-x-01", Payload: []byte(`{"id":"r1"}`)},
	}

	v := Classify(s, events, traceID, matchers.NewRegistry(), nil, nil, time.Time{})

	assert.Equal(t, "pass", v.Outcome)
	assert.Empty(t, v.MissingPositives)
	assert.Empty(t, v.UnexpectedCascades)
	assert.Empty(t, v.Anomalies)
}

func TestClassify_MissingPositive_Fails(t *testing.T) {
	s := &scenario.Scenario{
		Sequence: []scenario.Step{
			{Service: "room-service", Reads: []scenario.Read{
				{Location: "reply", Matcher: "matches_shape", Expected: map[string]any{"id": "r1"}},
			}},
		},
	}
	v := Classify(s, nil, "trace1", matchers.NewRegistry(), nil, nil, time.Time{})
	assert.Equal(t, "fail", v.Outcome)
	assert.Len(t, v.MissingPositives, 1)
}

// TestClassify_PerStepDuration confirms each step's Duration is the
// delta from the previous step's last matched event (T_open for the
// first step), so authors can see which service in a pipeline is the
// bottleneck.
func TestClassify_PerStepDuration(t *testing.T) {
	tOpen := time.Unix(1_700_000_000, 0)
	traceID := "tracebottleneck"
	tp := "00-" + traceID + "-aaaaaaaaaaaaaaaa-01"
	s := &scenario.Scenario{
		Sequence: []scenario.Step{
			{Service: "room-service", Reads: []scenario.Read{
				{Location: "reply", Matcher: "matches_shape", Expected: map[string]any{"k": "a"}},
			}},
			{Service: "room-worker", Reads: []scenario.Read{
				{Location: "mongo.rooms", Matcher: "matches_shape", Expected: map[string]any{"k": "b"}},
			}},
		},
	}
	events := []readers.Event{
		{Location: "reply", Timestamp: tOpen.Add(50 * time.Millisecond), Traceparent: tp, Payload: []byte(`{"k":"a"}`)},
		{Location: "mongo.rooms", Timestamp: tOpen.Add(300 * time.Millisecond), Traceparent: tp, Payload: []byte(`{"k":"b"}`)},
	}
	v := Classify(s, events, traceID, matchers.NewRegistry(), nil, nil, tOpen)
	assert.Equal(t, "pass", v.Outcome)
	if assert.Len(t, v.Steps, 2) {
		assert.Equal(t, 50*time.Millisecond, v.Steps[0].Duration, "step 1 = first match - tOpen")
		assert.Equal(t, 250*time.Millisecond, v.Steps[1].Duration, "step 2 = its match - step 1's last match")
	}
}

func TestClassify_UnexpectedCascade_Fails(t *testing.T) {
	now := time.Now()
	traceID := "trace1"
	s := &scenario.Scenario{
		Sequence: []scenario.Step{
			{Service: "room-service", Reads: []scenario.Read{
				{Location: "reply", Matcher: "matches_shape", Expected: map[string]any{"id": "r1"}},
			}},
		},
	}
	events := []readers.Event{
		{Location: "reply", Timestamp: now, Traceparent: "00-" + traceID + "-x-01", Payload: []byte(`{"id":"r1"}`)},
		{Location: "mongo.subscriptions", Timestamp: now, Traceparent: "00-" + traceID + "-y-01", OwnerSvc: "room-service"},
	}
	v := Classify(s, events, traceID, matchers.NewRegistry(), nil, nil, time.Time{})
	assert.Equal(t, "fail", v.Outcome)
	assert.Len(t, v.UnexpectedCascades, 1)
	assert.Equal(t, "mongo.subscriptions", v.UnexpectedCascades[0].Location)
}
