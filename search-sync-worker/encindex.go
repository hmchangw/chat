package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/searchindex"
)

// EncMessageDoc is the encrypted-index document. It mirrors MessageSearchIndex's
// metadata fields (es-tagged, reflected into the template) but replaces the
// plaintext `content` with blind tokens + an atrest ciphertext blob. The
// contentEnc/encNonce mappings are authored in encMessageTemplateProperties
// because the es-tag reflection helper cannot express the binary type (which is
// inherently non-indexed in Elasticsearch — it rejects an explicit `index`
// parameter).
type EncMessageDoc struct {
	MessageID             string     `json:"messageId"                              es:"keyword"`
	RoomID                string     `json:"roomId"                                 es:"keyword"`
	SiteID                string     `json:"siteId"                                 es:"keyword"`
	UserID                string     `json:"userId"                                 es:"keyword"`
	UserAccount           string     `json:"userAccount"                            es:"keyword"`
	ContentBlind          string     `json:"contentBlind"                           es:"text,whitespace"`
	ContentEnc            []byte     `json:"contentEnc"` // mapping authored explicitly
	EncNonce              []byte     `json:"encNonce"`   // mapping authored explicitly
	BlindKeyVersion       string     `json:"blindKeyVersion"                        es:"keyword"`
	CreatedAt             time.Time  `json:"createdAt"                              es:"date"`
	EditedAt              *time.Time `json:"editedAt,omitempty"                     es:"date"`
	UpdatedAt             *time.Time `json:"updatedAt,omitempty"                    es:"date"`
	ThreadParentID        string     `json:"threadParentMessageId,omitempty"        es:"keyword"`
	ThreadParentCreatedAt *time.Time `json:"threadParentMessageCreatedAt,omitempty" es:"date"`
	TShow                 bool       `json:"tshow,omitempty"                        es:"boolean"`
}

// encMessageTemplateProperties reflects the es-tagged metadata fields, then
// injects the binary mappings the reflector cannot express. The binary type is
// non-indexed by definition in Elasticsearch and rejects an explicit `index`
// parameter, so the mapping carries only the type.
func encMessageTemplateProperties() map[string]any {
	props := esPropertiesFromStruct[EncMessageDoc]()
	props["contentEnc"] = map[string]any{"type": "binary"}
	props["encNonce"] = map[string]any{"type": "binary"}
	return props
}

func encMessageTemplateBody(prefix string) json.RawMessage {
	tmpl := map[string]any{
		"index_patterns": []string{fmt.Sprintf("%s-*", searchindex.StripVersionBase(prefix))},
		"template": map[string]any{
			"settings": map[string]any{
				"index": map[string]any{
					"number_of_shards":   4,
					"number_of_replicas": 2,
					"refresh_interval":   "30s",
				},
			},
			"mappings": map[string]any{
				"dynamic":    false,
				"properties": encMessageTemplateProperties(),
			},
		},
	}
	data, _ := json.Marshal(tmpl)
	return data
}

func buildEncDocument(evt *model.MessageEvent, contentBlind string, contentEnc, encNonce []byte, keyVersion string) json.RawMessage {
	doc := EncMessageDoc{
		MessageID:             evt.Message.ID,
		RoomID:                evt.Message.RoomID,
		SiteID:                evt.SiteID,
		UserID:                evt.Message.UserID,
		UserAccount:           evt.Message.UserAccount,
		ContentBlind:          contentBlind,
		ContentEnc:            contentEnc,
		EncNonce:              encNonce,
		BlindKeyVersion:       keyVersion,
		CreatedAt:             evt.Message.CreatedAt,
		EditedAt:              evt.Message.EditedAt,
		UpdatedAt:             evt.Message.UpdatedAt,
		ThreadParentID:        evt.Message.ThreadParentMessageID,
		ThreadParentCreatedAt: evt.Message.ThreadParentMessageCreatedAt,
		TShow:                 evt.Message.TShow,
	}
	data, _ := json.Marshal(doc)
	return data
}
