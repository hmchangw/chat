package model

import "strings"

// IsBotAccount reports whether an account name denotes a bot/pseudo user.
// The rule — a ".bot" suffix or a "p_" prefix — is the single source of truth
// for bot classification and is equivalent to the regex `(\.bot$|^p_)` used by
// pkg/pipelines and room-service. Subscriptions store the result in u.isBot so
// member-count reconciliation can split user vs app counts off an indexed field
// instead of evaluating a regex per document on every read.
func IsBotAccount(account string) bool {
	return strings.HasSuffix(account, ".bot") || strings.HasPrefix(account, "p_")
}
