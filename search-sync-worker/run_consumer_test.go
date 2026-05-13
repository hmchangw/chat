package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/Marz32onE/instrumentation-go/otel-nats/oteljetstream"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/searchengine"
)

// fakeBatch satisfies oteljetstream.MessageBatch by streaming `msgs` over a
// closed channel.
type fakeBatch struct {
	msgs []oteljetstream.Msg
	err  error
}

func (b *fakeBatch) Messages() <-chan oteljetstream.Msg {
	ch := make(chan oteljetstream.Msg, len(b.msgs))
	for _, m := range b.msgs {
		ch <- m
	}
	close(ch)
	return ch
}
func (b *fakeBatch) Error() error { return b.err }

// fakeConsumer satisfies oteljetstream.Consumer; runConsumer only calls Fetch.
// The other methods panic so misuse surfaces loudly in tests.
type fakeConsumer struct {
	fetchCount atomic.Int64
	fetchFn    func(batch int) oteljetstream.MessageBatch
}

func (c *fakeConsumer) Fetch(batch int, _ ...jetstream.FetchOpt) (oteljetstream.MessageBatch, error) {
	c.fetchCount.Add(1)
	mb := c.fetchFn(batch)
	if mb == nil {
		return &fakeBatch{}, nil
	}
	return mb, nil
}
func (c *fakeConsumer) Consume(oteljetstream.MsgHandler, ...jetstream.PullConsumeOpt) (oteljetstream.ConsumeContext, error) {
	panic("not used")
}
func (c *fakeConsumer) Messages(...jetstream.PullMessagesOpt) (oteljetstream.MessagesContext, error) {
	panic("not used")
}
func (c *fakeConsumer) Next(context.Context, ...jetstream.FetchOpt) (context.Context, jetstream.Msg, error) {
	panic("not used")
}
func (c *fakeConsumer) FetchBytes(int, ...jetstream.FetchOpt) (oteljetstream.MessageBatch, error) {
	panic("not used")
}
func (c *fakeConsumer) FetchNoWait(int) (oteljetstream.MessageBatch, error) { panic("not used") }
func (c *fakeConsumer) Info(context.Context) (*oteljetstream.ConsumerInfo, error) {
	panic("not used")
}
func (c *fakeConsumer) CachedInfo() *oteljetstream.ConsumerInfo { panic("not used") }

// runConsumerEvent builds the canonical MessageEvent reused by every test in
// this file. Keep the shape consistent with handler_test.go's makeStubMsg.
func runConsumerEvent(msgID string) *model.MessageEvent {
	return &model.MessageEvent{
		Event: model.EventCreated,
		Message: model.Message{
			ID: msgID, RoomID: "r1", UserID: "u1", UserAccount: "alice",
			Content: "hi", CreatedAt: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC),
		},
		SiteID: "site-a", Timestamp: 100,
	}
}

func makeMessageStubMsg(t *testing.T, msgID string) oteljetstream.Msg {
	t.Helper()
	return oteljetstream.Msg{Msg: makeStubMsg(t, runConsumerEvent(msgID)), Ctx: context.Background()}
}

func TestRunConsumer_StopChDrainsAndExits(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	store.EXPECT().Bulk(gomock.Any(), gomock.Len(1)).Return([]searchengine.BulkResult{{Status: 201}}, nil)

	h := NewHandler(store, newMessageCollection("msgs-v1"), 500)
	h.Add(makeStubMsg(t, runConsumerEvent("m1")))
	require.Equal(t, 1, h.MessageCount())

	cons := &fakeConsumer{fetchFn: func(int) oteljetstream.MessageBatch { return &fakeBatch{} }}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})

	go runConsumer(context.Background(), cons, h, 10, 50, 5*time.Second, stopCh, doneCh)
	close(stopCh)

	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("runConsumer did not exit within 2s of stopCh close")
	}
	assert.Equal(t, 0, h.MessageCount(), "stopCh must drain the buffer before exit")
}

func TestRunConsumer_SizeBasedFlush(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	var flushedActions atomic.Int64
	store.EXPECT().Bulk(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, actions []searchengine.BulkAction) ([]searchengine.BulkResult, error) {
			flushedActions.Add(int64(len(actions)))
			results := make([]searchengine.BulkResult, len(actions))
			for i := range results {
				results[i] = searchengine.BulkResult{Status: 201}
			}
			return results, nil
		}).AnyTimes()

	const bulkCap = 3
	h := NewHandler(store, newMessageCollection("msgs-v1"), bulkCap)

	var served atomic.Int64
	cons := &fakeConsumer{fetchFn: func(_ int) oteljetstream.MessageBatch {
		if served.Add(1) > 1 {
			return &fakeBatch{}
		}
		return &fakeBatch{msgs: []oteljetstream.Msg{
			makeMessageStubMsg(t, "m1"),
			makeMessageStubMsg(t, "m2"),
			makeMessageStubMsg(t, "m3"),
		}}
	}}

	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	go runConsumer(context.Background(), cons, h, 10, bulkCap, time.Hour, stopCh, doneCh)

	require.Eventually(t, func() bool { return flushedActions.Load() >= bulkCap },
		2*time.Second, 20*time.Millisecond,
		"size-based flush must trigger once ActionCount >= bulkBatchSize")

	close(stopCh)
	<-doneCh
}

func TestRunConsumer_TimeBasedFlush(t *testing.T) {
	ctrl := gomock.NewController(t)
	store := NewMockStore(ctrl)
	var flushes atomic.Int64
	store.EXPECT().Bulk(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, actions []searchengine.BulkAction) ([]searchengine.BulkResult, error) {
			flushes.Add(1)
			results := make([]searchengine.BulkResult, len(actions))
			for i := range results {
				results[i] = searchengine.BulkResult{Status: 201}
			}
			return results, nil
		}).AnyTimes()

	h := NewHandler(store, newMessageCollection("msgs-v1"), 500)

	var served atomic.Int64
	cons := &fakeConsumer{fetchFn: func(_ int) oteljetstream.MessageBatch {
		if served.Add(1) == 1 {
			return &fakeBatch{msgs: []oteljetstream.Msg{makeMessageStubMsg(t, "m1")}}
		}
		// Subsequent fetches return an error so the time-based branch is reached.
		return &errBatch{}
	}}

	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	// 200ms flush interval + 5s Eventually budget tolerates race-detector
	// overhead and CI scheduler jitter under load.
	go runConsumer(context.Background(), cons, h, 10, 500, 200*time.Millisecond, stopCh, doneCh)

	require.Eventually(t, func() bool { return flushes.Load() >= 1 },
		5*time.Second, 50*time.Millisecond,
		"time-based flush must fire after bulkFlushInterval with a non-empty buffer")

	close(stopCh)
	<-doneCh
}

type errBatch struct{}

func (b *errBatch) Messages() <-chan oteljetstream.Msg {
	ch := make(chan oteljetstream.Msg)
	close(ch)
	return ch
}
func (b *errBatch) Error() error { return errors.New("fetch error") }
