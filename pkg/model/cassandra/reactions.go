package cassandra

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// ErrDuplicateReactionKey is returned when UnmarshalJSON encounters
// two records with the same (emoji, userAccount) pair.
var ErrDuplicateReactionKey = errors.New("duplicate reaction key")

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

// Reactions is the in-row reaction map. Keys are unique (emoji, user_account) pairs; JSON is a flat array sorted for stable output.
type Reactions map[ReactionKey]ReactorInfo

type reactionEntry struct {
	Emoji       string    `json:"emoji"`
	UserAccount string    `json:"userAccount"`
	UserID      string    `json:"userId"`
	EngName     string    `json:"engName"`
	ChnName     string    `json:"chnName"`
	Account     string    `json:"account"`
	ReactedAt   time.Time `json:"reactedAt"`
}

// MarshalJSON emits a flat array sorted by (emoji, userAccount); nil → "null", empty → "[]".
func (r Reactions) MarshalJSON() ([]byte, error) {
	if r == nil {
		return []byte("null"), nil
	}
	if len(r) == 0 {
		return []byte("[]"), nil
	}
	entries := make([]reactionEntry, 0, len(r))
	for k, v := range r {
		entries = append(entries, reactionEntry{
			Emoji:       k.Emoji,
			UserAccount: k.UserAccount,
			UserID:      v.UserID,
			EngName:     v.EngName,
			ChnName:     v.ChnName,
			Account:     v.Account,
			ReactedAt:   v.ReactedAt,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Emoji != entries[j].Emoji {
			return entries[i].Emoji < entries[j].Emoji
		}
		return entries[i].UserAccount < entries[j].UserAccount
	})
	return json.Marshal(entries)
}

// UnmarshalJSON parses the flat array into the map; null → nil, [] → empty map, duplicates → error.
func (r *Reactions) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*r = nil
		return nil
	}
	var entries []reactionEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("unmarshal reactions array: %w", err)
	}
	out := make(Reactions, len(entries))
	for _, e := range entries {
		key := ReactionKey{Emoji: e.Emoji, UserAccount: e.UserAccount}
		if _, ok := out[key]; ok {
			return fmt.Errorf("reactions: duplicate key (%s, %s): %w", e.Emoji, e.UserAccount, ErrDuplicateReactionKey)
		}
		out[key] = ReactorInfo{
			UserID:    e.UserID,
			EngName:   e.EngName,
			ChnName:   e.ChnName,
			Account:   e.Account,
			ReactedAt: e.ReactedAt,
		}
	}
	*r = out
	return nil
}
