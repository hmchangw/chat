package debounce

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
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
	Close() error
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
	closer func() error // closes the underlying connection; nil if not applicable
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

func (a *valkeyAdapter) Close() error {
	return a.closer()
}

// --- Redis/Valkey implementation of sortedSetCommander ---

// redisAdapter wraps *redis.Client to satisfy sortedSetCommander.
type redisAdapter struct {
	c   *redis.Client
	key string // sorted set key name
}

func (a *redisAdapter) zadd(ctx context.Context, member string, score float64) error {
	return a.c.ZAdd(ctx, a.key, redis.Z{Score: score, Member: member}).Err()
}

// claimScript atomically finds expired entries, updates their scores to a
// processing deadline, and returns them as a flat list of member names.
// Runs as a single Lua script so no other client can interleave.
//
// ZRANGEBYSCORE with LIMIT is O(log(N)+M) where N is the set size and M is
// batchSize. After a long outage many entries may expire; the batch limit
// prevents claiming too many at once.
var claimScript = redis.NewScript(`
local key = KEYS[1]
local batchSize = tonumber(ARGV[3])

local entries = redis.call('ZRANGEBYSCORE', key, '-inf', ARGV[1], 'LIMIT', 0, batchSize)
if #entries == 0 then
    return {}
end
for _, member in ipairs(entries) do
    redis.call('ZADD', key, ARGV[2], member)
end
return entries
`)

func (a *redisAdapter) claimExpired(ctx context.Context, now float64, processingDeadline float64, batchSize int) ([]string, error) {
	raw, err := claimScript.Run(ctx, a.c, []string{a.key}, now, processingDeadline, batchSize).StringSlice()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	return raw, nil
}

// removeScript conditionally removes a member only if its current score
// matches the expected score. Prevents removing a key that was re-triggered
// during processing.
//
// The tonumber() comparison is safe because scores are always integer
// millisecond timestamps (via time.UnixMilli), which are within the
// safe integer range for IEEE 754 float64 (up to 2^53).
var removeScript = redis.NewScript(`
local key = KEYS[1]
local member = ARGV[1]
local expectedScore = ARGV[2]

local score = redis.call('ZSCORE', key, member)
if score and tonumber(score) == tonumber(expectedScore) then
    redis.call('ZREM', key, member)
end
return 0
`)

func (a *redisAdapter) removeIfScore(ctx context.Context, member string, expectedScore float64) error {
	return removeScript.Run(ctx, a.c, []string{a.key}, member, expectedScore).Err()
}

// AdapterConfig holds Valkey connection configuration for the debounce adapter.
type AdapterConfig struct {
	Addr     string
	Password string
	Key      string // Valkey sorted set key name, e.g., "debounce:room-key-rotation"
}

// NewValkeyAdapter creates an Adapter backed by Valkey, pings to verify connectivity.
func NewValkeyAdapter(cfg AdapterConfig) (Adapter, error) {
	c := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("valkey connect: %w", err)
	}
	return &valkeyAdapter{
		client: &redisAdapter{c: c, key: cfg.Key},
		closer: c.Close,
	}, nil
}
