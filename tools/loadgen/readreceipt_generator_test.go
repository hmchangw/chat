package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeReadReceiptRequester records calls and returns a canned reply/error.
type fakeReadReceiptRequester struct {
	mu    sync.Mutex
	calls int
	reply []byte
	err   error
}

func (f *fakeReadReceiptRequester) Request(_ context.Context, _ string, _ []byte, _ time.Duration) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.reply, f.err
}

func (f *fakeReadReceiptRequester) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestDeriveReadReceiptTargets_TopLevelOnly(t *testing.T) {
	plan := &MessagePlan{Messages: []plannedMessage{
		{RoomID: "r1", MessageID: "m1", SenderAccount: "user-1", ThreadParentID: ""},
		{RoomID: "r1", MessageID: "m2", SenderAccount: "user-2", ThreadParentID: "m1"},                        // reply → excluded
		{RoomID: "r2", MessageID: "m3", SenderAccount: "user-3", ThreadParentID: "", ThreadRoomID: "tr-r2-1"}, // thread parent → included
	}}
	targets := deriveReadReceiptTargets(plan)
	require.Len(t, targets, 2)
	assert.Equal(t, readReceiptTarget{Account: "user-1", RoomID: "r1", MessageID: "m1"}, targets[0])
	assert.Equal(t, readReceiptTarget{Account: "user-3", RoomID: "r2", MessageID: "m3"}, targets[1])
}

func TestReadReceiptGenerator_RateAndReplies(t *testing.T) {
	req := &fakeReadReceiptRequester{reply: []byte(`{"readers":[]}`)}
	collector := NewReadReceiptCollector()
	gen := NewReadReceiptGenerator(&ReadReceiptGeneratorConfig{
		Targets:        []readReceiptTarget{{Account: "user-1", RoomID: "r1", MessageID: "m1"}},
		SiteID:         "site-test",
		Rate:           200,
		RequestTimeout: time.Second,
		Requester:      req,
		Collector:      collector,
		MaxInFlight:    16,
	}, 42)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))

	assert.Positive(t, req.count(), "generator issued zero requests")
	assert.NotEmpty(t, collector.Samples(), "collector recorded zero samples")
}

func TestReadReceiptGenerator_RejectsNonPositiveRate(t *testing.T) {
	gen := NewReadReceiptGenerator(&ReadReceiptGeneratorConfig{
		Targets:   []readReceiptTarget{{Account: "user-1", RoomID: "r1", MessageID: "m1"}},
		Rate:      0,
		Requester: &fakeReadReceiptRequester{},
		Collector: NewReadReceiptCollector(),
	}, 42)
	assert.Error(t, gen.Run(context.Background()))
}

func TestReadReceiptGenerator_RecordsReplyError(t *testing.T) {
	req := &fakeReadReceiptRequester{err: context.DeadlineExceeded}
	collector := NewReadReceiptCollector()
	gen := NewReadReceiptGenerator(&ReadReceiptGeneratorConfig{
		Targets:        []readReceiptTarget{{Account: "user-1", RoomID: "r1", MessageID: "m1"}},
		SiteID:         "site-test",
		Rate:           200,
		RequestTimeout: time.Second,
		Requester:      req,
		Collector:      collector,
		MaxInFlight:    0, // serial — deterministic single error path
	}, 42)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	require.NoError(t, gen.Run(ctx))
	assert.Positive(t, collector.Failed(), "deadline-exceeded replies should be tallied as failures")
	assert.Empty(t, collector.Samples())
}
