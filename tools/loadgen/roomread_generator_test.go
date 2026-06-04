package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/idgen"
	"github.com/hmchangw/chat/pkg/natsutil"
)

// fakeRoomReadRequester records every subject it is asked to request (and the
// X-Request-ID carried on the call's context) and returns a configurable
// reply/error.
type fakeRoomReadRequester struct {
	mu       sync.Mutex
	subjects []string
	reqIDs   []string
	reply    []byte
	err      error
}

func (f *fakeRoomReadRequester) Request(ctx context.Context, subj string, _ []byte, _ time.Duration) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subjects = append(f.subjects, subj)
	f.reqIDs = append(f.reqIDs, natsutil.RequestIDFromContext(ctx))
	if f.err != nil {
		return nil, f.err
	}
	return f.reply, nil
}

func (f *fakeRoomReadRequester) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.subjects))
	copy(out, f.subjects)
	return out
}

func (f *fakeRoomReadRequester) recordedReqIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.reqIDs))
	copy(out, f.reqIDs)
	return out
}

func newRoomReadTestGen(t *testing.T, req RoomReadRequester, c *RoomReadCollector) *roomReadGenerator {
	t.Helper()
	p, ok := BuiltinPreset("small")
	require.True(t, ok)
	f := BuildRoomReadFixtures(&p, 42, "site-test", time.Now().UTC())
	return newRoomReadGenerator(&roomReadGeneratorConfig{
		Fixtures:       &f,
		SiteID:         "site-test",
		Rate:           200,
		RequestTimeout: time.Second,
		Requester:      req,
		Collector:      c,
		MaxInFlight:    8,
	}, 42)
}

func TestRoomReadGenerator_EmitsMessageReadSubjects(t *testing.T) {
	req := &fakeRoomReadRequester{reply: []byte(`{"status":"accepted"}`)}
	c := NewRoomReadCollector()
	gen := newRoomReadTestGen(t, req, c)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))

	subs := req.recorded()
	require.NotEmpty(t, subs, "generator issued no requests")
	for _, s := range subs {
		assert.True(t, strings.HasPrefix(s, "chat.user."), "unexpected subject %q", s)
		assert.True(t, strings.HasSuffix(s, ".message.read"), "unexpected subject %q", s)
		assert.Contains(t, s, ".request.room.", "subject must use the message.read request path: %q", s)
	}
	assert.NotEmpty(t, c.Samples(), "accepted replies should be recorded as samples")
}

func TestRoomReadGenerator_BadReplyRecorded(t *testing.T) {
	req := &fakeRoomReadRequester{reply: []byte(`{"status":"nope"}`)}
	c := NewRoomReadCollector()
	gen := newRoomReadTestGen(t, req, c)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))

	assert.Empty(t, c.Samples(), "non-accepted replies must not count as samples")
	assert.Greater(t, c.BadReplyCount(), 0, "non-accepted replies must count as bad replies")
}

func TestRoomReadGenerator_TimeoutRecorded(t *testing.T) {
	req := &fakeRoomReadRequester{err: context.DeadlineExceeded}
	c := NewRoomReadCollector()
	gen := newRoomReadTestGen(t, req, c)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))

	assert.Greater(t, c.TimeoutErrors(), 0, "DeadlineExceeded must count as a timeout")
}

func TestRoomReadGenerator_RequiresPositiveRate(t *testing.T) {
	req := &fakeRoomReadRequester{reply: []byte(`{"status":"accepted"}`)}
	c := NewRoomReadCollector()
	gen := newRoomReadTestGen(t, req, c)
	gen.cfg.Rate = 0
	assert.Error(t, gen.Run(context.Background()))
}

func TestRoomReadGenerator_EmptyFixturesNoPanic(t *testing.T) {
	req := &fakeRoomReadRequester{reply: []byte(`{"status":"accepted"}`)}
	c := NewRoomReadCollector()
	empty := Fixtures{} // no rooms, no subscriptions
	gen := newRoomReadGenerator(&roomReadGeneratorConfig{
		Fixtures:       &empty,
		SiteID:         "site-test",
		Rate:           200,
		RequestTimeout: time.Second,
		Requester:      req,
		Collector:      c,
		MaxInFlight:    8,
	}, 42)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))

	assert.Empty(t, req.recorded(), "no rooms means no requests should be issued")
	assert.Empty(t, c.Samples())
}

func TestRoomReadGenerator_CarriesRequestID(t *testing.T) {
	req := &fakeRoomReadRequester{reply: []byte(`{"status":"accepted"}`)}
	c := NewRoomReadCollector()
	gen := newRoomReadTestGen(t, req, c)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))

	ids := req.recordedReqIDs()
	require.NotEmpty(t, ids, "generator issued no requests")
	seen := map[string]bool{}
	for _, id := range ids {
		assert.NotEmpty(t, id, "every request must carry an X-Request-ID")
		assert.True(t, idgen.IsValidUUID(id), "request ID %q must be a valid UUID", id)
		assert.False(t, seen[id], "each request must mint a fresh X-Request-ID, got duplicate %q", id)
		seen[id] = true
	}
}
