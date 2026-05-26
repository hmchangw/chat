package scenario

import (
	"fmt"
	"sort"

	"github.com/hmchangw/chat/tools/integration-suite-v2/internal/fixtures"
)

// Resolution holds the per-scenario placeholder → fixture bindings.
type Resolution struct {
	Users map[string]fixtures.CastUser
}

// Resolve binds each placeholder in s to a concrete cast fixture.
// Currently supports user placeholders only; rooms/messages added in
// later parts as scenarios demand.
//
// Placeholders are processed in name-sorted order (deterministic
// across runs). Within a run, each user is picked at most once across
// all placeholders: two placeholders with the same predicate get
// distinct fixtures. Fails fast if the predicate matches but every
// match is already taken by an earlier placeholder.
func Resolve(s *Scenario, cast *fixtures.Cast) (*Resolution, error) {
	out := &Resolution{Users: map[string]fixtures.CastUser{}}
	used := map[string]bool{}

	names := make([]string, 0, len(s.Input.Placeholders))
	for k := range s.Input.Placeholders {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, name := range names {
		p := s.Input.Placeholders[name]
		switch p.Type {
		case "user":
			matches := matchUserPredicate(cast, p.Predicate)
			if len(matches) == 0 {
				return nil, fmt.Errorf("placeholder %q: no fixture matches predicate %v", name, p.Predicate)
			}
			var picked *fixtures.CastUser
			for i := range matches {
				if !used[matches[i].Account] {
					picked = &matches[i]
					break
				}
			}
			if picked == nil {
				return nil, fmt.Errorf("placeholder %q: predicate %v matched %d users but all already picked by earlier placeholders", name, p.Predicate, len(matches))
			}
			out.Users[name] = *picked
			used[picked.Account] = true
		default:
			return nil, fmt.Errorf("placeholder %q: unsupported type %q (v2-Part1 supports 'user' only)", name, p.Type)
		}
	}
	return out, nil
}

func matchUserPredicate(cast *fixtures.Cast, pred map[string]any) []fixtures.CastUser {
	wantTags := tagListFromPredicate(pred)
	return cast.FindByPredicate(wantTags, 0)
}

// tagListFromPredicate turns {verified: true, banned: false} into ["verified"].
// Bool-true predicates require the tag; bool-false predicates are ignored at
// this layer (absence isn't currently expressible via the cast tag list).
func tagListFromPredicate(pred map[string]any) []string {
	var out []string
	for k, v := range pred {
		if b, ok := v.(bool); ok && b {
			out = append(out, k)
		}
	}
	return out
}
