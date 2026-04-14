package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/searchengine"
)

// spotlightCollection implements Collection for spotlight room-typeahead
// search. Documents are per-subscription (one doc per (user, room) pair) so
// the search service can filter by userAccount and match on roomName.
type spotlightCollection struct {
	inboxMemberCollection
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
	evt, payload, err := parseMemberEvent(data)
	if err != nil {
		return nil, err
	}
	if payload.Subscription.ID == "" {
		return nil, fmt.Errorf("build spotlight action: missing subscription id")
	}

	switch evt.Type {
	case model.OutboxMemberAdded:
		doc := newSpotlightSearchIndex(payload)
		body, err := json.Marshal(doc)
		if err != nil {
			return nil, fmt.Errorf("marshal spotlight doc: %w", err)
		}
		return []searchengine.BulkAction{{
			Action:  searchengine.ActionIndex,
			Index:   c.indexName,
			DocID:   payload.Subscription.ID,
			Version: evt.Timestamp,
			Doc:     body,
		}}, nil
	case model.OutboxMemberRemoved:
		return []searchengine.BulkAction{{
			Action:  searchengine.ActionDelete,
			Index:   c.indexName,
			DocID:   payload.Subscription.ID,
			Version: evt.Timestamp,
		}}, nil
	default:
		return nil, fmt.Errorf("build spotlight action: unsupported event type %q", evt.Type)
	}
}

// SpotlightSearchIndex defines the Elasticsearch document structure for the
// spotlight index. One doc per subscription.
type SpotlightSearchIndex struct {
	SubscriptionID string    `json:"subscriptionId" es:"keyword"`
	UserID         string    `json:"userId"         es:"keyword"`
	UserAccount    string    `json:"userAccount"    es:"keyword"`
	RoomID         string    `json:"roomId"         es:"keyword"`
	RoomName       string    `json:"roomName"       es:"search_as_you_type,custom_analyzer"`
	RoomType       string    `json:"roomType"       es:"keyword"`
	SiteID         string    `json:"siteId"         es:"keyword"`
	JoinedAt       time.Time `json:"joinedAt"       es:"date"`
}

func newSpotlightSearchIndex(p *model.MemberAddedPayload) SpotlightSearchIndex {
	return SpotlightSearchIndex{
		SubscriptionID: p.Subscription.ID,
		UserID:         p.Subscription.User.ID,
		UserAccount:    p.Subscription.User.Account,
		RoomID:         p.Subscription.RoomID,
		RoomName:       p.Room.Name,
		RoomType:       string(p.Room.Type),
		SiteID:         p.Subscription.SiteID,
		JoinedAt:       p.Subscription.JoinedAt,
	}
}

// spotlightTemplateBody builds the ES index template for the spotlight
// collection. The `index_patterns` field is set to the exact configured
// index name so a custom SPOTLIGHT_INDEX value still receives the correct
// mapping (no broad wildcard that might catch unrelated indices).
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
							"type":        "whitespace",
							"token_chars": []string{"letter", "digit", "punctuation", "symbol"},
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
