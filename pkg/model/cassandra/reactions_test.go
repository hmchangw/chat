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

	t.Run("grouped_by_emoji_with_sorted_users", func(t *testing.T) {
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
		// Inner arrays sorted by account ASC (pending architect's final sort decision).
		require.Len(t, grouped["👍"], 2)
		assert.Equal(t, "alice", grouped["👍"][0]["account"])
		assert.Equal(t, "carol", grouped["👍"][1]["account"])
		require.Len(t, grouped["❤️"], 1)
		assert.Equal(t, "bob", grouped["❤️"][0]["account"])
		// displayName composition: "Eng Chn" with account fallback when both blank.
		assert.Equal(t, "Bob 鲍勃", grouped["❤️"][0]["displayName"])
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
}
