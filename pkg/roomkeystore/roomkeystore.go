package roomkeystore

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrNoCurrentKey is returned by Rotate when no current key exists for the room.
var ErrNoCurrentKey = errors.New("no current key")

// RoomKeyPair holds the raw P-256 key bytes for a room.
type RoomKeyPair struct {
	PublicKey  []byte // 65-byte uncompressed point
	PrivateKey []byte // 32-byte scalar
}

// VersionedKeyPair pairs a key pair with its caller-assigned version identifier.
type VersionedKeyPair struct {
	VersionID string
	KeyPair   RoomKeyPair
}

// RoomKeyStore defines storage operations for room encryption key pairs.
type RoomKeyStore interface {
	Set(ctx context.Context, roomID string, versionID string, pair RoomKeyPair) error
	Get(ctx context.Context, roomID string) (*VersionedKeyPair, error)
	GetByVersion(ctx context.Context, roomID, versionID string) (*RoomKeyPair, error)
	Rotate(ctx context.Context, roomID string, versionID string, newPair RoomKeyPair) error
	Delete(ctx context.Context, roomID string) error
}

// Config holds Valkey connection and grace period configuration, parsed via caarlos0/env.
type Config struct {
	Addr        string        `env:"VALKEY_ADDR,required"`
	Password    string        `env:"VALKEY_PASSWORD" envDefault:""`
	GracePeriod time.Duration `env:"VALKEY_KEY_GRACE_PERIOD,required"`
}

// hashCommander is a minimal internal interface over the Valkey hash commands used by valkeyStore.
// Unexported and command-specific so unit tests can inject a fake without a live Valkey connection.
type hashCommander interface {
	hset(ctx context.Context, key string, pub, priv, ver string) error
	hgetall(ctx context.Context, key string) (map[string]string, error)
	rotatePipeline(ctx context.Context, currentKey, prevKey string, pub, priv, ver string, gracePeriod time.Duration) error
	deletePipeline(ctx context.Context, currentKey, prevKey string) error
}

// valkeyStore is the Valkey-backed implementation of RoomKeyStore.
type valkeyStore struct {
	client      hashCommander
	gracePeriod time.Duration
}

// redisAdapter wraps *redis.Client to satisfy hashCommander.
type redisAdapter struct {
	c *redis.Client
}

func (a *redisAdapter) hset(ctx context.Context, key string, pub, priv, ver string) error {
	return a.c.HSet(ctx, key, "pub", pub, "priv", priv, "ver", ver).Err()
}

func (a *redisAdapter) hgetall(ctx context.Context, key string) (map[string]string, error) {
	return a.c.HGetAll(ctx, key).Result()
}

func (a *redisAdapter) rotatePipeline(ctx context.Context, currentKey, prevKey string, pub, priv, ver string, gracePeriod time.Duration) error {
	pipe := a.c.Pipeline()
	// Read current key to copy to prev — we use HGetAll outside the pipeline,
	// then write both in a single pipeline for atomicity of the write side.
	current, err := a.c.HGetAll(ctx, currentKey).Result()
	if err != nil {
		return fmt.Errorf("read current key: %w", err)
	}
	if len(current) == 0 {
		return ErrNoCurrentKey
	}
	pipe.HSet(ctx, prevKey, "pub", current["pub"], "priv", current["priv"], "ver", current["ver"])
	pipe.Expire(ctx, prevKey, gracePeriod)
	pipe.HSet(ctx, currentKey, "pub", pub, "priv", priv, "ver", ver)
	_, err = pipe.Exec(ctx)
	return err
}

func (a *redisAdapter) deletePipeline(ctx context.Context, currentKey, prevKey string) error {
	pipe := a.c.Pipeline()
	pipe.Del(ctx, currentKey)
	pipe.Del(ctx, prevKey)
	_, err := pipe.Exec(ctx)
	return err
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
		return nil, fmt.Errorf("valkey connect: %w", err)
	}
	return &valkeyStore{client: &redisAdapter{c: c}, gracePeriod: cfg.GracePeriod}, nil
}

// roomkey returns the Valkey hash key for a room's current key pair.
func roomkey(roomID string) string {
	return "room:" + roomID + ":key"
}

// roomprevkey returns the Valkey hash key for a room's previous key pair.
func roomprevkey(roomID string) string {
	return "room:" + roomID + ":key:prev"
}

// Set stores pair in Valkey as a hash with no TTL.
// Does not touch the previous key slot.
func (s *valkeyStore) Set(ctx context.Context, roomID string, versionID string, pair RoomKeyPair) error {
	pub := base64.StdEncoding.EncodeToString(pair.PublicKey)
	priv := base64.StdEncoding.EncodeToString(pair.PrivateKey)
	key := roomkey(roomID)
	if err := s.client.hset(ctx, key, pub, priv, versionID); err != nil {
		return fmt.Errorf("set room key: %w", err)
	}
	return nil
}

// Get retrieves the current key pair for roomID. Returns (nil, nil) if the key does not exist.
func (s *valkeyStore) Get(ctx context.Context, roomID string) (*VersionedKeyPair, error) {
	fields, err := s.client.hgetall(ctx, roomkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get room key: %w", err)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	pair, err := decodeKeyPair(fields)
	if err != nil {
		return nil, fmt.Errorf("get room key: %w", err)
	}
	return &VersionedKeyPair{
		VersionID: fields["ver"],
		KeyPair:   *pair,
	}, nil
}

// GetByVersion retrieves the key pair matching versionID from either the current or previous slot.
// Returns (nil, nil) if neither matches or both are absent.
func (s *valkeyStore) GetByVersion(ctx context.Context, roomID, versionID string) (*RoomKeyPair, error) {
	// Check current key.
	currentFields, err := s.client.hgetall(ctx, roomkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get room key by version: %w", err)
	}
	if len(currentFields) > 0 && currentFields["ver"] == versionID {
		pair, err := decodeKeyPair(currentFields)
		if err != nil {
			return nil, fmt.Errorf("get room key by version: %w", err)
		}
		return pair, nil
	}

	// Check previous key.
	prevFields, err := s.client.hgetall(ctx, roomprevkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get room key by version: %w", err)
	}
	if len(prevFields) > 0 && prevFields["ver"] == versionID {
		pair, err := decodeKeyPair(prevFields)
		if err != nil {
			return nil, fmt.Errorf("get room key by version: %w", err)
		}
		return pair, nil
	}

	return nil, nil
}

// Rotate atomically moves the current key to the previous slot (with grace period TTL)
// and writes newPair as the current key. Returns ErrNoCurrentKey if no current key exists.
func (s *valkeyStore) Rotate(ctx context.Context, roomID string, versionID string, newPair RoomKeyPair) error {
	// Check if current key exists.
	currentFields, err := s.client.hgetall(ctx, roomkey(roomID))
	if err != nil {
		return fmt.Errorf("rotate room key: %w", err)
	}
	if len(currentFields) == 0 {
		return fmt.Errorf("rotate room key: %w", ErrNoCurrentKey)
	}

	pub := base64.StdEncoding.EncodeToString(newPair.PublicKey)
	priv := base64.StdEncoding.EncodeToString(newPair.PrivateKey)
	if err := s.client.rotatePipeline(ctx, roomkey(roomID), roomprevkey(roomID), pub, priv, versionID, s.gracePeriod); err != nil {
		return fmt.Errorf("rotate room key: %w", err)
	}
	return nil
}

// Delete removes both the current and previous key pairs for roomID.
// No-op if either or both are absent.
func (s *valkeyStore) Delete(ctx context.Context, roomID string) error {
	if err := s.client.deletePipeline(ctx, roomkey(roomID), roomprevkey(roomID)); err != nil {
		return fmt.Errorf("delete room key: %w", err)
	}
	return nil
}

// decodeKeyPair decodes base64-encoded pub and priv fields from a Valkey hash.
func decodeKeyPair(fields map[string]string) (*RoomKeyPair, error) {
	pub, err := base64.StdEncoding.DecodeString(fields["pub"])
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	priv, err := base64.StdEncoding.DecodeString(fields["priv"])
	if err != nil {
		return nil, fmt.Errorf("decode private key: %w", err)
	}
	return &RoomKeyPair{PublicKey: pub, PrivateKey: priv}, nil
}
