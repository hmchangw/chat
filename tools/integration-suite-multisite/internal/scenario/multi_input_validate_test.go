package scenario

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ValidateMultiInput (spec §4) checks:
//   - task ids unique within a scenario
//   - list-shape tasks have an id
//   - ${<id>.reply.*} references name a declared task that fires
//     EARLIER in declaration order
//   - expected[].match.task is only valid on location: reply and must
//     name a declared task

func TestValidateMultiInput_DuplicateTaskID(t *testing.T) {
	s := &Scenario{
		Name: "x",
		Input: TaskList{
			{ID: "t1", Site: "site-a", Verb: "nats_request"},
			{ID: "t1", Site: "site-a", Verb: "nats_request"},
		},
	}
	err := ValidateMultiInput(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate task id")
	assert.Contains(t, err.Error(), "t1")
}

func TestValidateMultiInput_ListShapeMissingID(t *testing.T) {
	// A two-task list where one element has no id.
	s := &Scenario{
		Name: "x",
		Input: TaskList{
			{ID: "t1", Site: "site-a", Verb: "nats_request"},
			{ID: "", Site: "site-a", Verb: "nats_request"},
		},
	}
	err := ValidateMultiInput(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "input[1]")
	assert.Contains(t, strings.ToLower(err.Error()), "id")
}

func TestValidateMultiInput_LegacySingleTaskNoIDRequired(t *testing.T) {
	// The legacy single-fire shape decodes to a one-task list with an
	// empty id — that is allowed (it can't reference itself).
	s := &Scenario{
		Name: "x",
		Input: TaskList{
			{ID: "", Site: "site-a", Verb: "nats_request", Subject: "chat.x"},
		},
	}
	require.NoError(t, ValidateMultiInput(s))
}

func TestValidateMultiInput_UnknownReplyRef(t *testing.T) {
	s := &Scenario{
		Name: "x",
		Input: TaskList{
			{ID: "create", Site: "site-a", Verb: "nats_request", Subject: "chat.create"},
			{ID: "join", Site: "site-a", Verb: "nats_request",
				Payload: map[string]any{"roomId": "${t99.reply.body_json.roomId}"}},
		},
	}
	err := ValidateMultiInput(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "join")
	assert.Contains(t, err.Error(), "t99")
}

func TestValidateMultiInput_ForwardReplyRef(t *testing.T) {
	s := &Scenario{
		Name: "x",
		Input: TaskList{
			{ID: "create", Site: "site-a", Verb: "nats_request",
				Subject: "chat.${join.reply.body_json.x}"},
			{ID: "join", Site: "site-a", Verb: "nats_request"},
		},
	}
	err := ValidateMultiInput(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fires after")
	assert.Contains(t, err.Error(), "create")
	assert.Contains(t, err.Error(), "join")
}

func TestValidateMultiInput_ReplyRefInCredential(t *testing.T) {
	// A reference in the credential field is detected the same way.
	s := &Scenario{
		Name: "x",
		Input: TaskList{
			{ID: "t1", Site: "site-a", Verb: "nats_request", Subject: "chat.x"},
			{ID: "t2", Site: "site-a", Verb: "nats_request", Subject: "chat.y",
				Credential: "${t99.reply.body_json.cred}"},
		},
	}
	err := ValidateMultiInput(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "t99")
}

func TestValidateMultiInput_ReplyRefInNestedPayload(t *testing.T) {
	// A valid backward reference nested deep inside the payload must be
	// recognised (no false "unknown task" error).
	s := &Scenario{
		Name: "x",
		Input: TaskList{
			{ID: "create", Site: "site-a", Verb: "nats_request", Subject: "chat.create"},
			{ID: "send", Site: "site-a", Verb: "nats_request", Subject: "chat.send",
				Payload: map[string]any{
					"outer": map[string]any{
						"inner": []any{"${create.reply.body_json.roomId}"},
					},
				}},
		},
	}
	require.NoError(t, ValidateMultiInput(s))
}

func TestValidateMultiInput_MatchTaskOnPollerRejected(t *testing.T) {
	s := &Scenario{
		Name:  "x",
		Input: TaskList{{ID: "t1", Site: "site-a", Verb: "nats_request"}},
		Expected: []Expected{
			{Location: "mongo_find", Match: map[string]any{"task": "t1", "name": "Eng"}},
		},
	}
	err := ValidateMultiInput(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "match.task")
	assert.Contains(t, err.Error(), "reply")
}

func TestValidateMultiInput_MatchTaskUnknownID(t *testing.T) {
	s := &Scenario{
		Name:  "x",
		Input: TaskList{{ID: "t1", Site: "site-a", Verb: "nats_request"}},
		Expected: []Expected{
			{Location: "reply", Match: map[string]any{"task": "t99", "body_json": map[string]any{}}},
		},
	}
	err := ValidateMultiInput(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "t99")
}

func TestValidateMultiInput_MatchTaskOnReplyKnownID(t *testing.T) {
	s := &Scenario{
		Name:  "x",
		Input: TaskList{{ID: "t1", Site: "site-a", Verb: "nats_request"}},
		Expected: []Expected{
			{Location: "reply", Match: map[string]any{"task": "t1", "body_json": map[string]any{}}},
		},
	}
	require.NoError(t, ValidateMultiInput(s))
}

func TestValidateMultiInput_UnscopedReplyMatch(t *testing.T) {
	// A reply assertion with no task: key is allowed (backward-compat).
	s := &Scenario{
		Name:  "x",
		Input: TaskList{{ID: "", Site: "site-a", Verb: "nats_request"}},
		Expected: []Expected{
			{Location: "reply", Match: map[string]any{"body_json": map[string]any{"status": "accepted"}}},
		},
	}
	require.NoError(t, ValidateMultiInput(s))
}
