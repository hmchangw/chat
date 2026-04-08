package debounce

import (
	"context"
	"fmt"
	"time"
)

// ClaimedEntry represents a key claimed by the poll loop.
type ClaimedEntry struct {
	Key          string
	ClaimedScore float64
}

// Adapter abstracts the sorted-set operations the Debouncer needs.
type Adapter interface {
	Trigger(ctx context.Context, key string, deadline time.Time) error
	Claim(ctx context.Context, now time.Time, processingTimeout time.Duration, batchSize int) ([]ClaimedEntry, error)
	Remove(ctx context.Context, key string, claimedScore float64) error
	Requeue(ctx context.Context, key string, deadline time.Time) error
}

// sortedSetCommander is a minimal internal interface over the Valkey sorted-set
// commands used by valkeyAdapter. Unexported so unit tests can inject a fake
// without a live Valkey connection.
type sortedSetCommander interface {
	zadd(ctx context.Context, member string, score float64) error
	claimExpired(ctx context.Context, now float64, processingDeadline float64, batchSize int) ([]string, error)
	removeIfScore(ctx context.Context, member string, expectedScore float64) error
}

// valkeyAdapter implements Adapter using a sortedSetCommander.
type valkeyAdapter struct {
	client sortedSetCommander
}

func (a *valkeyAdapter) Trigger(ctx context.Context, key string, deadline time.Time) error {
	deadlineMs := float64(deadline.UnixMilli())
	if err := a.client.zadd(ctx, key, deadlineMs); err != nil {
		return fmt.Errorf("set debounce deadline: %w", err)
	}
	return nil
}

func (a *valkeyAdapter) Claim(ctx context.Context, now time.Time, processingTimeout time.Duration, batchSize int) ([]ClaimedEntry, error) {
	nowMs := float64(now.UnixMilli())
	deadlineMs := float64(now.Add(processingTimeout).UnixMilli())

	members, err := a.client.claimExpired(ctx, nowMs, deadlineMs, batchSize)
	if err != nil {
		return nil, fmt.Errorf("claim expired entries: %w", err)
	}

	if len(members) == 0 {
		return nil, nil
	}

	entries := make([]ClaimedEntry, len(members))
	for i, m := range members {
		entries[i] = ClaimedEntry{
			Key:          m,
			ClaimedScore: deadlineMs,
		}
	}
	return entries, nil
}

func (a *valkeyAdapter) Remove(ctx context.Context, key string, claimedScore float64) error {
	if err := a.client.removeIfScore(ctx, key, claimedScore); err != nil {
		return fmt.Errorf("conditional remove: %w", err)
	}
	return nil
}

func (a *valkeyAdapter) Requeue(ctx context.Context, key string, deadline time.Time) error {
	deadlineMs := float64(deadline.UnixMilli())
	if err := a.client.zadd(ctx, key, deadlineMs); err != nil {
		return fmt.Errorf("requeue debounce entry: %w", err)
	}
	return nil
}
