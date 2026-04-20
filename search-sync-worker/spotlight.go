package main

import (
	"encoding/json"
	"fmt"

	"github.com/hmchangw/chat/pkg/searchengine"
)

type spotlightCollection struct {
	roomMemberCollection
	indexName string
}

func newSpotlightCollection(indexName string) *spotlightCollection {
	return &spotlightCollection{indexName: indexName}
}

func (c *spotlightCollection) ConsumerName() string {
	return "spotlight-sync"
}

func (c *spotlightCollection) TemplateName() string {
	return "spotlight_template"
}

func (c *spotlightCollection) TemplateBody() json.RawMessage {
	return spotlightTemplateBody(c.indexName)
}

func (c *spotlightCollection) BuildAction(data []byte) ([]searchengine.BulkAction, error) {
	evt, err := parseMemberEvent(data)
	if err != nil {
		return nil, err
	}

	if evt.Add != nil {
		add := evt.Add
		if add.HistorySharedSince > 0 {
			return nil, nil
		}
		actions := make([]searchengine.BulkAction, 0, len(add.Accounts))
		for _, account := range add.Accounts {
			doc := SpotlightSearchIndex{
				UserAccount: account,
				RoomID:      add.RoomID,
				RoomName:    add.RoomName,
				RoomType:    string(add.RoomType),
				SiteID:      add.SiteID,
				JoinedAt:    add.JoinedAt,
			}
			body, err := json.Marshal(doc)
			if err != nil {
				return nil, fmt.Errorf("marshal spotlight doc: %w", err)
			}
			actions = append(actions, searchengine.BulkAction{
				Action:  searchengine.ActionIndex,
				Index:   c.indexName,
				DocID:   spotlightDocID(account, add.RoomID),
				Version: add.Timestamp,
				Doc:     body,
			})
		}
		return actions, nil
	}

	if evt.Remove != nil {
		rm := evt.Remove
		actions := make([]searchengine.BulkAction, 0, len(rm.Accounts))
		for _, account := range rm.Accounts {
			actions = append(actions, searchengine.BulkAction{
				Action:  searchengine.ActionDelete,
				Index:   c.indexName,
				DocID:   spotlightDocID(account, rm.RoomID),
				Version: rm.Timestamp,
			})
		}
		return actions, nil
	}

	return nil, fmt.Errorf("spotlight: no add or remove event parsed")
}

func spotlightDocID(account, roomID string) string {
	return account + "_" + roomID
}

type SpotlightSearchIndex struct {
	UserAccount string `json:"userAccount" es:"keyword"`
	RoomID      string `json:"roomId"      es:"keyword"`
	RoomName    string `json:"roomName"    es:"search_as_you_type,custom_analyzer"`
	RoomType    string `json:"roomType"    es:"keyword"`
	SiteID      string `json:"siteId"      es:"keyword"`
	JoinedAt    int64  `json:"joinedAt"    es:"date"`
}

func spotlightTemplateBody(indexName string) json.RawMessage {
	tmpl := map[string]any{
		"index_patterns": []string{indexName},
		"template": map[string]any{
			"settings": map[string]any{
				"index": map[string]any{
					"number_of_shards":   3,
					"number_of_replicas": 1,
				},
				"analysis": map[string]any{
					"analyzer": map[string]any{
						"custom_analyzer": map[string]any{
							"type":      "custom",
							"tokenizer": "custom_tokenizer",
							"filter":    []string{"lowercase"},
						},
					},
					"tokenizer": map[string]any{
						"custom_tokenizer": map[string]any{
							"type": "whitespace",
						},
					},
				},
			},
			"mappings": map[string]any{
				"dynamic":    false,
				"properties": esPropertiesFromStruct[SpotlightSearchIndex](),
			},
		},
	}
	data, _ := json.Marshal(tmpl)
	return data
}
