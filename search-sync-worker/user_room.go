package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/searchengine"
)

type userRoomCollection struct {
	roomMemberCollection
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
	evt, err := parseMemberEvent(data)
	if err != nil {
		return nil, err
	}

	if evt.Add != nil {
		add := evt.Add
		if add.HistorySharedSince > 0 {
			return nil, nil
		}
		ts := add.Timestamp
		actions := make([]searchengine.BulkAction, 0, len(add.Accounts))
		for _, account := range add.Accounts {
			body, err := buildAddRoomUpdateBody(account, add.RoomID, ts)
			if err != nil {
				return nil, err
			}
			actions = append(actions, searchengine.BulkAction{
				Action: searchengine.ActionUpdate,
				Index:  c.indexName,
				DocID:  account,
				Doc:    body,
			})
		}
		return actions, nil
	}

	if evt.Remove != nil {
		rm := evt.Remove
		ts := rm.Timestamp
		actions := make([]searchengine.BulkAction, 0, len(rm.Accounts))
		for _, account := range rm.Accounts {
			body, err := buildRemoveRoomUpdateBody(rm.RoomID, ts)
			if err != nil {
				return nil, err
			}
			actions = append(actions, searchengine.BulkAction{
				Action: searchengine.ActionUpdate,
				Index:  c.indexName,
				DocID:  account,
				Doc:    body,
			})
		}
		return actions, nil
	}

	return nil, fmt.Errorf("user-room: no add or remove event parsed")
}

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
