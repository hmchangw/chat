package msgkeystore

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// ErrNoCurrentKey is returned by Rotate when no current key exists for the room.
var ErrNoCurrentKey = errors.New("no current key")

// VersionedKey pairs a raw AES-256 key with its store-assigned version number.
type VersionedKey struct {
	Version int
	Key     []byte // 32-byte AES-256 key
}

// KeyStore defines storage operations for per-room DB encryption keys.
type KeyStore interface {
	Set(ctx context.Context, roomID string, key []byte) (int, error)
	Get(ctx context.Context, roomID string) (*VersionedKey, error)
	GetByVersion(ctx context.Context, roomID string, version int) ([]byte, error)
	Rotate(ctx context.Context, roomID string, newKey []byte) (int, error)
	Delete(ctx context.Context, roomID string) error
}

// Config holds Valkey connection and grace period configuration, parsed via caarlos0/env.
type Config struct {
	Addr        string        `env:"VALKEY_ADDR,required"`
	Password    string        `env:"VALKEY_PASSWORD" envDefault:""`
	GracePeriod time.Duration `env:"VALKEY_DBKEY_GRACE_PERIOD,required"`
}

// hashCommander is a minimal internal interface over the Valkey hash commands used by valkeyStore.
// Unexported and command-specific so unit tests can inject a fake without a live Valkey connection.
type hashCommander interface {
	hset(ctx context.Context, key string, val string) error
	hgetall(ctx context.Context, key string) (map[string]string, error)
	rotatePipeline(ctx context.Context, currentKey, prevKey string, val string, gracePeriod time.Duration) (int, error)
	deletePipeline(ctx context.Context, currentKey, prevKey string) error
}

// valkeyStore is the Valkey-backed implementation of KeyStore.
type valkeyStore struct {
	client      hashCommander
	gracePeriod time.Duration
}

// dbkey returns the Valkey hash key for a room's current DB encryption key.
func dbkey(roomID string) string {
	return "room:" + roomID + ":dbkey"
}

// dbprevkey returns the Valkey hash key for a room's previous DB encryption key.
func dbprevkey(roomID string) string {
	return "room:" + roomID + ":dbkey:prev"
}

// Set stores key in Valkey as a hash with no TTL, assigning version 0.
// Does not touch the previous key slot.
func (s *valkeyStore) Set(ctx context.Context, roomID string, key []byte) (int, error) {
	encoded := base64.StdEncoding.EncodeToString(key)
	valkeyKey := dbkey(roomID)
	if err := s.client.hset(ctx, valkeyKey, encoded); err != nil {
		return 0, fmt.Errorf("set db key: %w", err)
	}
	return 0, nil
}

// Get retrieves the current DB encryption key for roomID. Returns (nil, nil) if the key does not exist.
func (s *valkeyStore) Get(ctx context.Context, roomID string) (*VersionedKey, error) {
	fields, err := s.client.hgetall(ctx, dbkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get db key: %w", err)
	}
	if len(fields) == 0 {
		return nil, nil
	}
	ver, err := strconv.Atoi(fields["ver"])
	if err != nil {
		return nil, fmt.Errorf("get db key: parse version: %w", err)
	}
	decoded, err := decodeKey(fields)
	if err != nil {
		return nil, fmt.Errorf("get db key: %w", err)
	}
	return &VersionedKey{
		Version: ver,
		Key:     decoded,
	}, nil
}

// GetByVersion retrieves the key matching version from either the current or previous slot.
// Returns (nil, nil) if neither matches or both are absent.
func (s *valkeyStore) GetByVersion(ctx context.Context, roomID string, version int) ([]byte, error) {
	versionID := strconv.Itoa(version)

	// Check current key.
	currentFields, err := s.client.hgetall(ctx, dbkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get db key by version: %w", err)
	}
	if len(currentFields) > 0 && currentFields["ver"] == versionID {
		decoded, err := decodeKey(currentFields)
		if err != nil {
			return nil, fmt.Errorf("get db key by version: %w", err)
		}
		return decoded, nil
	}

	// Check previous key.
	prevFields, err := s.client.hgetall(ctx, dbprevkey(roomID))
	if err != nil {
		return nil, fmt.Errorf("get db key by version: %w", err)
	}
	if len(prevFields) > 0 && prevFields["ver"] == versionID {
		decoded, err := decodeKey(prevFields)
		if err != nil {
			return nil, fmt.Errorf("get db key by version: %w", err)
		}
		return decoded, nil
	}

	return nil, nil
}

// Rotate atomically moves the current key to the previous slot (with grace period TTL),
// increments the version, and writes newKey as the current key.
// Returns the new version number. Returns ErrNoCurrentKey if no current key exists.
func (s *valkeyStore) Rotate(ctx context.Context, roomID string, newKey []byte) (int, error) {
	encoded := base64.StdEncoding.EncodeToString(newKey)
	version, err := s.client.rotatePipeline(ctx, dbkey(roomID), dbprevkey(roomID), encoded, s.gracePeriod)
	if err != nil {
		return 0, fmt.Errorf("rotate db key: %w", err)
	}
	return version, nil
}

// Delete removes both the current and previous keys for roomID.
// No-op if either or both are absent.
func (s *valkeyStore) Delete(ctx context.Context, roomID string) error {
	if err := s.client.deletePipeline(ctx, dbkey(roomID), dbprevkey(roomID)); err != nil {
		return fmt.Errorf("delete db key: %w", err)
	}
	return nil
}

// decodeKey decodes a base64-encoded key field from a Valkey hash.
func decodeKey(fields map[string]string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(fields["key"])
	if err != nil {
		return nil, fmt.Errorf("decode key: %w", err)
	}
	return decoded, nil
}
