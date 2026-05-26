// Package fixtures holds the fixture cast: the seeded test-universe
// roster. The cast is read-only by verbs; verbs may only create
// run-prefixed temp data.
package fixtures

import "github.com/hmchangw/chat/pkg/model"

// CastUser is one user fixture with materialized credentials.
// Credentials are filled in by the seeder at startup (nkey generated;
// auth-service /auth called to mint a NATS JWT).
//
// UserDoc, when non-nil, is the joined production-shape user record
// (from the seed JSON under `tools/integration-suite-v2/seed/`). The
// runtime exposes its JSON fields via ${<placeholder>.<field>}
// substitution in scenario `input.*` and `reads[].expected.*` blocks.
// See `internal/runtime/substitute.go` and the placeholder-value-
// plumbing spec.
type CastUser struct {
	ID       string
	Tags     []string
	Account  string
	JWT      string
	NkeySeed string
	UserDoc  *model.User
}

// Cast is the in-memory roster after seeding.
type Cast struct {
	Users []CastUser
	// Rooms / Messages / Subscriptions added in later parts as scenarios demand.
}

// FindByPredicate returns up to maxN users matching every (tag) in tags.
// Returns nil if fewer than 1 match.
func (c *Cast) FindByPredicate(tags []string, maxN int) []CastUser {
	var out []CastUser
	for _, u := range c.Users {
		if hasAllTags(u.Tags, tags) {
			out = append(out, u)
			if maxN > 0 && len(out) >= maxN {
				break
			}
		}
	}
	return out
}

func hasAllTags(have, want []string) bool {
	set := map[string]struct{}{}
	for _, t := range have {
		set[t] = struct{}{}
	}
	for _, t := range want {
		if _, ok := set[t]; !ok {
			return false
		}
	}
	return true
}
