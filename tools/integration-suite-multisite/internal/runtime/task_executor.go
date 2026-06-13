package runtime

import (
	"context"
	"fmt"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/seedeffect"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/verbs"
)

// taskFirer is the subset of *Dispatcher the task executor depends on.
// Defined here (consumer side) so the executor can be unit-tested with
// a fake that scripts replies and errors without a real NATS backend.
type taskFirer interface {
	Fire(ctx context.Context, in *InputSpec, subCtx *Context, cred verbs.Credential, traceparent string) error
}

// fireTasks runs a scenario's tasks in declaration order through the
// firer, threading a single substitution context so each task's reply
// (captured by Fire into subCtx.Replies) is visible to later tasks'
// ${<id>.reply.*} substitutions.
//
// Fire-anyway (spec §5.2): a task whose reply is rejected/error does
// NOT halt the chain — Fire returns nil and downstream tasks still
// fire; assertions remain the source of truth. Only a Fire error —
// which is an unresolved ${...} substitution or a dispatch failure —
// halts the chain, wrapped with the offending task's label.
func fireTasks(
	ctx context.Context,
	tasks []scenario.Task,
	firer taskFirer,
	subCtx *Context,
	users map[string]*seedeffect.SeedUser,
	services map[string]Credential,
) error {
	for i := range tasks {
		t := &tasks[i]
		cred := resolveCredential(t.Credential, users, services)
		in := &InputSpec{
			Site:    t.Site,
			Verb:    t.Verb,
			Subject: t.Subject,
			Payload: t.Payload,
			TaskID:  t.ID,
		}
		if err := firer.Fire(ctx, in, subCtx, cred, ""); err != nil {
			return fmt.Errorf("task %q: %w", taskFireLabel(t, i), err)
		}
	}
	return nil
}

// taskFireLabel names a task for error messages, falling back to the
// index form for the legacy single-fire (empty-id) shape.
func taskFireLabel(t *scenario.Task, i int) string {
	if t.ID != "" {
		return t.ID
	}
	return fmt.Sprintf("input[%d]", i)
}
