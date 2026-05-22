package cassandra

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMessageReactionRow_JSONRoundTrip(t *testing.T) {
	original := MessageReactionRow{
		MessageID: "msg-123",
		Emoji:     "👍",
		Users: []Participant{
			{ID: "u1", EngName: "Alice", Account: "alice"},
			{ID: "u2", EngName: "Bob", Account: "bob"},
		},
	}
	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded MessageReactionRow
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, original, decoded)
}
