package main

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func mustRaw(t *testing.T, m bson.M) bson.Raw {
	t.Helper()
	r, err := bson.Marshal(m)
	require.NoError(t, err)
	return r
}

func TestBuildEnvelope_OpsAndSubjects(t *testing.T) {
	const site = "site1"
	const nowMs = int64(1718100000123)

	tests := []struct {
		name        string
		op          string
		hasFull     bool
		hasPreImage bool
		wantSubject string
	}{
		{"insert", "insert", true, false, "chat.oplog.site1.rocketchat_message.insert"},
		{"update", "update", true, false, "chat.oplog.site1.rocketchat_message.update"},
		{"replace", "replace", true, false, "chat.oplog.site1.rocketchat_message.replace"},
		{"delete with preimage", "delete", false, true, "chat.oplog.site1.rocketchat_message.delete"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := changeEvent{
				EventID:       "EVT-" + tc.op,
				Op:            tc.op,
				DB:            "rocketchat",
				Collection:    "rocketchat_message",
				DocumentKey:   mustRaw(t, bson.M{"_id": "abc"}),
				ClusterTimeMs: 1718100000000,
			}
			if tc.hasFull {
				ev.FullDocument = mustRaw(t, bson.M{"_id": "abc", "msg": "hi"})
			}
			if tc.hasPreImage {
				ev.PreImage = mustRaw(t, bson.M{"_id": "abc", "msg": "bye"})
			}

			subj, msgID, evt, err := buildEnvelope(&ev, site, nowMs)
			require.NoError(t, err)

			assert.Equal(t, tc.wantSubject, subj)
			assert.Equal(t, ev.EventID, msgID, "msgID must equal eventID")
			assert.Equal(t, ev.EventID, evt.EventID)
			assert.Equal(t, tc.op, evt.Op)
			assert.Equal(t, "rocketchat", evt.DB)
			assert.Equal(t, "rocketchat_message", evt.Collection)
			assert.Equal(t, site, evt.SiteID)
			assert.Equal(t, nowMs, evt.Timestamp, "event-level timestamp is the injected publish time")
			assert.Equal(t, int64(1718100000000), evt.ClusterTime)

			// documentKey is always present and valid JSON.
			assert.True(t, json.Valid(evt.DocumentKey))

			if tc.hasFull {
				require.NotNil(t, evt.FullDocument)
				assert.True(t, json.Valid(evt.FullDocument))
			} else {
				assert.Nil(t, evt.FullDocument, "delete carries no post-image")
			}

			if tc.hasPreImage {
				require.NotNil(t, evt.PreImage)
				assert.True(t, json.Valid(evt.PreImage))
			} else {
				assert.Nil(t, evt.PreImage)
			}
		})
	}
}

func TestBuildEnvelope_OpaqueDocumentContents(t *testing.T) {
	ev := changeEvent{
		EventID:      "E1",
		Op:           "insert",
		DB:           "rocketchat",
		Collection:   "users",
		DocumentKey:  mustRaw(t, bson.M{"_id": "u1"}),
		FullDocument: mustRaw(t, bson.M{"_id": "u1", "name": "alice", "active": true}),
	}
	_, _, evt, err := buildEnvelope(&ev, "site1", 1)
	require.NoError(t, err)

	// The connector does not interpret the doc; it round-trips as JSON.
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(evt.FullDocument, &decoded))
	assert.Equal(t, "alice", decoded["name"])
	assert.Equal(t, true, decoded["active"])
}
