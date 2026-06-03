package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/atrest"
	"github.com/hmchangw/chat/pkg/model/cassandra"
)

// fakeHistoryBatchClient is a hand-written stub matching historyBatchClient.
// It records each call's account+ids and returns the configured messages.
type fakeHistoryBatchClient struct {
	msgs    []cassandra.Message
	err     error
	callIDs [][]string
	callAcc []string
}

func (f *fakeHistoryBatchClient) GetByIDs(_ context.Context, account string, ids []string) ([]cassandra.Message, error) {
	f.callAcc = append(f.callAcc, account)
	f.callIDs = append(f.callIDs, ids)
	if f.err != nil {
		return nil, f.err
	}
	return f.msgs, nil
}

func TestRetrieveContentB_FetchesOnceAndMapsByID(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	hits := []encMessageSearchHit{
		{MessageID: "m1", RoomID: "r1", SiteID: "site-a", UserAccount: "alice", CreatedAt: created},
		{MessageID: "m2", RoomID: "r2", SiteID: "site-a", UserAccount: "bob", CreatedAt: created},
	}
	// History returns the messages out of ES order on purpose: the result
	// must be projected in ES order, content filled from the ID map.
	client := &fakeHistoryBatchClient{msgs: []cassandra.Message{
		{MessageID: "m2", RoomID: "r2", Msg: "world"},
		{MessageID: "m1", RoomID: "r1", Msg: "hello"},
	}}

	out := retrieveContentB(context.Background(), client, "alice", hits)
	require.Len(t, out, 2)
	assert.Equal(t, "m1", out[0].MessageID)
	assert.Equal(t, "hello", out[0].Content)
	assert.Equal(t, "m2", out[1].MessageID)
	assert.Equal(t, "world", out[1].Content)

	// Exactly one batch call, with all hit IDs and the caller's account.
	require.Len(t, client.callIDs, 1, "history must be queried exactly once")
	assert.Equal(t, []string{"m1", "m2"}, client.callIDs[0])
	assert.Equal(t, []string{"alice"}, client.callAcc)
}

func TestRetrieveContentB_MissingIDYieldsEmptyContent(t *testing.T) {
	hits := []encMessageSearchHit{
		{MessageID: "m1", RoomID: "r1"},
		{MessageID: "m2", RoomID: "r2"},
	}
	// History only returns m1 (m2 inaccessible/missing) → m2 content empty.
	client := &fakeHistoryBatchClient{msgs: []cassandra.Message{
		{MessageID: "m1", Msg: "hello"},
	}}

	out := retrieveContentB(context.Background(), client, "alice", hits)
	require.Len(t, out, 2)
	assert.Equal(t, "hello", out[0].Content)
	assert.Equal(t, "m2", out[1].MessageID)
	assert.Equal(t, "", out[1].Content, "missing id must project to empty content, not drop the hit")
}

func TestRetrieveContentB_ClientErrorReturnsHitsWithEmptyContent(t *testing.T) {
	hits := []encMessageSearchHit{
		{MessageID: "m1", RoomID: "r1"},
	}
	client := &fakeHistoryBatchClient{err: errors.New("history down")}

	out := retrieveContentB(context.Background(), client, "alice", hits)
	require.Len(t, out, 1)
	assert.Equal(t, "m1", out[0].MessageID)
	assert.Equal(t, "", out[0].Content)
}

func TestRetrieveContentB_Empty(t *testing.T) {
	client := &fakeHistoryBatchClient{}
	out := retrieveContentB(context.Background(), client, "alice", nil)
	assert.Empty(t, out)
	assert.NotNil(t, out, "must return a non-nil empty slice")
	assert.Empty(t, client.callIDs, "no hits → no history call")
}

// fakeDecrypter is a hand-written stub matching the decrypter interface.
// It returns a per-roomID plaintext, or an error for roomIDs in failRooms.
type fakeDecrypter struct {
	byRoom    map[string]string
	failRooms map[string]bool
	calls     []decryptCall
}

type decryptCall struct {
	roomID  string
	payload []byte
	nonce   []byte
}

func (f *fakeDecrypter) Decrypt(_ context.Context, roomID string, payload []byte, meta atrest.EncMeta) (atrest.EncryptedFields, error) {
	f.calls = append(f.calls, decryptCall{roomID: roomID, payload: payload, nonce: meta.Nonce})
	if f.failRooms[roomID] {
		return atrest.EncryptedFields{}, errors.New("decrypt failed")
	}
	return atrest.EncryptedFields{Msg: f.byRoom[roomID]}, nil
}

// encESResponse builds a minimal ES `_search` response carrying enc hits.
func encESResponse(t *testing.T, hits ...map[string]any) json.RawMessage {
	t.Helper()
	hh := make([]any, 0, len(hits))
	for _, src := range hits {
		hh = append(hh, map[string]any{"_source": src})
	}
	body := map[string]any{
		"hits": map[string]any{
			"total": map[string]any{"value": len(hits)},
			"hits":  hh,
		},
	}
	data, err := json.Marshal(body)
	require.NoError(t, err)
	return data
}

func TestParseEncMessagesResponse_ReadsCiphertextNotPlaintext(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	raw := encESResponse(t,
		map[string]any{
			"messageId":   "m1",
			"roomId":      "r1",
			"siteId":      "site-a",
			"userAccount": "alice",
			"contentEnc":  []byte("CIPHER1"),
			"encNonce":    []byte("NONCE1"),
			"createdAt":   created,
			// A stray plaintext content must be ignored — the enc hit has no such field.
			"content": "PLAINTEXT-LEAK",
		},
	)

	hits, total, err := parseEncMessagesResponse(raw)
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	require.Len(t, hits, 1)
	assert.Equal(t, "m1", hits[0].MessageID)
	assert.Equal(t, "r1", hits[0].RoomID)
	assert.Equal(t, []byte("CIPHER1"), hits[0].ContentEnc)
	assert.Equal(t, []byte("NONCE1"), hits[0].EncNonce)
}

func TestRetrieveContentA_FillsContentFromDecrypter(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	hits := []encMessageSearchHit{
		{MessageID: "m1", RoomID: "r1", SiteID: "site-a", UserAccount: "alice",
			ContentEnc: []byte("CT1"), EncNonce: []byte("N1"), CreatedAt: created},
		{MessageID: "m2", RoomID: "r2", SiteID: "site-a", UserAccount: "bob",
			ContentEnc: []byte("CT2"), EncNonce: []byte("N2"), CreatedAt: created},
	}
	d := &fakeDecrypter{byRoom: map[string]string{"r1": "hello", "r2": "world"}}

	out := retrieveContentA(context.Background(), d, hits)
	require.Len(t, out, 2)
	assert.Equal(t, "hello", out[0].Content)
	assert.Equal(t, "world", out[1].Content)
	assert.Equal(t, "m1", out[0].MessageID)
	assert.Equal(t, "r1", out[0].RoomID)

	// Decrypt was called with the per-hit ciphertext + nonce.
	require.Len(t, d.calls, 2)
	assert.Equal(t, "r1", d.calls[0].roomID)
	assert.Equal(t, []byte("CT1"), d.calls[0].payload)
	assert.Equal(t, []byte("N1"), d.calls[0].nonce)
}

func TestRetrieveContentA_DecryptErrorLeavesContentEmptyAndKeepsHit(t *testing.T) {
	hits := []encMessageSearchHit{
		{MessageID: "m1", RoomID: "r1", ContentEnc: []byte("CT1"), EncNonce: []byte("N1")},
		{MessageID: "m2", RoomID: "r2", ContentEnc: []byte("CT2"), EncNonce: []byte("N2")},
	}
	d := &fakeDecrypter{
		byRoom:    map[string]string{"r2": "world"},
		failRooms: map[string]bool{"r1": true},
	}

	out := retrieveContentA(context.Background(), d, hits)
	// The failing hit is kept, just with empty content; siblings unaffected.
	require.Len(t, out, 2)
	assert.Equal(t, "m1", out[0].MessageID)
	assert.Equal(t, "", out[0].Content, "decrypt failure must leave content empty, not drop the hit")
	assert.Equal(t, "world", out[1].Content)
}

func TestRetrieveContentA_Empty(t *testing.T) {
	d := &fakeDecrypter{}
	out := retrieveContentA(context.Background(), d, nil)
	assert.Empty(t, out)
	assert.NotNil(t, out, "must return a non-nil empty slice")
}
