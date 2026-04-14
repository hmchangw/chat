package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/searchengine"
)

// userRoomCollection implements Collection for the user-room access-control
// index. It maintains a per-user rooms array (one doc per user) used by the
// search service as a terms filter on message search queries.
//
// Concurrency model: the collection is safe to run with multiple search-sync
// worker pods sharing the same durable consumer. Out-of-order delivery of
// (added, removed) pairs for the same (user, room) is handled by an
// application-level timestamp guard inside the painless scripts — each update
// carries the OutboxEvent timestamp in `params.ts` and compares against the
// per-room stored timestamp in `ctx._source.roomTimestamps`, skipping the
// write (via `ctx.op = 'none'`) if the incoming event is stale. Concurrent
// writers from different pods serialize on the primary shard's per-doc lock,
// so the guard converges on last-write-wins-by-event-timestamp regardless of
// physical arrival order.
type userRoomCollection struct {
	inboxMemberCollection
	indexName string
}

func newUserRoomCollection(indexName string) *userRoomCollection {
	return &userRoomCollection{indexName: indexName}
}

func (c *userRoomCollection) ConsumerName() string {
	return "user-room-sync"
}

func (c *userRoomCollection) TemplateName() string {
	return "user_room_template"
}

func (c *userRoomCollection) TemplateBody() json.RawMessage {
	return userRoomTemplateBody(c.indexName)
}

// addRoomScript / removeRoomScript implement application-level last-write-wins
// on (user, room) using `params.ts` (OutboxEvent.Timestamp in millis). Stale
// events short-circuit via `ctx.op = 'none'` which tells ES to skip the write
// entirely — no version bump, no disk I/O.
const (
	addRoomScript = `if (ctx._source.roomTimestamps == null) { ctx._source.roomTimestamps = [:]; } ` +
		`if (ctx._source.rooms == null) { ctx._source.rooms = []; } ` +
		`long stored = ctx._source.roomTimestamps.containsKey(params.rid) ` +
		`? ((Number)ctx._source.roomTimestamps.get(params.rid)).longValue() : 0L; ` +
		`if (params.ts > stored) { ` +
		`if (!ctx._source.rooms.contains(params.rid)) { ctx._source.rooms.add(params.rid); } ` +
		`ctx._source.roomTimestamps.put(params.rid, params.ts); ` +
		`ctx._source.updatedAt = params.now; ` +
		`} else { ctx.op = 'none'; }`

	removeRoomScript = `if (ctx._source.roomTimestamps == null) { ctx._source.roomTimestamps = [:]; } ` +
		`long stored = ctx._source.roomTimestamps.containsKey(params.rid) ` +
		`? ((Number)ctx._source.roomTimestamps.get(params.rid)).longValue() : 0L; ` +
		`if (params.ts > stored) { ` +
		`if (ctx._source.rooms != null) { ` +
		`int idx = ctx._source.rooms.indexOf(params.rid); ` +
		`if (idx >= 0) { ctx._source.rooms.remove(idx); } } ` +
		`ctx._source.roomTimestamps.put(params.rid, params.ts); ` +
		`} else { ctx.op = 'none'; }`
)

func (c *userRoomCollection) BuildAction(data []byte) ([]searchengine.BulkAction, error) {
	evt, payload, err := parseMemberEvent(data)
	if err != nil {
		return nil, err
	}
	if payload.Subscription.User.Account == "" {
		return nil, fmt.Errorf("build user-room action: missing user account")
	}
	if payload.Subscription.RoomID == "" {
		return nil, fmt.Errorf("build user-room action: missing room id")
	}

	// Restricted rooms (HistorySharedSince set) are handled by the search
	// service via DB+cache at query time — skip indexing.
	if payload.Subscription.HistorySharedSince != nil {
		return nil, nil
	}

	account := payload.Subscription.User.Account
	roomID := payload.Subscription.RoomID
	ts := evt.Timestamp

	switch evt.Type {
	case model.OutboxMemberAdded:
		body, err := buildAddRoomUpdateBody(account, roomID, ts)
		if err != nil {
			return nil, err
		}
		return []searchengine.BulkAction{{
			Action: searchengine.ActionUpdate,
			Index:  c.indexName,
			DocID:  account,
			Doc:    body,
		}}, nil
	case model.OutboxMemberRemoved:
		body, err := buildRemoveRoomUpdateBody(roomID, ts)
		if err != nil {
			return nil, err
		}
		return []searchengine.BulkAction{{
			Action: searchengine.ActionUpdate,
			Index:  c.indexName,
			DocID:  account,
			Doc:    body,
		}}, nil
	default:
		return nil, fmt.Errorf("build user-room action: unsupported event type %q", evt.Type)
	}
}

// userRoomUpsertDoc is the full document inserted when the user has no prior
// user-room entry (i.e., the first time a room is added for this user). The
// `RoomTimestamps` map seeds the per-room timestamp guard.
type userRoomUpsertDoc struct {
	UserAccount    string           `json:"userAccount"`
	Rooms          []string         `json:"rooms"`
	RoomTimestamps map[string]int64 `json:"roomTimestamps"`
	CreatedAt      string           `json:"createdAt"`
	UpdatedAt      string           `json:"updatedAt"`
}

func buildAddRoomUpdateBody(account, roomID string, ts int64) (json.RawMessage, error) {
	now := time.UnixMilli(ts).UTC().Format(time.RFC3339Nano)
	body := map[string]any{
		"script": map[string]any{
			"source": addRoomScript,
			"lang":   "painless",
			"params": map[string]any{
				"rid": roomID,
				"ts":  ts,
				"now": now,
			},
		},
		"upsert": userRoomUpsertDoc{
			UserAccount:    account,
			Rooms:          []string{roomID},
			RoomTimestamps: map[string]int64{roomID: ts},
			CreatedAt:      now,
			UpdatedAt:      now,
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal add room update: %w", err)
	}
	return data, nil
}

func buildRemoveRoomUpdateBody(roomID string, ts int64) (json.RawMessage, error) {
	body := map[string]any{
		"script": map[string]any{
			"source": removeRoomScript,
			"lang":   "painless",
			"params": map[string]any{
				"rid": roomID,
				"ts":  ts,
			},
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal remove room update: %w", err)
	}
	return data, nil
}

// userRoomTemplateBody builds the ES index template for the user-room
// collection. The `index_patterns` field is set to the exact configured
// index name so a custom USER_ROOM_INDEX value still receives the correct
// mapping. The `roomTimestamps` field is mapped as `flattened` so new
// roomIds don't balloon the mapping with per-key dynamic sub-fields.
func userRoomTemplateBody(indexName string) json.RawMessage {
	tmpl := map[string]any{
		"index_patterns": []string{indexName},
		"template": map[string]any{
			"settings": map[string]any{
				"index": map[string]any{
					"number_of_shards":   1,
					"number_of_replicas": 1,
				},
			},
			"mappings": map[string]any{
				"dynamic": false,
				"properties": map[string]any{
					"userAccount": map[string]any{"type": "keyword"},
					"rooms": map[string]any{
						"type": "text",
						"fields": map[string]any{
							"keyword": map[string]any{"type": "keyword", "ignore_above": 256},
						},
					},
					"roomTimestamps": map[string]any{"type": "flattened"},
					"createdAt":      map[string]any{"type": "date"},
					"updatedAt":      map[string]any{"type": "date"},
				},
			},
		},
	}
	data, _ := json.Marshal(tmpl)
	return data
}
