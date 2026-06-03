package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/hmchangw/chat/pkg/atrest"
	"github.com/hmchangw/chat/pkg/model"
)

// encMessageSearchHit is the `_source` shape of an encrypted-message ES hit.
// It mirrors messageSearchHit's metadata but carries the per-room atrest
// ciphertext (contentEnc) + nonce (encNonce) INSTEAD of plaintext content —
// the encrypted index never stores a `content` field, so this struct has no
// way to surface plaintext straight off the wire.
type encMessageSearchHit struct {
	MessageID             string     `json:"messageId"`
	RoomID                string     `json:"roomId"`
	SiteID                string     `json:"siteId"`
	UserID                string     `json:"userId"`
	UserAccount           string     `json:"userAccount"`
	ContentEnc            []byte     `json:"contentEnc"`
	EncNonce              []byte     `json:"encNonce"`
	BlindKeyVersion       string     `json:"blindKeyVersion"`
	CreatedAt             time.Time  `json:"createdAt"`
	EditedAt              *time.Time `json:"editedAt,omitempty"`
	UpdatedAt             *time.Time `json:"updatedAt,omitempty"`
	ThreadParentID        string     `json:"threadParentMessageId,omitempty"`
	ThreadParentCreatedAt *time.Time `json:"threadParentMessageCreatedAt,omitempty"`
}

// decrypter is the subset of atrest.Cipher the enc search path needs. The
// signature matches atrest.Cipher.Decrypt exactly so the production cipher
// satisfies it directly and tests can inject a fake.
type decrypter interface {
	Decrypt(ctx context.Context, roomID string, payload []byte, meta atrest.EncMeta) (atrest.EncryptedFields, error)
}

// parseEncMessagesResponse decodes an ES `_search` response from the
// encrypted index into encMessageSearchHit staging structs.
func parseEncMessagesResponse(raw json.RawMessage) ([]encMessageSearchHit, int64, error) {
	var rr rawResponse[encMessageSearchHit]
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, 0, fmt.Errorf("parse enc messages response: %w", err)
	}

	out := make([]encMessageSearchHit, 0, len(rr.Hits.Hits))
	for i := range rr.Hits.Hits {
		out = append(out, rr.Hits.Hits[i].Source)
	}
	return out, rr.Hits.Total.Value, nil
}

// retrieveContentA is Approach A content retrieval: decrypt each hit's
// ciphertext in-process to recover the plaintext message, then project to
// the public wire type.
//
// A per-hit decrypt failure is non-fatal: it is logged (no secrets — only
// the messageId and the error) and the hit is kept with empty Content so a
// single corrupt/unauthorized row never sinks an entire search response.
func retrieveContentA(ctx context.Context, d decrypter, hits []encMessageSearchHit) []model.SearchMessage {
	out := make([]model.SearchMessage, 0, len(hits))
	for i := range hits {
		hit := &hits[i]
		content := ""
		fields, err := d.Decrypt(ctx, hit.RoomID, hit.ContentEnc, atrest.EncMeta{Nonce: hit.EncNonce})
		if err != nil {
			slog.WarnContext(ctx, "decrypt search hit failed; returning empty content",
				"messageId", hit.MessageID, "roomId", hit.RoomID, "error", err)
		} else {
			content = fields.Msg
		}
		out = append(out, toEncSearchMessage(hit, content))
	}
	return out
}

// toEncSearchMessage projects an encMessageSearchHit plus its decrypted
// content into the public model.SearchMessage wire type. It mirrors
// toSearchMessage but sources Content from the supplied (decrypted) value
// rather than from the hit.
func toEncSearchMessage(hit *encMessageSearchHit, content string) model.SearchMessage {
	return model.SearchMessage{
		MessageID:                    hit.MessageID,
		RoomID:                       hit.RoomID,
		SiteID:                       hit.SiteID,
		UserAccount:                  hit.UserAccount,
		Content:                      content,
		CreatedAt:                    hit.CreatedAt,
		EditedAt:                     hit.EditedAt,
		UpdatedAt:                    hit.UpdatedAt,
		ThreadParentMessageID:        hit.ThreadParentID,
		ThreadParentMessageCreatedAt: hit.ThreadParentCreatedAt,
	}
}
