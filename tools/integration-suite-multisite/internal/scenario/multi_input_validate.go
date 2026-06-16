package scenario

import (
	"fmt"
	"regexp"
	"sort"
)

// replyRefRe matches a ${<id>.reply.…} substitution token and captures
// the task id. Both ${id.reply.body_json.field} and ${id.reply.status}
// forms match; only the leading task id is captured.
var replyRefRe = regexp.MustCompile(`\$\{([a-z][a-z0-9_-]*)\.reply\.[^}]+\}`)

// ValidateMultiInput enforces the spec §4 multi-input rules. It runs
// after YAML decode (TaskList already normalized both shapes into
// []Task) and before any I/O.
//
//   - task ids unique within the scenario
//   - every list-shape task carries an id (a single empty-id task is
//     the legacy single-fire shape and is allowed)
//   - every ${<id>.reply.*} reference names a declared task that fires
//     EARLIER in declaration order (linear order ⇒ no cycle possible)
//   - expected[].match.task appears only on location: reply assertions
//     and names a declared task
func ValidateMultiInput(s *Scenario) error {
	// Index declared task ids to their declaration order. The legacy
	// single-fire shape (one task, empty id) is permitted: it can't be
	// referenced and needs no id.
	order := make(map[string]int, len(s.Input))
	multiTask := len(s.Input) > 1
	for i := range s.Input {
		id := s.Input[i].ID
		if id == "" {
			if multiTask {
				return fmt.Errorf("scenario %q: input[%d] is missing required id: (list-shape tasks must each declare an id)", s.Name, i)
			}
			continue
		}
		if prev, ok := order[id]; ok {
			return fmt.Errorf("scenario %q: duplicate task id %q at input[%d] and input[%d]", s.Name, id, prev, i)
		}
		order[id] = i
	}

	// Reply-reference validation: each task may reference only tasks
	// declared earlier.
	for i := range s.Input {
		t := &s.Input[i]
		for _, ref := range collectReplyRefs(t) {
			pos, ok := order[ref]
			if !ok {
				return fmt.Errorf("scenario %q: task %q references ${%s.reply.*} but no task with id %q is declared (declared: %v)",
					s.Name, taskLabel(t, i), ref, ref, sortedIDs(order))
			}
			if pos >= i {
				return fmt.Errorf("scenario %q: task %q references ${%s.reply.*} but task %q fires after %q in declaration order",
					s.Name, taskLabel(t, i), ref, ref, taskLabel(t, i))
			}
		}
	}

	// match.task scoping: reply-only, known id.
	for ei := range s.Expected {
		e := &s.Expected[ei]
		raw, has := e.Match["task"]
		if !has {
			continue
		}
		if e.Location != "reply" {
			return fmt.Errorf("scenario %q: expected[%d].match.task is only valid on location: reply (got %q)", s.Name, ei, e.Location)
		}
		id, ok := raw.(string)
		if !ok {
			return fmt.Errorf("scenario %q: expected[%d].match.task must be a string task id, got %T", s.Name, ei, raw)
		}
		if _, ok := order[id]; !ok {
			return fmt.Errorf("scenario %q: expected[%d].match.task references unknown task id %q (declared: %v)", s.Name, ei, id, sortedIDs(order))
		}
	}
	return nil
}

// collectReplyRefs returns the task ids referenced via ${<id>.reply.*}
// in a task's subject, credential, and payload (recursively).
func collectReplyRefs(t *Task) []string {
	seen := map[string]struct{}{}
	scanReplyRefs(t.Subject, seen)
	scanReplyRefs(t.Credential, seen)
	walkReplyRefs(t.Payload, seen)
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func scanReplyRefs(s string, seen map[string]struct{}) {
	for _, m := range replyRefRe.FindAllStringSubmatch(s, -1) {
		seen[m[1]] = struct{}{}
	}
}

// walkReplyRefs recurses through the payload's maps, slices, and string
// leaves collecting reply references.
func walkReplyRefs(v any, seen map[string]struct{}) {
	switch vv := v.(type) {
	case string:
		scanReplyRefs(vv, seen)
	case map[string]any:
		for _, e := range vv {
			walkReplyRefs(e, seen)
		}
	case []any:
		for _, e := range vv {
			walkReplyRefs(e, seen)
		}
	}
}

// taskLabel returns the task's id for error messages, falling back to
// the index form when the task has no id (legacy single-fire shape).
func taskLabel(t *Task, i int) string {
	if t.ID != "" {
		return t.ID
	}
	return fmt.Sprintf("input[%d]", i)
}

func sortedIDs(order map[string]int) []string {
	out := make([]string, 0, len(order))
	for id := range order {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
