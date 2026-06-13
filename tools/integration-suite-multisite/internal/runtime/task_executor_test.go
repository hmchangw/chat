package runtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/verbs"
)

// fakeFirer records every Fire call and, per task id, can either write
// a scripted reply into subCtx.Replies (simulating the dispatcher) or
// return an error (simulating an unresolved substitution).
type fakeFirer struct {
	calls   []*InputSpec
	replies map[string]map[string]any // taskID → body_json to store
	errOn   map[string]error          // taskID → error to return
}

func (f *fakeFirer) Fire(_ context.Context, in *InputSpec, subCtx *Context, _ verbs.Credential, _ string) error {
	f.calls = append(f.calls, in)
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

func TestFireTasks_SinglePassThrough(t *testing.T) {
	ff := &fakeFirer{}
	tasks := []scenario.Task{
		{ID: "t1", Site: "site-a", Verb: "nats_request", Subject: "chat.x"},
	}
	subCtx := Context{}
	err := fireTasks(context.Background(), tasks, ff, &subCtx, nil, nil)
	require.NoError(t, err)
	require.Len(t, ff.calls, 1)
	assert.Equal(t, "t1", ff.calls[0].TaskID)
	assert.Equal(t, "chat.x", ff.calls[0].Subject)
}

func TestFireTasks_OrderPreserved(t *testing.T) {
	ff := &fakeFirer{}
	tasks := []scenario.Task{
		{ID: "create", Site: "site-a", Verb: "nats_request"},
		{ID: "join", Site: "site-a", Verb: "nats_request"},
		{ID: "send", Site: "site-a", Verb: "nats_request"},
	}
	subCtx := Context{}
	require.NoError(t, fireTasks(context.Background(), tasks, ff, &subCtx, nil, nil))
	require.Len(t, ff.calls, 3)
	assert.Equal(t, "create", ff.calls[0].TaskID)
	assert.Equal(t, "join", ff.calls[1].TaskID)
	assert.Equal(t, "send", ff.calls[2].TaskID)
}

func TestFireTasks_ReplyVisibleToNextTask(t *testing.T) {
	// The firer stores t1's reply; the executor passes the same subCtx
	// to t2's Fire, so t2 sees t1's reply in the context.
	ff := &fakeFirer{replies: map[string]map[string]any{
		"t1": {"roomId": "r-1"},
	}}
	tasks := []scenario.Task{
		{ID: "t1", Site: "site-a", Verb: "nats_request"},
		{ID: "t2", Site: "site-a", Verb: "nats_request"},
	}
	subCtx := Context{}
	require.NoError(t, fireTasks(context.Background(), tasks, ff, &subCtx, nil, nil))
	require.Contains(t, subCtx.Replies, "t1")
	assert.Equal(t, "r-1", subCtx.Replies["t1"].BodyJSON["roomId"])
}

func TestFireTasks_FireAnyway_FailedReplyDoesNotHalt(t *testing.T) {
	// t1 returns a (rejected) reply but no error — t2 must still fire.
	ff := &fakeFirer{replies: map[string]map[string]any{
		"t1": {"status": "rejected"},
	}}
	tasks := []scenario.Task{
		{ID: "t1", Site: "site-a", Verb: "nats_request"},
		{ID: "t2", Site: "site-a", Verb: "nats_request"},
	}
	subCtx := Context{}
	require.NoError(t, fireTasks(context.Background(), tasks, ff, &subCtx, nil, nil))
	require.Len(t, ff.calls, 2, "rejected reply must not halt downstream tasks")
}

func TestFireTasks_UnresolvedSubstitutionHalts(t *testing.T) {
	// Fire returns an error for t2 (simulating an unresolved ${...});
	// the loop halts and t3 never fires. The error names the task.
	ff := &fakeFirer{errOn: map[string]error{
		"t2": fmt.Errorf("dispatcher: substitute payload: unknown path %q", "${t1.reply.body_json.roomId}"),
	}}
	tasks := []scenario.Task{
		{ID: "t1", Site: "site-a", Verb: "nats_request"},
		{ID: "t2", Site: "site-a", Verb: "nats_request"},
		{ID: "t3", Site: "site-a", Verb: "nats_request"},
	}
	subCtx := Context{}
	err := fireTasks(context.Background(), tasks, ff, &subCtx, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "t2")
	assert.Contains(t, err.Error(), "${t1.reply.body_json.roomId}")
	assert.Len(t, ff.calls, 2, "loop halts at t2; t3 never fires")
}
