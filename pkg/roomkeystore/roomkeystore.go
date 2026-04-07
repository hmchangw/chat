package roomkeystore

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

// RoomKeyPair holds the raw P-256 key bytes for a room.
type RoomKeyPair struct {
	PublicKey  []byte // 65-byte uncompressed point
	PrivateKey []byte // 32-byte scalar
}

// VersionedKeyPair pairs a key pair with its store-assigned version number.
type VersionedKeyPair struct {
	Version int
	KeyPair RoomKeyPair
}

// RoomKeyStore defines storage operations for room encryption key pairs.
type RoomKeyStore interface {
	Set(ctx context.Context, roomID string, pair RoomKeyPair) (int, error)
	Get(ctx context.Context, roomID string) (*VersionedKeyPair, error)
	GetByVersion(ctx context.Context, roomID string, version int) (*RoomKeyPair, error)
	Rotate(ctx context.Context, roomID string, newPair RoomKeyPair) (int, error)
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
	hset(ctx context.Context, key string, pub, priv string) error
	hgetall(ctx context.Context, key string) (map[string]string, error)
	rotatePipeline(ctx context.Context, currentKey, prevKey string, pub, priv string, gracePeriod time.Duration) (int, error)
	deletePipeline(ctx context.Context, currentKey, prevKey string) error
}

// valkeyStore is the Valkey-backed implementation of RoomKeyStore.
type valkeyStore struct {
	client      hashCommander
	gracePeriod time.Duration
}

// roomkey returns the Valkey hash key for a room's current key pair.
func roomkey(roomID string) string {
	return "room:" + roomID + ":key"
}

// roomprevkey returns the Valkey hash key for a room's previous key pair.
func roomprevkey(roomID string) string {
	return "room:" + roomID + ":key:prev"
}

// Set stores pair in Valkey as a hash with no TTL, assigning version 0.
// Does not touch the previous key slot.
func (s *valkeyStore) Set(ctx context.Context, roomID string, pair RoomKeyPair) (int, error) {
	pub := base64.StdEncoding.EncodeToString(pair.PublicKey)
	priv := base64.StdEncoding.EncodeToString(pair.PrivateKey)
	key := roomkey(roomID)
	if err := s.client.hset(ctx, key, pub, priv); err != nil {
		return 0, fmt.Errorf("set room key: %w", err)
	}
	return 0, nil
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
	ver, err := strconv.Atoi(fields["ver"])
	if err != nil {
		return nil, fmt.Errorf("get room key: parse version: %w", err)
	}
	pair, err := decodeKeyPair(fields)
	if err != nil {
		return nil, fmt.Errorf("get room key: %w", err)
	}
	return &VersionedKeyPair{
		Version: ver,
		KeyPair: *pair,
	}, nil
}

// GetByVersion retrieves the key pair matching version from either the current or previous slot.
// Returns (nil, nil) if neither matches or both are absent.
func (s *valkeyStore) GetByVersion(ctx context.Context, roomID string, version int) (*RoomKeyPair, error) {
	versionID := strconv.Itoa(version)

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

// Rotate atomically moves the current key to the previous slot (with grace period TTL),
// increments the version, and writes newPair as the current key.
// Returns the new version number. Returns ErrNoCurrentKey if no current key exists.
func (s *valkeyStore) Rotate(ctx context.Context, roomID string, newPair RoomKeyPair) (int, error) {
	pub := base64.StdEncoding.EncodeToString(newPair.PublicKey)
	priv := base64.StdEncoding.EncodeToString(newPair.PrivateKey)
	version, err := s.client.rotatePipeline(ctx, roomkey(roomID), roomprevkey(roomID), pub, priv, s.gracePeriod)
	if err != nil {
		return 0, fmt.Errorf("rotate room key: %w", err)
	}
	return version, nil
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
