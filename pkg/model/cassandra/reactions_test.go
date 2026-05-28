package cassandra

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReactionKey_JSONRoundTrip(t *testing.T) {
	k := ReactionKey{Emoji: "👍", UserAccount: "alice"}
	roundTrip(t, k)
}

func TestReactorInfo_JSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	ri := ReactorInfo{
		UserID:    "u1",
		EngName:   "Alice",
		ChnName:   "爱丽丝",
		Account:   "alice",
		ReactedAt: now,
	}
	roundTrip(t, ri)
}

func TestReactions_MarshalJSON(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)

	t.Run("nil", func(t *testing.T) {
		// Direct marshal exercises the nil branch that omitempty otherwise skips.
		data, err := json.Marshal(Reactions(nil))
		require.NoError(t, err)
		assert.Equal(t, "null", string(data))
	})

	t.Run("nil_omitted_via_omitempty", func(t *testing.T) {
		msg := Message{
			RoomID:    "r1",
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			MessageID: "m1",
			Sender:    Participant{ID: "u1", Account: "alice"},
			Msg:       "hi",
		}
		data, err := json.Marshal(msg)
		require.NoError(t, err)
		var raw map[string]any
		require.NoError(t, json.Unmarshal(data, &raw))
		_, present := raw["reactions"]
		assert.False(t, present, "nil Reactions should be omitted via omitempty")
	})

	t.Run("empty", func(t *testing.T) {
		data, err := json.Marshal(Reactions{})
		require.NoError(t, err)
		assert.Equal(t, "{}", string(data))
	})

	t.Run("single", func(t *testing.T) {
		r := Reactions{
			ReactionKey{Emoji: "👍", UserAccount: "alice"}: ReactorInfo{
				UserID: "u1", EngName: "Alice", ChnName: "爱丽丝", Account: "alice", ReactedAt: now,
			},
		}
		data, err := json.Marshal(r)
		require.NoError(t, err)
		var grouped map[string][]map[string]string
		require.NoError(t, json.Unmarshal(data, &grouped))
		require.Contains(t, grouped, "👍")
		require.Len(t, grouped["👍"], 1)
		assert.Equal(t, "alice", grouped["👍"][0]["account"])
		assert.Equal(t, "Alice 爱丽丝", grouped["👍"][0]["displayName"])
	})

	t.Run("grouped_by_emoji", func(t *testing.T) {
		// Inner-array order is unspecified (matches the existing backend convention —
		// no sort). Assertions are set-based, not positional.
		r := Reactions{
			ReactionKey{Emoji: "👍", UserAccount: "carol"}: ReactorInfo{
				UserID: "u3", EngName: "Carol", ChnName: "卡罗尔", Account: "carol", ReactedAt: now,
			},
			ReactionKey{Emoji: "👍", UserAccount: "alice"}: ReactorInfo{
				UserID: "u1", EngName: "Alice", ChnName: "爱丽丝", Account: "alice", ReactedAt: now,
			},
			ReactionKey{Emoji: "❤️", UserAccount: "bob"}: ReactorInfo{
				UserID: "u2", EngName: "Bob", ChnName: "鲍勃", Account: "bob", ReactedAt: now,
			},
		}
		data, err := json.Marshal(r)
		require.NoError(t, err)
		var grouped map[string][]map[string]string
		require.NoError(t, json.Unmarshal(data, &grouped))
		require.Contains(t, grouped, "❤️")
		require.Contains(t, grouped, "👍")
		assert.ElementsMatch(t, []map[string]string{
			{"account": "alice", "displayName": "Alice 爱丽丝"},
			{"account": "carol", "displayName": "Carol 卡罗尔"},
		}, grouped["👍"])
		assert.ElementsMatch(t, []map[string]string{
			{"account": "bob", "displayName": "Bob 鲍勃"},
		}, grouped["❤️"])
	})

	t.Run("displayName_fallback_to_account", func(t *testing.T) {
		r := Reactions{
			ReactionKey{Emoji: "👍", UserAccount: "anon"}: ReactorInfo{Account: "anon", ReactedAt: now},
		}
		data, err := json.Marshal(r)
		require.NoError(t, err)
		var grouped map[string][]map[string]string
		require.NoError(t, json.Unmarshal(data, &grouped))
		assert.Equal(t, "anon", grouped["👍"][0]["displayName"])
	})

	t.Run("one_user_multiple_different_emoji", func(t *testing.T) {
		// Spec §1: a user may react with multiple different emoji on the same
		// message. The same account must appear under each emoji bucket they
		// reacted with — different ReactionKey, distinct map entries.
		r := Reactions{
			ReactionKey{Emoji: "👍", UserAccount: "alice"}:  ReactorInfo{EngName: "Alice", Account: "alice", ReactedAt: now},
			ReactionKey{Emoji: "❤️", UserAccount: "alice"}: ReactorInfo{EngName: "Alice", Account: "alice", ReactedAt: now},
			ReactionKey{Emoji: "🎉", UserAccount: "alice"}:  ReactorInfo{EngName: "Alice", Account: "alice", ReactedAt: now},
		}
		data, err := json.Marshal(r)
		require.NoError(t, err)
		var grouped map[string][]map[string]string
		require.NoError(t, json.Unmarshal(data, &grouped))
		assert.Len(t, grouped, 3, "expected three emoji buckets, one per distinct reaction")
		for _, emoji := range []string{"👍", "❤️", "🎉"} {
			require.Len(t, grouped[emoji], 1, "alice should appear exactly once under %q", emoji)
			assert.Equal(t, "alice", grouped[emoji][0]["account"])
		}
	})

	t.Run("no_duplicate_account_within_emoji_bucket", func(t *testing.T) {
		// Spec §1 self-uniqueness: the (emoji, userAccount) map key guarantees
		// one user cannot appear twice under the same emoji. Verified at the
		// type level (Go disallows duplicate map keys) and re-asserted here so
		// any future regression that bypasses the map shape is caught.
		r := Reactions{
			ReactionKey{Emoji: "👍", UserAccount: "alice"}: ReactorInfo{Account: "alice", ReactedAt: now},
			ReactionKey{Emoji: "👍", UserAccount: "bob"}:   ReactorInfo{Account: "bob", ReactedAt: now},
			ReactionKey{Emoji: "👍", UserAccount: "carol"}: ReactorInfo{Account: "carol", ReactedAt: now},
		}
		data, err := json.Marshal(r)
		require.NoError(t, err)
		var grouped map[string][]map[string]string
		require.NoError(t, json.Unmarshal(data, &grouped))
		require.Len(t, grouped["👍"], 3)
		seen := make(map[string]bool, 3)
		for _, u := range grouped["👍"] {
			require.False(t, seen[u["account"]], "duplicate account %q in 👍 bucket", u["account"])
			seen[u["account"]] = true
		}
	})
}
