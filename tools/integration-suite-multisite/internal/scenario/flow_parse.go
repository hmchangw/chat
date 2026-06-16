package scenario

import (
	"fmt"
	"strings"
)

// FlowPlan is the ordered sequence of barriers a flow:-bearing
// scenario executes. Each barrier is a non-empty list of ids that
// run as a group (size 1 for sequential, size N for parallel
// observation groups). Kind is filled by the validator after id-kind
// resolution; the parser doesn't know whether an id is a fire or an
// observation.
type FlowPlan struct {
	Barriers []FlowBarrier
}

// FlowBarrier is one position in the flow.
type FlowBarrier struct {
	IDs  []string // size 1 = sequential; size N>1 = parallel group
	Kind string   // "fire" | "observe" — set by validator; empty after parse
}

// ParseFlow tokenizes the FlowExpression's normalized Raw string per
// spec §3.3:
//
//	flow  ::= group ( ">>" group )*
//	group ::= id | "[" id ( "," id )* "]"
//
// The parser is hand-rolled — small enough to keep clear, and the
// failure modes need precise messages.
func ParseFlow(expr *FlowExpression) (*FlowPlan, error) {
	if expr == nil || strings.TrimSpace(expr.Raw) == "" {
		return nil, fmt.Errorf("flow: must not be empty")
	}

	groups, err := splitOnArrow(expr.Raw)
	if err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("flow: must not be empty")
	}

	plan := &FlowPlan{Barriers: make([]FlowBarrier, 0, len(groups))}
	for i, g := range groups {
		ids, err := parseGroup(g)
		if err != nil {
			return nil, fmt.Errorf("flow: group %d (%q): %w", i, g, err)
		}
		plan.Barriers = append(plan.Barriers, FlowBarrier{IDs: ids})
	}
	return plan, nil
}

// splitOnArrow splits on ">>" while rejecting empty / trailing /
// leading arrows. The split is character-based (not regex) so
// whitespace handling is explicit.
func splitOnArrow(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("flow: must not be empty")
	}

	// Walk and split on literal ">>". Brackets do not contain ">>" in
	// a well-formed flow; any ">>" inside brackets is a syntax error.
	parts := []string{}
	cur := strings.Builder{}
	inBracket := false
	i := 0
	for i < len(s) {
		if s[i] == '[' {
			if inBracket {
				return nil, fmt.Errorf("nested brackets not supported")
			}
			inBracket = true
			cur.WriteByte(s[i])
			i++
			continue
		}
		if s[i] == ']' {
			if !inBracket {
				return nil, fmt.Errorf("unbalanced bracket — ']' without matching '['")
			}
			inBracket = false
			cur.WriteByte(s[i])
			i++
			continue
		}
		if !inBracket && i+1 < len(s) && s[i] == '>' && s[i+1] == '>' {
			piece := strings.TrimSpace(cur.String())
			if piece == "" {
				return nil, fmt.Errorf("malformed flow: missing group before '>>'")
			}
			parts = append(parts, piece)
			cur.Reset()
			i += 2
			continue
		}
		cur.WriteByte(s[i])
		i++
	}
	if inBracket {
		return nil, fmt.Errorf("unbalanced bracket — '[' without matching ']'")
	}
	tail := strings.TrimSpace(cur.String())
	if tail == "" {
		return nil, fmt.Errorf("malformed flow: trailing '>>' with no group after")
	}
	parts = append(parts, tail)
	return parts, nil
}

// parseGroup interprets one of the >>-separated pieces, either a
// bare id or a bracketed comma-list.
func parseGroup(piece string) ([]string, error) {
	piece = strings.TrimSpace(piece)
	if piece == "" {
		return nil, fmt.Errorf("empty group")
	}
	if !strings.HasPrefix(piece, "[") {
		// Bare id.
		if !validID(piece) {
			return nil, fmt.Errorf("invalid id %q", piece)
		}
		return []string{piece}, nil
	}
	// Bracketed group. Must end with ']'.
	if !strings.HasSuffix(piece, "]") {
		return nil, fmt.Errorf("unbalanced bracket in group %q", piece)
	}
	inner := strings.TrimSpace(piece[1 : len(piece)-1])
	if inner == "" {
		return nil, fmt.Errorf("empty group")
	}
	rawIDs := strings.Split(inner, ",")
	ids := make([]string, 0, len(rawIDs))
	for _, r := range rawIDs {
		id := strings.TrimSpace(r)
		if id == "" {
			return nil, fmt.Errorf("empty id in parallel group %q (consecutive commas?)", piece)
		}
		if !validID(id) {
			return nil, fmt.Errorf("invalid id %q in parallel group", id)
		}
		ids = append(ids, id)
	}
	if len(ids) < 1 {
		return nil, fmt.Errorf("empty group")
	}
	return ids, nil
}

// validID matches the spec §3.3 id grammar: [a-z][a-z0-9_-]*. Kept
// permissive of underscore + hyphen to match step-id conventions.
func validID(s string) bool {
	if s == "" {
		return false
	}
	if s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '_' || c == '-':
		default:
			return false
		}
	}
	return true
}
