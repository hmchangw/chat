package roomkeystore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RoomKeyPair holds the raw P-256 key bytes for a room.
type RoomKeyPair struct {
	PublicKey  []byte // 65-byte uncompressed point
	PrivateKey []byte // 32-byte scalar
}

// RoomKeyStore defines storage operations for room encryption key pairs.
type RoomKeyStore interface {
	Set(ctx context.Context, roomID string, pair RoomKeyPair) error
	Get(ctx context.Context, roomID string) (*RoomKeyPair, error)
	Delete(ctx context.Context, roomID string) error
}

// Config holds Valkey connection and TTL configuration, parsed via caarlos0/env.
type Config struct {
	Addr     string        `env:"VALKEY_ADDR,required"`
	Password string        `env:"VALKEY_PASSWORD" envDefault:""`
	KeyTTL   time.Duration `env:"VALKEY_KEY_TTL,required"`
}

// hashCommander is a minimal internal interface over the Valkey hash commands used by valkeyStore.
// Unexported and command-specific so unit tests can inject a fake without a live Valkey connection.
type hashCommander interface {
	hset(ctx context.Context, key string, pub, priv string) error
	expire(ctx context.Context, key string, ttl time.Duration) error
	hgetall(ctx context.Context, key string) (map[string]string, error)
	del(ctx context.Context, key string) error
}

// valkeyStore is the Valkey-backed implementation of RoomKeyStore.
type valkeyStore struct {
	client hashCommander
	ttl    time.Duration
}

// redisAdapter wraps *redis.Client to satisfy hashCommander.
type redisAdapter struct {
	c *redis.Client
}

func (a *redisAdapter) hset(ctx context.Context, key string, pub, priv string) error {
	return a.c.HSet(ctx, key, "pub", pub, "priv", priv).Err()
}

func (a *redisAdapter) expire(ctx context.Context, key string, ttl time.Duration) error {
	return a.c.Expire(ctx, key, ttl).Err()
}

func (a *redisAdapter) hgetall(ctx context.Context, key string) (map[string]string, error) {
	return a.c.HGetAll(ctx, key).Result()
}

func (a *redisAdapter) del(ctx context.Context, key string) error {
	return a.c.Del(ctx, key).Err()
}

// NewValkeyStore creates a valkeyStore, pings Valkey to verify connectivity, and returns it.
func NewValkeyStore(cfg Config) (*valkeyStore, error) {
	c := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("valkey connect: %w", err)
	}
	return &valkeyStore{client: &redisAdapter{c: c}, ttl: cfg.KeyTTL}, nil
}

// roomkey returns the Valkey hash key for a room's key pair.
func roomkey(roomID string) string {
	return "room:" + roomID + ":key"
}

// Set stores pair in Valkey and (re)sets the TTL on the hash key.
func (s *valkeyStore) Set(_ context.Context, _ string, _ RoomKeyPair) error {
	return errors.New("not implemented")
}

// Get retrieves the key pair for roomID. Returns (nil, nil) if the key does not exist.
func (s *valkeyStore) Get(_ context.Context, _ string) (*RoomKeyPair, error) {
	return nil, errors.New("not implemented")
}

// Delete removes the key pair for roomID. No-op if the key does not exist.
func (s *valkeyStore) Delete(_ context.Context, _ string) error {
	return errors.New("not implemented")
}
