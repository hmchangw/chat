package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

func TestEncMessageTemplateProperties_FieldShapes(t *testing.T) {
	props := encMessageTemplateProperties()

	// contentBlind: text + whitespace analyzer, NOT custom_analyzer.
	cb := props["contentBlind"].(map[string]any)
	assert.Equal(t, "text", cb["type"])
	assert.Equal(t, "whitespace", cb["analyzer"])

	// contentEnc / encNonce: binary, not indexed.
	for _, f := range []string{"contentEnc", "encNonce"} {
		m := props[f].(map[string]any)
		assert.Equal(t, "binary", m["type"], f)
		assert.Equal(t, false, m["index"], f)
	}

	assert.Equal(t, "keyword", props["blindKeyVersion"].(map[string]any)["type"])

	// Metadata preserved (sample); plaintext `content` must be ABSENT.
	assert.Equal(t, "keyword", props["roomId"].(map[string]any)["type"])
	assert.Equal(t, "date", props["createdAt"].(map[string]any)["type"])
	_, hasContent := props["content"]
	assert.False(t, hasContent, "encrypted index must not map plaintext content")
}

func TestEncMessageTemplateBody_PatternAndNoCustomAnalyzer(t *testing.T) {
	body := encMessageTemplateBody("enc-messages-v1")
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	patterns := parsed["index_patterns"].([]any)
	// StripVersionBase drops the -vN suffix; the version lives in the rolling
	// index name, never in the template index_patterns (mirrors messages.go).
	assert.Equal(t, "enc-messages-*", patterns[0])

	// No custom_analyzer block on the encrypted template.
	tmpl := parsed["template"].(map[string]any)
	settings, _ := json.Marshal(tmpl["settings"])
	assert.NotContains(t, string(settings), "custom_analyzer")
}

func TestBuildEncDocument_Roundtrips(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	evt := &model.MessageEvent{
		SiteID:    "site-a",
		Timestamp: now.UnixMilli(),
		Message: model.Message{
			ID: "m1", RoomID: "r1", UserID: "u1", UserAccount: "acc1",
			Content: "hello", CreatedAt: now,
		},
	}
	doc := buildEncDocument(evt, "blindfield hashes", []byte("CIPHER"), []byte("NONCE12bytes"), "v1")

	var got EncMessageDoc
	require.NoError(t, json.Unmarshal(doc, &got))
	assert.Equal(t, "m1", got.MessageID)
	assert.Equal(t, "r1", got.RoomID)
	assert.Equal(t, "blindfield hashes", got.ContentBlind)
	assert.Equal(t, []byte("CIPHER"), got.ContentEnc)
	assert.Equal(t, "v1", got.BlindKeyVersion)
	assert.Equal(t, now.UTC(), got.CreatedAt.UTC())
}
