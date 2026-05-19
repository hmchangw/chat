package main

import (
	"context"
	"strings"

	"github.com/hmchangw/chat/pkg/mention"
	"github.com/hmchangw/chat/pkg/model"
)

// isBot returns true if account follows the bot naming convention used across
// the codebase (suffix `.bot` or prefix `p_`). Mirrors the predicate in
// message-gatekeeper/helper.go and room-service/helper.go — promoting to a
// shared pkg/botid is a future cleanup; keep these copies in sync if the
// convention changes.
func isBot(account string) bool {
	return strings.HasSuffix(account, ".bot") || strings.HasPrefix(account, "p_")
}

// dedupedAccounts returns sender prepended to mentions, with later duplicates
// dropped. Order matters because it's part of the FindUsersByAccounts contract
// in handler tests; sender comes first so the deduped list shape is stable
// across the no-mention and mention paths.
func dedupedAccounts(sender string, mentions []string) []string {
	out := make([]string, 0, 1+len(mentions))
	seen := make(map[string]struct{}, 1+len(mentions))
	out = append(out, sender)
	seen[sender] = struct{}{}
	for _, a := range mentions {
		if _, ok := seen[a]; ok {
			continue
		}
		seen[a] = struct{}{}
		out = append(out, a)
	}
	return out
}

// memoLookup adapts a pre-fetched account->User map to mention.LookupFunc so
// mention.Resolve can enrich Participants without a second store round-trip.
// Missing accounts are simply omitted from the returned slice, matching the
// real store's semantics for unknown accounts.
func memoLookup(users map[string]model.User) mention.LookupFunc {
	return func(_ context.Context, accounts []string) ([]model.User, error) {
		out := make([]model.User, 0, len(accounts))
		for _, a := range accounts {
			if u, ok := users[a]; ok {
				out = append(out, u)
			}
		}
		return out, nil
	}
}
