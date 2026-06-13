package scenario

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ── Helpers ─────────────────────────────────────────────────────────

// scn builds a minimal Scenario with the given inputs, expecteds, and
// flow string. Sites/seeds left empty — ValidateFlow doesn't read
// them; ValidateSiteFields + ValidateMultiInput run first.
func scn(inputs []Task, expecteds []Expected, flow string) *Scenario {
	s := &Scenario{Name: "x", Input: TaskList(inputs), Expected: expecteds}
	if flow != "" {
		s.Flow = &FlowExpression{Raw: flow}
	}
	return s
}

func mustParse(t *testing.T, s *Scenario) *FlowPlan {
	t.Helper()
	require.NotNil(t, s.Flow)
	p, err := ParseFlow(s.Flow)
	require.NoError(t, err)
	return p
}

// ── Reference checks ───────────────────────────────────────────────

func TestValidateFlow_UnknownRef(t *testing.T) {
	s := scn(
		[]Task{{ID: "a", Site: "site-a", Verb: "nats_request"}},
		nil,
		"a >> b",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "b")
}

func TestValidateFlow_UnreferencedInput(t *testing.T) {
	s := scn(
		[]Task{
			{ID: "a", Site: "site-a", Verb: "nats_request"},
			{ID: "b", Site: "site-a", Verb: "nats_request"},
		},
		nil,
		"a",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "b")
	assert.Contains(t, err.Error(), "not referenced")
}

func TestValidateFlow_UnreferencedExpected(t *testing.T) {
	s := scn(
		[]Task{{ID: "a", Site: "site-a", Verb: "nats_request"}},
		[]Expected{{ID: "obs", Location: "reply", Match: map[string]any{}}},
		"a",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "obs")
}

func TestValidateFlow_InputMissingID(t *testing.T) {
	s := scn(
		[]Task{
			{ID: "a", Site: "site-a", Verb: "nats_request"},
			{ID: "", Site: "site-a", Verb: "nats_request"},
		},
		nil,
		"a",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "input[1]")
	assert.Contains(t, strings.ToLower(err.Error()), "id")
}

func TestValidateFlow_ExpectedMissingID(t *testing.T) {
	s := scn(
		[]Task{{ID: "a", Site: "site-a", Verb: "nats_request"}},
		[]Expected{
			{ID: "obs1", Location: "reply", Match: map[string]any{}},
			{ID: "", Location: "mongo_find", Site: "site-a",
				Args: map[string]any{"collection": "rooms"}, Match: map[string]any{"x": "y"}},
		},
		"a >> obs1",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected[1]")
	assert.Contains(t, strings.ToLower(err.Error()), "id")
}

func TestValidateFlow_DuplicateInFlow(t *testing.T) {
	s := scn(
		[]Task{{ID: "a", Site: "site-a", Verb: "nats_request"}},
		[]Expected{{ID: "obs", Location: "reply", Match: map[string]any{}}},
		"a >> obs >> a",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "a")
	assert.Contains(t, strings.ToLower(err.Error()), "twice")
}

// ── Homogeneity / parallel-fire ─────────────────────────────────────

func TestValidateFlow_MixedTypeGroup_Rejected(t *testing.T) {
	s := scn(
		[]Task{{ID: "a", Site: "site-a", Verb: "nats_request"}},
		[]Expected{{ID: "obs", Location: "reply", Match: map[string]any{}}},
		"[a, obs]",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "homogeneous")
}

func TestValidateFlow_ParallelFire_Rejected(t *testing.T) {
	s := scn(
		[]Task{
			{ID: "a", Site: "site-a", Verb: "nats_request"},
			{ID: "b", Site: "site-a", Verb: "nats_request"},
		},
		nil,
		"[a, b]",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parallel fire")
	assert.Contains(t, err.Error(), "concurrency")
}

func TestValidateFlow_ParallelObserve_OK(t *testing.T) {
	s := scn(
		[]Task{{ID: "a", Site: "site-a", Verb: "nats_request"}},
		[]Expected{
			{ID: "obs1", Location: "reply", Match: map[string]any{}},
			{ID: "obs2", Location: "mongo_find", Site: "site-a",
				Args: map[string]any{"collection": "rooms"}, Match: map[string]any{"x": "y"}},
		},
		"a >> [obs1, obs2]",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.NoError(t, err)
}

// ── Reply scoping (`of:`) ───────────────────────────────────────────

func TestValidateFlow_OfOnNonReply_Rejected(t *testing.T) {
	s := scn(
		[]Task{{ID: "a", Site: "site-a", Verb: "nats_request"}},
		[]Expected{{ID: "obs", Location: "mongo_find", Site: "site-a",
			Args: map[string]any{"collection": "rooms"}, Match: map[string]any{"x": "y"}, Of: "a"}},
		"a >> obs",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "of")
	assert.Contains(t, err.Error(), "reply")
}

func TestValidateFlow_OfUnknownInput_Rejected(t *testing.T) {
	s := scn(
		[]Task{{ID: "a", Site: "site-a", Verb: "nats_request"}},
		[]Expected{{ID: "obs", Location: "reply", Match: map[string]any{}, Of: "ghost"}},
		"a >> obs",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ghost")
}

func TestValidateFlow_OfPositionedAfter_Rejected(t *testing.T) {
	s := scn(
		[]Task{
			{ID: "a", Site: "site-a", Verb: "nats_request"},
			{ID: "b", Site: "site-a", Verb: "nats_request"},
		},
		[]Expected{
			{ID: "obs", Location: "reply", Match: map[string]any{}, Of: "b"},
		},
		"a >> obs >> b",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "of")
}

func TestValidateFlow_AmbiguousReplyScope_Rejected(t *testing.T) {
	// Two fires precede a reply observation without an intervening
	// reply observation — ambiguous; loader requires `of:`.
	s := scn(
		[]Task{
			{ID: "a", Site: "site-a", Verb: "nats_request"},
			{ID: "b", Site: "site-a", Verb: "nats_request"},
		},
		[]Expected{{ID: "obs", Location: "reply", Match: map[string]any{}}},
		"a >> b >> obs",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ambiguous")
	assert.Contains(t, err.Error(), "a")
	assert.Contains(t, err.Error(), "b")
}

func TestValidateFlow_PositionDefaultUnambiguous_OK(t *testing.T) {
	s := scn(
		[]Task{{ID: "a", Site: "site-a", Verb: "nats_request"}},
		[]Expected{{ID: "obs", Location: "reply", Match: map[string]any{}}},
		"a >> obs",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.NoError(t, err)
}

func TestValidateFlow_OfDisambiguatesTwoFires_OK(t *testing.T) {
	s := scn(
		[]Task{
			{ID: "a", Site: "site-a", Verb: "nats_request"},
			{ID: "b", Site: "site-a", Verb: "nats_request"},
		},
		[]Expected{
			{ID: "reply_a", Location: "reply", Match: map[string]any{}, Of: "a"},
			{ID: "reply_b", Location: "reply", Match: map[string]any{}, Of: "b"},
		},
		"a >> b >> [reply_a, reply_b]",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.NoError(t, err)
}

func TestValidateFlow_MatchTaskInFlow_Rejected(t *testing.T) {
	s := scn(
		[]Task{{ID: "a", Site: "site-a", Verb: "nats_request"}},
		[]Expected{{ID: "obs", Location: "reply",
			Match: map[string]any{"task": "a", "body_json": map[string]any{}}}},
		"a >> obs",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "match.task")
	assert.Contains(t, err.Error(), "of")
}

// ── Forward-ref / same-group substitution ───────────────────────────

func TestValidateFlow_ForwardRefInPayload_Rejected(t *testing.T) {
	s := scn(
		[]Task{
			{ID: "a", Site: "site-a", Verb: "nats_request",
				Payload: map[string]any{"x": "${b.reply.body_json.y}"}},
			{ID: "b", Site: "site-a", Verb: "nats_request"},
		},
		nil,
		"a >> b",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "after")
}

func TestValidateFlow_SameGroupSiblingRef_Rejected(t *testing.T) {
	s := scn(
		[]Task{{ID: "a", Site: "site-a", Verb: "nats_request"}},
		[]Expected{
			{ID: "obs_b", Location: "mongo_find", Site: "site-a",
				Args: map[string]any{"collection": "rooms"},
				Match: map[string]any{"x": "y"}},
			{ID: "obs_c", Location: "mongo_find", Site: "site-a",
				Args: map[string]any{"collection": "rooms",
					"filter": map[string]any{"y": "${obs_b.body_json.z}"}},
				Match: map[string]any{"x": "y"}},
		},
		"a >> [obs_b, obs_c]",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "obs_b")
	assert.Contains(t, err.Error(), "same parallel group")
}

func TestValidateFlow_BackwardRef_OK(t *testing.T) {
	s := scn(
		[]Task{
			{ID: "a", Site: "site-a", Verb: "nats_request"},
			{ID: "b", Site: "site-a", Verb: "nats_request",
				Subject: "chat.${a.reply.body_json.roomId}.x"},
		},
		nil,
		"a >> b",
	)
	_, err := ValidateFlow(s, mustParse(t, s))
	require.NoError(t, err)
}

// ── Negative placement (warning, not error) ─────────────────────────

func TestValidateFlow_NegativeInFinalBarrier_NoWarning(t *testing.T) {
	s := scn(
		[]Task{{ID: "a", Site: "site-a", Verb: "nats_request"}},
		[]Expected{
			{ID: "obs1", Location: "reply", Match: map[string]any{}},
			{ID: "no_err", Location: "logs_tail", Site: "site-a",
				Args: map[string]any{"service": "room-worker"},
				Match: map[string]any{"level": "error"}, Not: true},
		},
		"a >> obs1 >> no_err",
	)
	warnings, err := ValidateFlow(s, mustParse(t, s))
	require.NoError(t, err)
	assert.Empty(t, warnings, "not:true in final barrier emits no warning")
}

func TestValidateFlow_NegativeMidFlow_Warning(t *testing.T) {
	s := scn(
		[]Task{
			{ID: "a", Site: "site-a", Verb: "nats_request"},
			{ID: "b", Site: "site-a", Verb: "nats_request"},
		},
		[]Expected{
			{ID: "no_err", Location: "logs_tail", Site: "site-a",
				Args: map[string]any{"service": "room-worker"},
				Match: map[string]any{"level": "error"}, Not: true},
			{ID: "obs_b", Location: "reply", Of: "b", Match: map[string]any{}},
		},
		"a >> no_err >> b >> obs_b",
	)
	warnings, err := ValidateFlow(s, mustParse(t, s))
	require.NoError(t, err, "negative placement is a WARNING, not an error")
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "no_err")
	assert.Contains(t, warnings[0], "final barrier")
}
