package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomkeysender"
)

// barrierPublisher blocks every Publish call on a shared barrier so a test can
// observe how many goroutines reach Publish concurrently before any are
// allowed to finish.
type barrierPublisher struct {
	barrier chan struct{}
	arrived chan struct{}
	count   atomic.Int32
}

func newBarrierPublisher(buffer int) *barrierPublisher {
	return &barrierPublisher{
		barrier: make(chan struct{}),
		arrived: make(chan struct{}, buffer),
	}
}

func (b *barrierPublisher) Publish(_ string, _ []byte) error {
	b.count.Add(1)
	b.arrived <- struct{}{}
	<-b.barrier
	return nil
}

func TestFanOutKey_RespectsWorkerCap(t *testing.T) {
	const accounts = 8
	const workers = 3

	bp := newBarrierPublisher(accounts)
	h := &Handler{
		keySender:        roomkeysender.NewSender(bp),
		keyFanoutWorkers: workers,
	}

	accts := make([]string, accounts)
	for i := range accts {
		accts[i] = fmt.Sprintf("u-%d", i)
	}
	evt := model.RoomKeyEvent{RoomID: "r", Version: 1}

	done := make(chan struct{})
	go func() {
		h.fanOutKey(context.Background(), "r", accts, &evt)
		close(done)
	}()

	// Exactly `workers` goroutines should reach the publisher before any
	// finish; the rest should be queued on the semaphore.
	for i := 0; i < workers; i++ {
		select {
		case <-bp.arrived:
		case <-time.After(time.Second):
			t.Fatalf("only %d goroutines reached Publish; fanout is not concurrent", i)
		}
	}
	select {
	case <-bp.arrived:
		t.Fatalf("more than %d goroutines reached Publish; worker cap not enforced", workers)
	case <-time.After(50 * time.Millisecond):
	}
	assert.Equal(t, int32(workers), bp.count.Load())

	close(bp.barrier)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fanout did not complete after releasing the barrier")
	}
	assert.Equal(t, int32(accounts), bp.count.Load(), "every account must have been published exactly once")
}

// recordingPublisher just records the accounts it sees, no blocking.
type recordingPublisher struct {
	mu       sync.Mutex
	subjects []string
}

func (r *recordingPublisher) Publish(subj string, _ []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subjects = append(r.subjects, subj)
	return nil
}

func (r *recordingPublisher) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.subjects))
	copy(out, r.subjects)
	return out
}

func TestFanOutKey_PublishesEveryAccount(t *testing.T) {
	const accounts = 100

	rp := &recordingPublisher{}
	h := &Handler{
		keySender:        roomkeysender.NewSender(rp),
		keyFanoutWorkers: 16,
	}
	accts := make([]string, accounts)
	for i := range accts {
		accts[i] = fmt.Sprintf("acct-%03d", i)
	}
	evt := model.RoomKeyEvent{RoomID: "r"}
	h.fanOutKey(context.Background(), "r", accts, &evt)

	got := rp.snapshot()
	require.Len(t, got, accounts, "must publish once per account")
}

func TestFanOutKey_NoAccountsIsNoOp(t *testing.T) {
	rp := &recordingPublisher{}
	h := &Handler{
		keySender:        roomkeysender.NewSender(rp),
		keyFanoutWorkers: 16,
	}
	h.fanOutKey(context.Background(), "r", nil, &model.RoomKeyEvent{})
	assert.Empty(t, rp.snapshot())
}

func TestFanOutKey_WorkersDefaultWhenZero(t *testing.T) {
	rp := &recordingPublisher{}
	h := &Handler{
		keySender:        roomkeysender.NewSender(rp),
		keyFanoutWorkers: 0, // unset → must fall back to default, not deadlock
	}
	h.fanOutKey(context.Background(), "r", []string{"a", "b", "c"}, &model.RoomKeyEvent{})
	assert.Len(t, rp.snapshot(), 3)
}
