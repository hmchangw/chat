package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"

	"github.com/hmchangw/chat/pkg/atrest"
	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/model/cassandra"
	"github.com/hmchangw/chat/pkg/subject"
)

// historyBatchRequestTimeout matches the nats.go default request timeout used
// by the other history-service requesters in the codebase.
const historyBatchRequestTimeout = 2 * time.Second

// historyBatchChunkSize caps the message IDs sent in a single GetByIDs call.
// It mirrors history-service's server-side maxBatchMessageIDs (200, in
// history-service/internal/service/messages_batch.go) — exceeding it makes the
// batch RPC reject the whole request, so search-service splits its hit IDs into
// chunks no larger than this regardless of SEARCH_MAX_DOC_COUNTS.
const historyBatchChunkSize = 200

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

// historyBatchClient fetches decrypted message bodies from history-service's
// batch GetMessagesByIDs RPC. It is the Approach-B counterpart to the
// in-process decrypter: instead of decrypting ciphertext stored in the ES
// hit, search-service asks history-service (which owns Cassandra + the
// per-message access enforcement) for the plaintext. Defined in the consumer
// so tests can inject a fake.
type historyBatchClient interface {
	GetByIDs(ctx context.Context, account string, ids []string) ([]cassandra.Message, error)
}

// retrieveContentB is Approach B content retrieval: collect every hit's
// messageId, ask history-service for those bodies in batch RPC(s), then project
// the hits back in ES order filling Content from the returned map.
//
// The hit IDs are split into chunks of at most historyBatchChunkSize because
// history-service rejects a batch larger than its server-side cap; this keeps
// arm B working even when SEARCH_MAX_DOC_COUNTS is raised above that cap.
//
// Each chunk's fetch is best-effort and independent: a chunk that errors is
// logged (no content leaked — only the account and ID count) and leaves its
// hits with empty Content, while the other chunks still fill, so a transient
// history outage degrades the response rather than failing the whole search. A
// messageId absent from a reply (inaccessible per server-side enforcement, or
// simply missing) projects to empty Content — never a dropped hit.
func retrieveContentB(ctx context.Context, client historyBatchClient, account string, hits []encMessageSearchHit) []model.SearchMessage {
	out := make([]model.SearchMessage, 0, len(hits))
	if len(hits) == 0 {
		return out
	}

	ids := make([]string, 0, len(hits))
	for i := range hits {
		ids = append(ids, hits[i].MessageID)
	}

	byID := make(map[string]string, len(hits))
	for start := 0; start < len(ids); start += historyBatchChunkSize {
		end := start + historyBatchChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		msgs, err := client.GetByIDs(ctx, account, chunk)
		if err != nil {
			slog.WarnContext(ctx, "history batch fetch chunk failed; leaving chunk content empty",
				"account", account, "ids", len(chunk), "error", err)
			continue
		}
		for i := range msgs {
			byID[msgs[i].MessageID] = msgs[i].Msg
		}
	}

	for i := range hits {
		hit := &hits[i]
		out = append(out, toEncSearchMessage(hit, byID[hit.MessageID]))
	}
	return out
}

// natsHistoryBatchClient is the production historyBatchClient: a thin NATS
// requester over history-service's batch GetMessagesByIDs RPC. It mirrors the
// other history requesters in the codebase (message-gatekeeper's
// historyParentFetcher) — marshal the request, issue a request/reply at
// subject.MsgBatchGet, decode the typed errcode envelope first, then the
// payload.
type natsHistoryBatchClient struct {
	nc     *otelnats.Conn
	siteID string
}

func newNATSHistoryBatchClient(nc *otelnats.Conn, siteID string) *natsHistoryBatchClient {
	return &natsHistoryBatchClient{nc: nc, siteID: siteID}
}

// historyBatchRequest mirrors history-service's GetMessagesByIDsRequest wire
// shape (the source struct lives under internal/ and isn't importable).
type historyBatchRequest struct {
	MessageIDs []string `json:"messageIds"`
}

// historyBatchResponse mirrors history-service's GetMessagesByIDsResponse.
type historyBatchResponse struct {
	Messages []cassandra.Message `json:"messages"`
}

func (c *natsHistoryBatchClient) GetByIDs(ctx context.Context, account string, ids []string) ([]cassandra.Message, error) {
	reqBytes, err := json.Marshal(historyBatchRequest{MessageIDs: ids})
	if err != nil {
		return nil, fmt.Errorf("marshal batch GetMessagesByIDs request: %w", err)
	}

	subj := subject.MsgBatchGet(account, c.siteID)
	msg, err := c.nc.Request(ctx, subj, reqBytes, historyBatchRequestTimeout)
	if err != nil {
		return nil, fmt.Errorf("history batch request: %w", err)
	}

	// Detect the errcode error envelope first; a real response has no top-level
	// "error" field so this cannot false-positive. Propagate the typed remote
	// errcode so the upstream classification is preserved.
	if ee, ok := errcode.Parse(msg.Data); ok && ee.Code.Valid() {
		return nil, ee
	}

	var resp historyBatchResponse
	if err := json.Unmarshal(msg.Data, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal batch GetMessagesByIDs response: %w", err)
	}
	return resp.Messages, nil
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
