package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
)

// fakeSearchRequester returns canned SearchMessagesResponse payloads keyed by
// query text, simulating an ES index that starts empty and gains hits later.
type fakeSearchRequester struct {
	// hits maps query text -> sequence of responses to serve in order. When the
	// sequence is exhausted, the last response is replayed indefinitely.
	hits map[string][]model.SearchMessagesResponse
	// calls records each request for assertion in tests.
	calls []fakeSearchCall
	// err, when non-nil, is returned for every call (overrides hits).
	err error
}

type fakeSearchCall struct {
	subject string
	req     model.SearchMessagesRequest
}

func (f *fakeSearchRequester) Request(_ context.Context, subj string, data []byte, _ time.Duration) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	var req model.SearchMessagesRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, err
	}
	f.calls = append(f.calls, fakeSearchCall{subject: subj, req: req})

	seq := f.hits[req.Query]
	if len(seq) == 0 {
		empty, _ := json.Marshal(model.SearchMessagesResponse{Messages: []model.SearchMessage{}})
		return empty, nil
	}
	// Pop from the front if there's still a queue; otherwise replay the tail.
	var resp model.SearchMessagesResponse
	if len(seq) > 1 {
		resp = seq[0]
		f.hits[req.Query] = seq[1:]
	} else {
		resp = seq[0]
	}
	return json.Marshal(resp)
}

func TestSearchIndexLookup_NotVisibleWhenResponseEmpty(t *testing.T) {
	r := &fakeSearchRequester{hits: map[string][]model.SearchMessagesResponse{}}
	lookup := newSearchIndexLookup(r, "alice", "room-1", "tok-abc", "msg-xyz", 1*time.Second)
	err := lookup(context.Background(), "msg-xyz")
	assert.ErrorIs(t, err, ErrRAWNotVisible)
}

func TestSearchIndexLookup_VisibleWhenMessageIDMatches(t *testing.T) {
	r := &fakeSearchRequester{
		hits: map[string][]model.SearchMessagesResponse{
			"tok-abc": {{Messages: []model.SearchMessage{{MessageID: "msg-xyz"}}, Total: 1}},
		},
	}
	lookup := newSearchIndexLookup(r, "alice", "room-1", "tok-abc", "msg-xyz", 1*time.Second)
	err := lookup(context.Background(), "msg-xyz")
	assert.NoError(t, err)
}

func TestSearchIndexLookup_NotVisibleWhenMessageIDMismatch(t *testing.T) {
	// search-service returns hits, but none of them are ours (token collision
	// with another room's message). Treat as not-yet-visible.
	r := &fakeSearchRequester{
		hits: map[string][]model.SearchMessagesResponse{
			"tok-abc": {{Messages: []model.SearchMessage{{MessageID: "msg-other"}}, Total: 1}},
		},
	}
	lookup := newSearchIndexLookup(r, "alice", "room-1", "tok-abc", "msg-xyz", 1*time.Second)
	err := lookup(context.Background(), "msg-xyz")
	assert.ErrorIs(t, err, ErrRAWNotVisible)
}

func TestSearchIndexLookup_ScopesQueryByRoomAndUser(t *testing.T) {
	r := &fakeSearchRequester{hits: map[string][]model.SearchMessagesResponse{}}
	lookup := newSearchIndexLookup(r, "alice", "room-7", "tok-9", "msg-1", 1*time.Second)
	_ = lookup(context.Background(), "msg-1")

	require.Len(t, r.calls, 1)
	call := r.calls[0]
	assert.Contains(t, call.subject, "alice", "subject must target the per-user search subject")
	assert.Equal(t, "tok-9", call.req.Query, "query must be the unique token to keep ES selectivity high")
	assert.Equal(t, []string{"room-7"}, call.req.RoomIDs, "lookup must scope by roomID to bound search-service work")
}

func TestSearchIndexLookup_TransportErrorPropagates(t *testing.T) {
	r := &fakeSearchRequester{err: errors.New("nats timeout")}
	lookup := newSearchIndexLookup(r, "alice", "room-1", "tok-abc", "msg-xyz", 1*time.Second)
	err := lookup(context.Background(), "msg-xyz")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrRAWNotVisible,
		"transport errors must surface as themselves so the operator distinguishes ES-not-yet-indexed from broken plumbing")
}

func TestSearchIndexToken_DeterministicAndSearchable(t *testing.T) {
	// Token derivation must be deterministic per messageID so a publisher and
	// a poller using the same msgID compute the same token without sharing state.
	tok1 := searchIndexToken("msg-abc")
	tok2 := searchIndexToken("msg-abc")
	assert.Equal(t, tok1, tok2)

	// Different messages get different tokens; otherwise concurrent runs would
	// false-positive on each other's docs.
	assert.NotEqual(t, tok1, searchIndexToken("msg-def"))

	// Token must consist solely of characters the ES default analyzer treats
	// as a single token (alphanumeric, lowercase). Anything else risks ES
	// splitting it and the search returning false negatives.
	for _, r := range tok1 {
		isAlnumLower := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		assert.True(t, isAlnumLower, "token char %q must be lowercase alphanumeric", r)
	}
	assert.GreaterOrEqual(t, len(tok1), 8, "token must be long enough to make collisions across a run vanishingly unlikely")
}

func TestSearchSyncBuildMessageContent_EmbedsToken(t *testing.T) {
	got := searchSyncMessageContent("msg-abc")
	assert.True(t, strings.Contains(got, searchIndexToken("msg-abc")),
		"published message content must contain the lookup token so ES is the one source of truth for visibility")
}

func TestSearchSyncScenario_Registered(t *testing.T) {
	sc, ok := LookupScenario("search-sync-lag")
	require.True(t, ok)
	assert.Equal(t, "search-sync-lag", sc.Name())
	_, isFactory := sc.(GeneratorFactory)
	assert.True(t, isFactory)
}
