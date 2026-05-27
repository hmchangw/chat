package cassandra

import (
	"encoding/json"
	"errors"
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
		assert.Equal(t, "[]", string(data))
	})

	t.Run("single", func(t *testing.T) {
		r := Reactions{
			ReactionKey{Emoji: "👍", UserAccount: "alice"}: ReactorInfo{
				UserID: "u1", EngName: "Alice", ChnName: "爱丽丝", Account: "alice", ReactedAt: now,
			},
		}
		data, err := json.Marshal(r)
		require.NoError(t, err)
		var entries []map[string]any
		require.NoError(t, json.Unmarshal(data, &entries))
		require.Len(t, entries, 1)
		assert.Equal(t, "👍", entries[0]["emoji"])
		assert.Equal(t, "alice", entries[0]["userAccount"])
		assert.Equal(t, "u1", entries[0]["userId"])
	})

	t.Run("sorted_multi", func(t *testing.T) {
		r := Reactions{
			ReactionKey{Emoji: "👍", UserAccount: "alice"}: ReactorInfo{
				UserID: "u1", EngName: "Alice", ChnName: "爱丽丝", Account: "alice", ReactedAt: now,
			},
			ReactionKey{Emoji: "❤️", UserAccount: "bob"}: ReactorInfo{
				UserID: "u2", EngName: "Bob", ChnName: "鲍勃", Account: "bob", ReactedAt: now,
			},
			ReactionKey{Emoji: "👍", UserAccount: "carol"}: ReactorInfo{
				UserID: "u3", EngName: "Carol", ChnName: "卡罗尔", Account: "carol", ReactedAt: now,
			},
		}
		data, err := json.Marshal(r)
		require.NoError(t, err)
		var entries []map[string]any
		require.NoError(t, json.Unmarshal(data, &entries))
		require.Len(t, entries, 3)
		// Sort: ❤️ < 👍, then alice < carol within 👍.
		assert.Equal(t, "❤️", entries[0]["emoji"])
		assert.Equal(t, "bob", entries[0]["userAccount"])
		assert.Equal(t, "👍", entries[1]["emoji"])
		assert.Equal(t, "alice", entries[1]["userAccount"])
		assert.Equal(t, "👍", entries[2]["emoji"])
		assert.Equal(t, "carol", entries[2]["userAccount"])
		assert.Equal(t, "u2", entries[0]["userId"])
		assert.Equal(t, "Bob", entries[0]["engName"])
		assert.Equal(t, "鲍勃", entries[0]["chnName"])
		assert.Equal(t, "bob", entries[0]["account"])
	})
}

func TestReactions_UnmarshalJSON(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)

	t.Run("null", func(t *testing.T) {
		r := Reactions{ReactionKey{Emoji: "x", UserAccount: "a"}: ReactorInfo{UserID: "u"}}
		require.NoError(t, r.UnmarshalJSON([]byte("null")))
		assert.Nil(t, r)
	})

	t.Run("empty_array", func(t *testing.T) {
		var r Reactions
		require.NoError(t, r.UnmarshalJSON([]byte("[]")))
		require.NotNil(t, r)
		assert.Len(t, r, 0)
	})

	t.Run("single", func(t *testing.T) {
		want := Reactions{
			ReactionKey{Emoji: "👍", UserAccount: "alice"}: ReactorInfo{
				UserID: "u1", EngName: "Alice", ChnName: "爱丽丝", Account: "alice", ReactedAt: now,
			},
		}
		data, err := json.Marshal(want)
		require.NoError(t, err)
		var got Reactions
		require.NoError(t, json.Unmarshal(data, &got))
		require.Len(t, got, 1)
		gv, ok := got[ReactionKey{Emoji: "👍", UserAccount: "alice"}]
		require.True(t, ok)
		assert.Equal(t, want[ReactionKey{Emoji: "👍", UserAccount: "alice"}], gv)
	})

	t.Run("multi", func(t *testing.T) {
		want := Reactions{
			ReactionKey{Emoji: "👍", UserAccount: "alice"}: ReactorInfo{
				UserID: "u1", EngName: "Alice", ChnName: "爱丽丝", Account: "alice", ReactedAt: now,
			},
			ReactionKey{Emoji: "❤️", UserAccount: "bob"}: ReactorInfo{
				UserID: "u2", EngName: "Bob", ChnName: "鲍勃", Account: "bob", ReactedAt: now,
			},
		}
		data, err := json.Marshal(want)
		require.NoError(t, err)
		var got Reactions
		require.NoError(t, json.Unmarshal(data, &got))
		require.Len(t, got, len(want))
		for k, v := range want {
			gv, ok := got[k]
			require.True(t, ok, "missing key %+v", k)
			assert.Equal(t, v, gv)
		}
	})

	t.Run("duplicate_key_error", func(t *testing.T) {
		raw := []byte(`[
			{"emoji":"👍","userAccount":"alice","userId":"u1","engName":"Alice","chnName":"爱丽丝","account":"alice","reactedAt":"2026-05-25T10:22:00Z"},
			{"emoji":"👍","userAccount":"alice","userId":"u1","engName":"Alice","chnName":"爱丽丝","account":"alice","reactedAt":"2026-05-25T10:22:00Z"}
		]`)
		var r Reactions
		err := r.UnmarshalJSON(raw)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate key")
		assert.Contains(t, err.Error(), "alice")
		assert.True(t, errors.Is(err, ErrDuplicateReactionKey), "error chain must include ErrDuplicateReactionKey")
	})

	t.Run("malformed_json_error", func(t *testing.T) {
		var r Reactions
		err := r.UnmarshalJSON([]byte(`{not valid json`))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unmarshal reactions array")
	})
}
