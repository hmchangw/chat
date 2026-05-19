package roomkeystore

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// universalAdapter wraps redis.UniversalClient (implemented by both
// *redis.Client and *redis.ClusterClient) to satisfy hashCommander.
type universalAdapter struct {
	c redis.UniversalClient
}

func (a *universalAdapter) hset(ctx context.Context, key string, pub, priv string) error {
	return a.c.HSet(ctx, key, "pub", pub, "priv", priv, "ver", "0").Err()
}

func (a *universalAdapter) hsetWithVersion(ctx context.Context, key string, pub, priv string, version int) error {
	return a.c.HSet(ctx, key, "pub", pub, "priv", priv, "ver", strconv.Itoa(version)).Err()
}

func (a *universalAdapter) hgetall(ctx context.Context, key string) (map[string]string, error) {
	return a.c.HGetAll(ctx, key).Result()
}

// rotateScript atomically reads the current key, copies it to the previous
// slot with a grace-period TTL, increments the version, and writes the new key as current.
// Returns the new version number.
// This runs as a single Lua script so no other client can interleave.
var rotateScript = redis.NewScript(`
local currentKey = KEYS[1]
local prevKey    = KEYS[2]
local newPub     = ARGV[1]
local newPriv    = ARGV[2]
local graceSec   = tonumber(ARGV[3])

local cur = redis.call('HGETALL', currentKey)
if #cur == 0 then
    return redis.error_reply('no current key')
end

local curVer = tonumber(redis.call('HGET', currentKey, 'ver')) or 0
local newVer = curVer + 1

redis.call('DEL', prevKey)
redis.call('HSET', prevKey, unpack(cur))
redis.call('EXPIRE', prevKey, graceSec)

redis.call('HSET', currentKey, 'pub', newPub, 'priv', newPriv, 'ver', tostring(newVer))
return newVer
`)

func (a *universalAdapter) rotatePipeline(ctx context.Context, currentKey, prevKey string, pub, priv string, gracePeriod time.Duration) (int, error) {
	graceSec := int(gracePeriod.Seconds())
	if graceSec < 1 {
		graceSec = 1
	}
	result, err := rotateScript.Run(ctx, a.c, []string{currentKey, prevKey}, pub, priv, graceSec).Int()
	if err != nil && strings.Contains(err.Error(), "no current key") {
		return 0, ErrNoCurrentKey
	}
	return result, err
}

func (a *universalAdapter) deletePipeline(ctx context.Context, currentKey, prevKey string) error {
	return a.c.Del(ctx, currentKey, prevKey).Err()
}

// hgetallMany issues HGETALL for every key in a single pipeline and returns
// one map per input key (in the same order). A missing hash yields an empty
// map rather than an error, matching go-redis v9 HGetAll semantics.
func (a *universalAdapter) hgetallMany(ctx context.Context, keys []string) ([]map[string]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	pipe := a.c.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(keys))
	for i, k := range keys {
		cmds[i] = pipe.HGetAll(ctx, k)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, err
	}
	out := make([]map[string]string, len(keys))
	for i, c := range cmds {
		m, err := c.Result()
		if err != nil {
			return nil, err
		}
		out[i] = m
	}
	return out, nil
}

func (a *universalAdapter) closeClient() error {
	return a.c.Close()
}

// NewValkeyStore creates a valkeyStore, pings Valkey to verify connectivity, and returns it.
func NewValkeyStore(cfg Config) (RoomKeyStore, error) {
	c := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		if closeErr := c.Close(); closeErr != nil {
			slog.Warn("valkey close after failed connect", "error", closeErr)
		}
		return nil, fmt.Errorf("valkey connect: %w", err)
	}
	return &valkeyStore{client: &universalAdapter{c: c}, closer: c, gracePeriod: cfg.GracePeriod}, nil
}

// ClusterConfig holds connection config for a Valkey cluster deployment.
// Addrs is a list of seed node addresses; go-redis ClusterClient discovers
// all nodes automatically via CLUSTER SLOTS. One address is sufficient but
// listing all masters is more robust against seed-node downtime at connect time.
type ClusterConfig struct {
	Addrs       []string
	Password    string
	GracePeriod time.Duration
}

// NewValkeyClusterStore creates a valkeyStore backed by a Valkey cluster,
// pings the cluster to verify connectivity, and returns it.
// The cluster is discovered automatically from the seed addresses in cfg.Addrs.
func NewValkeyClusterStore(cfg ClusterConfig) (RoomKeyStore, error) {
	c := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:    cfg.Addrs,
		Password: cfg.Password,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		if closeErr := c.Close(); closeErr != nil {
			slog.Warn("valkey cluster close after failed connect", "error", closeErr)
		}
		return nil, fmt.Errorf("valkey cluster connect: %w", err)
	}
	return &valkeyStore{client: &universalAdapter{c: c}, closer: c, gracePeriod: cfg.GracePeriod}, nil
}
