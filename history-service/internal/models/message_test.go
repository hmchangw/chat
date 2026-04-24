package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEditMessageRequest_JSON(t *testing.T) {
	req := EditMessageRequest{
		MessageID: "m-abc",
		NewMsg:    "corrected text",
	}
	data, err := json.Marshal(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{"messageId":"m-abc","newMsg":"corrected text"}`, string(data))

	var decoded EditMessageRequest
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, req, decoded)
}

func TestEditMessageResponse_JSON(t *testing.T) {
	resp := EditMessageResponse{
		MessageID: "m-abc",
		EditedAt:  1_714_000_000_000,
	}
	data, err := json.Marshal(resp)
	require.NoError(t, err)
	assert.JSONEq(t, `{"messageId":"m-abc","editedAt":1714000000000}`, string(data))

	var decoded EditMessageResponse
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, resp, decoded)
}

func TestMessageEditedEvent_JSON(t *testing.T) {
	evt := MessageEditedEvent{
		Type:      "message_edited",
		Timestamp: 1_714_000_000_000,
		RoomID:    "r1",
		MessageID: "m-abc",
		NewMsg:    "corrected text",
		EditedBy:  "alice",
		EditedAt:  1_714_000_000_000,
	}
	data, err := json.Marshal(evt)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"type":"message_edited",
		"timestamp":1714000000000,
		"roomId":"r1",
		"messageId":"m-abc",
		"newMsg":"corrected text",
		"editedBy":"alice",
		"editedAt":1714000000000
	}`, string(data))

	var decoded MessageEditedEvent
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, evt, decoded)
}
