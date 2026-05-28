package cassandra

import (
	"encoding/json"
	"time"

	"github.com/hmchangw/chat/pkg/displayfmt"
)

// ReactionKey is the map-key UDT for Message.Reactions.
type ReactionKey struct {
	// Emoji must be NFC-normalised by writers; enforcement is in the message gatekeeper, not this type.
	Emoji       string `json:"emoji"       cql:"emoji"`
	UserAccount string `json:"userAccount" cql:"user_account"`
}

// ReactorInfo is the map-value UDT for Message.Reactions.
type ReactorInfo struct {
	UserID    string    `json:"userId"    cql:"user_id"`
	EngName   string    `json:"engName"   cql:"eng_name"`
	ChnName   string    `json:"chnName"   cql:"chn_name"`
	Account   string    `json:"account"   cql:"account"`
	ReactedAt time.Time `json:"reactedAt" cql:"reacted_at"`
}

// Reactions is the in-row reaction map. Stored as map[(emoji,userAccount)]reactor;
// emitted on the wire grouped by emoji with composed display names — see MarshalJSON.
type Reactions map[ReactionKey]ReactorInfo

// reactionUser is the per-reactor record emitted on the wire.
type reactionUser struct {
	Account     string `json:"account"`
	DisplayName string `json:"displayName"`
}

// MarshalJSON groups reactors by emoji and emits map<emoji, [{account, displayName}]>.
// Order is unspecified at both levels (JSON object keys; per-emoji arrays follow Go map
// iteration order). nil → "null"; empty → "{}".
func (r Reactions) MarshalJSON() ([]byte, error) {
	if r == nil {
		return []byte("null"), nil
	}
	grouped := make(map[string][]reactionUser, len(r))
	for k, v := range r {
		grouped[k.Emoji] = append(grouped[k.Emoji], reactionUser{
			Account:     k.UserAccount,
			DisplayName: displayfmt.CombineWithFallback(v.EngName, v.ChnName, k.UserAccount),
		})
	}
	return json.Marshal(grouped)
}
