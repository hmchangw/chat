package scenario

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// stepRefRe matches a ${<id>.…} substitution token and captures the
// id. The `.…` body can be any of reply.body_json.X, reply.status,
// body_json.X, or a direct payload field — the validator only cares
// about WHICH id is referenced.
var stepRefRe = regexp.MustCompile(`\$\{([a-z][a-z0-9_-]*)\.[^}]+\}`)

// ValidateFlow enforces spec §4 once ParseFlow has produced a
// FlowPlan. Returns warnings (non-fatal, printed by the validator)
// alongside the first fatal error.
func ValidateFlow(s *Scenario, plan *FlowPlan) (warnings []string, err error) {
	// Index ids by kind. Inputs must all have ids when flow is set;
	// expecteds must all have ids too.
	inputKind := map[string]int{} // id -> input index
	for i := range s.Input {
		t := &s.Input[i]
		if t.ID == "" {
			return nil, fmt.Errorf("scenario %q: flow present but input[%d] is missing required id", s.Name, i)
		}
		if prev, ok := inputKind[t.ID]; ok {
			return nil, fmt.Errorf("scenario %q: duplicate input id %q at input[%d] and input[%d]", s.Name, t.ID, prev, i)
		}
		inputKind[t.ID] = i
	}
	expectedKind := map[string]int{} // id -> expected index
	for i := range s.Expected {
		e := &s.Expected[i]
		if e.ID == "" {
			return nil, fmt.Errorf("scenario %q: flow present but expected[%d] is missing required id", s.Name, i)
		}
		if prev, ok := expectedKind[e.ID]; ok {
			return nil, fmt.Errorf("scenario %q: duplicate expected id %q at expected[%d] and expected[%d]", s.Name, e.ID, prev, i)
		}
		if _, clash := inputKind[e.ID]; clash {
			return nil, fmt.Errorf("scenario %q: id %q used by both an input and expected[%d]", s.Name, e.ID, i)
		}
		expectedKind[e.ID] = i
	}

	// Build a position table from the flow + resolve each barrier's
	// kind. Reject mixed-type, parallel-fire, unknown refs, duplicates.
	idPosition := map[string]int{} // id -> barrier index (0-based)
	for bi := range plan.Barriers {
		b := &plan.Barriers[bi]
		var kind string
		for _, id := range b.IDs {
			if _, ok := inputKind[id]; ok {
				if kind == "" {
					kind = "fire"
				} else if kind != "fire" {
					return nil, fmt.Errorf("scenario %q: flow group %v must be homogeneous (contains both inputs and expecteds)", s.Name, b.IDs)
				}
			} else if _, ok := expectedKind[id]; ok {
				if kind == "" {
					kind = "observe"
				} else if kind != "observe" {
					return nil, fmt.Errorf("scenario %q: flow group %v must be homogeneous (contains both inputs and expecteds)", s.Name, b.IDs)
				}
			} else {
				return nil, fmt.Errorf("scenario %q: flow references unknown id %q (declared inputs: %v; declared expecteds: %v)",
					s.Name, id, sortedKeys(inputKind), sortedKeys(expectedKind))
			}
			if _, dup := idPosition[id]; dup {
				return nil, fmt.Errorf("scenario %q: id %q appears in flow twice", s.Name, id)
			}
			idPosition[id] = bi
		}
		if kind == "fire" && len(b.IDs) > 1 {
			return nil, fmt.Errorf("scenario %q: flow group %v is a parallel fire group — not supported in v1 (deferred to the concurrency slice)", s.Name, b.IDs)
		}
		b.Kind = kind
	}

	// Every input + expected must appear in flow.
	for id, idx := range inputKind {
		if _, ok := idPosition[id]; !ok {
			return nil, fmt.Errorf("scenario %q: input[%d] (id=%q) is not referenced by flow", s.Name, idx, id)
		}
	}
	for id, idx := range expectedKind {
		if _, ok := idPosition[id]; !ok {
			return nil, fmt.Errorf("scenario %q: expected[%d] (id=%q) is not referenced by flow", s.Name, idx, id)
		}
	}

	// Per-expected validation: of:, match.task:, ambiguous reply scope.
	// Pass 1: index reply-observation positions to enable ambiguity check.
	type replyObs struct {
		id  string
		pos int
		of  string
		idx int // expected[] index
	}
	var replies []replyObs
	for i := range s.Expected {
		e := &s.Expected[i]
		if e.Location == "reply" {
			replies = append(replies, replyObs{id: e.ID, pos: idPosition[e.ID], of: e.Of, idx: i})
		}
	}
	sort.Slice(replies, func(i, j int) bool { return replies[i].pos < replies[j].pos })

	for i := range s.Expected {
		e := &s.Expected[i]
		// of: validation
		if e.Of != "" {
			if e.Location != "reply" {
				return nil, fmt.Errorf("scenario %q: expected[%d] (id=%q): of: is valid only on location: reply (got %q)",
					s.Name, i, e.ID, e.Location)
			}
			ofPos, ok := inputKind[e.Of]
			if !ok {
				return nil, fmt.Errorf("scenario %q: expected[%d] (id=%q): of: %q must name a declared input (declared: %v)",
					s.Name, i, e.ID, e.Of, sortedKeys(inputKind))
			}
			_ = ofPos
			if idPosition[e.Of] >= idPosition[e.ID] {
				return nil, fmt.Errorf("scenario %q: expected[%d] (id=%q): of: %q fires at or after this observation in the flow",
					s.Name, i, e.ID, e.Of)
			}
		}
		// match.task: rejected in flow scenarios.
		if e.Match != nil {
			if _, has := e.Match["task"]; has {
				return nil, fmt.Errorf("scenario %q: expected[%d] (id=%q): match.task: is not valid in flow scenarios; use expected[%d].of instead",
					s.Name, i, e.ID, i)
			}
		}
	}

	// Ambiguous reply scope: for each reply observation without `of:`,
	// the number of fire barriers between it and the previous reply
	// observation (or scenario start) must be exactly one.
	prevReplyPos := -1
	for _, r := range replies {
		if r.of != "" {
			prevReplyPos = r.pos
			continue
		}
		// Count fire-kind barriers in (prevReplyPos, r.pos).
		firesBetween := []string{}
		for bi := prevReplyPos + 1; bi < r.pos; bi++ {
			if plan.Barriers[bi].Kind == "fire" {
				firesBetween = append(firesBetween, plan.Barriers[bi].IDs[0])
			}
		}
		if len(firesBetween) > 1 {
			return nil, fmt.Errorf("scenario %q: expected[%d] (id=%q): ambiguous reply scope — fires %v precede without an intervening reply observation; add of: <input-id>",
				s.Name, r.idx, r.id, firesBetween)
		}
		if len(firesBetween) == 0 {
			return nil, fmt.Errorf("scenario %q: expected[%d] (id=%q): reply observation has no preceding fire in the flow",
				s.Name, r.idx, r.id)
		}
		prevReplyPos = r.pos
	}

	// Substitution-reference validation: scan every input/expected body
	// for ${<id>.…} tokens; the referenced id must point earlier in
	// the flow than the referencing id, and never at a sibling inside
	// the same parallel group.
	idIsExpected := map[string]bool{}
	for id := range expectedKind {
		idIsExpected[id] = true
	}
	for i := range s.Input {
		t := &s.Input[i]
		for _, ref := range collectStepRefs(t.Subject, t.Credential, t.Payload) {
			if err := checkRef(s.Name, t.ID, ref, idPosition, plan); err != nil {
				return nil, err
			}
		}
	}
	for i := range s.Expected {
		e := &s.Expected[i]
		refs := append(collectStepRefsInMap(e.Args), collectStepRefsInMap(e.Match)...)
		for _, ref := range refs {
			if err := checkRef(s.Name, e.ID, ref, idPosition, plan); err != nil {
				return nil, err
			}
		}
	}

	// Negative-placement warning (not error).
	finalBarrier := len(plan.Barriers) - 1
	for i := range s.Expected {
		e := &s.Expected[i]
		if !e.Not {
			continue
		}
		if idPosition[e.ID] != finalBarrier {
			warnings = append(warnings,
				fmt.Sprintf("scenario %q: expected[%d] (id=%q): not:true observe entries prove absence only during their own window; place end-state absence checks in the final barrier for whole-scenario coverage (see AUTHORING.md §Negative-observe semantics)",
					s.Name, i, e.ID))
		}
	}

	return warnings, nil
}

// checkRef validates a single ${<id>.…} reference against the flow
// position table.
func checkRef(scenarioName, fromID, refID string, idPosition map[string]int, plan *FlowPlan) error {
	fromPos, ok := idPosition[fromID]
	if !ok {
		return nil // fromID isn't in flow (shouldn't happen by here)
	}
	refPos, ok := idPosition[refID]
	if !ok {
		// Unknown id reference — handled later by substitution at runtime
		// in the legacy path; in flow scenarios we hard-reject up-front.
		return fmt.Errorf("scenario %q: id %q references ${%s.…} but no input/expected with id %q is declared",
			scenarioName, fromID, refID, refID)
	}
	if refPos > fromPos {
		return fmt.Errorf("scenario %q: id %q references ${%s.…} but %q is positioned after %q in the flow",
			scenarioName, fromID, refID, refID, fromID)
	}
	if refPos == fromPos {
		// Same barrier — only valid if size 1 (i.e., self-ref, which is also a bug
		// but already caught by the no-duplicates rule).
		b := plan.Barriers[refPos]
		if len(b.IDs) > 1 {
			return fmt.Errorf("scenario %q: id %q references ${%s.…} from inside the same parallel group %v",
				scenarioName, fromID, refID, b.IDs)
		}
	}
	return nil
}

// collectStepRefs scans the union of subject, credential, and payload
// for ${<id>.…} references.
func collectStepRefs(subject, credential string, payload map[string]any) []string {
	seen := map[string]struct{}{}
	scanRefs(subject, seen)
	scanRefs(credential, seen)
	walkRefs(payload, seen)
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// collectStepRefsInMap is the args/match counterpart — no subject /
// credential, just a map walk.
func collectStepRefsInMap(m map[string]any) []string {
	seen := map[string]struct{}{}
	walkRefs(m, seen)
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func scanRefs(s string, seen map[string]struct{}) {
	for _, m := range stepRefRe.FindAllStringSubmatch(s, -1) {
		seen[m[1]] = struct{}{}
	}
}

func walkRefs(v any, seen map[string]struct{}) {
	switch vv := v.(type) {
	case string:
		scanRefs(vv, seen)
	case map[string]any:
		for _, e := range vv {
			walkRefs(e, seen)
		}
	case []any:
		for _, e := range vv {
			walkRefs(e, seen)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// _ = strings keep import even if helpers slim down
var _ = strings.TrimSpace
