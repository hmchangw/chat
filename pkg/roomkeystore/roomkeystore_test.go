package roomkeystore

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHashClient is a test double for hashCommander.
// It simulates an in-memory Valkey hash store with injectable per-method errors.
type fakeHashClient struct {
	store             map[string]map[string]string
	hsetErr           error
	hgetallErr        error
	hgetallCallCount  int // tracks number of hgetall calls made
	hgetallErrOnCall  int // if >0, hgetallErr fires only on this call number (1-based)
	rotatePipelineErr error
	deletePipelineErr error
}

func (f *fakeHashClient) hset(_ context.Context, key string, pub, priv, ver string) error {
	if f.hsetErr != nil {
		return f.hsetErr
	}
	if f.store == nil {
		f.store = make(map[string]map[string]string)
	}
	f.store[key] = map[string]string{"pub": pub, "priv": priv, "ver": ver}
	return nil
}

func (f *fakeHashClient) hgetall(_ context.Context, key string) (map[string]string, error) {
	f.hgetallCallCount++
	if f.hgetallErr != nil && (f.hgetallErrOnCall == 0 || f.hgetallCallCount == f.hgetallErrOnCall) {
		return nil, f.hgetallErr
	}
	if f.store == nil {
		return map[string]string{}, nil
	}
	m, ok := f.store[key]
	if !ok {
		return map[string]string{}, nil
	}
	return m, nil
}

func (f *fakeHashClient) rotatePipeline(_ context.Context, currentKey, prevKey string, pub, priv, ver string, _ time.Duration) error {
	if f.rotatePipelineErr != nil {
		return f.rotatePipelineErr
	}
	if f.store == nil {
		return ErrNoCurrentKey
	}
	cur, ok := f.store[currentKey]
	if !ok {
		return ErrNoCurrentKey
	}
	// Copy current to prev.
	f.store[prevKey] = map[string]string{"pub": cur["pub"], "priv": cur["priv"], "ver": cur["ver"]}
	// Write new current.
	f.store[currentKey] = map[string]string{"pub": pub, "priv": priv, "ver": ver}
	return nil
}

func (f *fakeHashClient) deletePipeline(_ context.Context, currentKey, prevKey string) error {
	if f.deletePipelineErr != nil {
		return f.deletePipelineErr
	}
	if f.store != nil {
		delete(f.store, currentKey)
		delete(f.store, prevKey)
	}
	return nil
}

// newTestStore creates a valkeyStore backed by the given fake for unit tests.
func newTestStore(fake *fakeHashClient) *valkeyStore {
	return &valkeyStore{client: fake, gracePeriod: time.Hour}
}

func TestValkeyStore_Set(t *testing.T) {
	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)
	pair := RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey}

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		versionID   string
		wantErr     bool
		errContains string
	}{
		{
			name:      "happy path — stores key pair with no TTL",
			fake:      &fakeHashClient{},
			roomID:    "room-1",
			versionID: "v1",
		},
		{
			name:        "hset error — returns wrapped error",
			fake:        &fakeHashClient{hsetErr: errors.New("connection refused")},
			roomID:      "room-1",
			versionID:   "v1",
			wantErr:     true,
			errContains: "set room key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			err := store.Set(context.Background(), tt.roomID, tt.versionID, pair)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			// Verify the hash was written under the correct Valkey key.
			stored := tt.fake.store[roomkey(tt.roomID)]
			require.NotNil(t, stored, "hash should exist in fake store")
			assert.NotEmpty(t, stored["pub"], "pub field should be set")
			assert.NotEmpty(t, stored["priv"], "priv field should be set")
			assert.Equal(t, tt.versionID, stored["ver"], "ver field should match")
		})
	}
}

func TestValkeyStore_Get(t *testing.T) {
	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		wantPair    *RoomKeyPair
		wantVer     string
		wantErr     bool
		errContains string
	}{
		{
			name: "happy path — returns VersionedKeyPair with correct VersionID",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				store := newTestStore(f)
				_ = store.Set(context.Background(), "room-1", "v1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				return f
			}(),
			roomID:   "room-1",
			wantPair: &RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey},
			wantVer:  "v1",
		},
		{
			name:   "missing key — returns nil, nil",
			fake:   &fakeHashClient{},
			roomID: "nonexistent",
		},
		{
			name:        "hgetall error — returns wrapped error",
			fake:        &fakeHashClient{hgetallErr: errors.New("io timeout")},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "get room key",
		},
		{
			name: "corrupted pub base64 — returns error",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					roomkey("room-1"): {"pub": "!!!notbase64!!!", "priv": "AQID", "ver": "v1"},
				},
			},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "get room key",
		},
		{
			name: "corrupted priv base64 — returns error",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					roomkey("room-1"): {"pub": "AQID", "priv": "!!!notbase64!!!", "ver": "v1"},
				},
			},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "get room key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			got, err := store.Get(context.Background(), tt.roomID)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if tt.wantPair == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.wantVer, got.VersionID)
			assert.Equal(t, tt.wantPair.PublicKey, got.KeyPair.PublicKey)
			assert.Equal(t, tt.wantPair.PrivateKey, got.KeyPair.PrivateKey)
		})
	}
}

func TestValkeyStore_GetByVersion(t *testing.T) {
	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)
	pubKey2 := bytes.Repeat([]byte{0x11}, 65)
	privKey2 := bytes.Repeat([]byte{0x22}, 32)

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		versionID   string
		wantPair    *RoomKeyPair
		wantErr     bool
		errContains string
	}{
		{
			name: "matches current key",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				_ = s.Set(context.Background(), "room-1", "v1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				return f
			}(),
			roomID:    "room-1",
			versionID: "v1",
			wantPair:  &RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey},
		},
		{
			name: "matches previous key after rotation",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				_ = s.Set(context.Background(), "room-1", "v1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				_ = s.Rotate(context.Background(), "room-1", "v2", RoomKeyPair{PublicKey: pubKey2, PrivateKey: privKey2})
				return f
			}(),
			roomID:    "room-1",
			versionID: "v1",
			wantPair:  &RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey},
		},
		{
			name: "no match — returns nil, nil",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				_ = s.Set(context.Background(), "room-1", "v1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				return f
			}(),
			roomID:    "room-1",
			versionID: "nonexistent-version",
		},
		{
			name:      "no keys at all — returns nil, nil",
			fake:      &fakeHashClient{},
			roomID:    "room-1",
			versionID: "v1",
		},
		{
			name:        "hgetall error on current key — returns wrapped error",
			fake:        &fakeHashClient{hgetallErr: errors.New("connection reset")},
			roomID:      "room-1",
			versionID:   "v1",
			wantErr:     true,
			errContains: "get room key by version",
		},
		{
			name: "hgetall error on previous key — returns wrapped error",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				// Set a current key with a different version so the code falls through to check previous.
				_ = s.Set(context.Background(), "room-1", "v1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				f.hgetallErr = errors.New("connection reset")
				f.hgetallErrOnCall = 2 // error only on the second hgetall (previous key lookup)
				return f
			}(),
			roomID:      "room-1",
			versionID:   "v-old",
			wantErr:     true,
			errContains: "get room key by version",
		},
		{
			name: "corrupted previous key base64 — returns error",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					roomkey("room-1"):     {"pub": "AQID", "priv": "AQID", "ver": "v1"},
					roomprevkey("room-1"): {"pub": "!!!bad!!!", "priv": "AQID", "ver": "v-old"},
				},
			},
			roomID:      "room-1",
			versionID:   "v-old",
			wantErr:     true,
			errContains: "get room key by version",
		},
		{
			name: "corrupted current key base64 — returns error when version matches current",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					roomkey("room-1"): {"pub": "!!!bad!!!", "priv": "AQID", "ver": "v1"},
				},
			},
			roomID:      "room-1",
			versionID:   "v1",
			wantErr:     true,
			errContains: "get room key by version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			got, err := store.GetByVersion(context.Background(), tt.roomID, tt.versionID)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if tt.wantPair == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.wantPair.PublicKey, got.PublicKey)
			assert.Equal(t, tt.wantPair.PrivateKey, got.PrivateKey)
		})
	}
}

func TestValkeyStore_Rotate(t *testing.T) {
	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)
	newPubKey := bytes.Repeat([]byte{0x11}, 65)
	newPrivKey := bytes.Repeat([]byte{0x22}, 32)

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		setupFn     func(s *valkeyStore)
		wantErr     bool
		errContains string
		errIs       error
	}{
		{
			name:   "happy path — new pair becomes current, old becomes previous",
			fake:   &fakeHashClient{},
			roomID: "room-1",
			setupFn: func(s *valkeyStore) {
				_ = s.Set(context.Background(), "room-1", "v1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
			},
		},
		{
			name:        "no current key — returns ErrNoCurrentKey",
			fake:        &fakeHashClient{},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "rotate room key",
			errIs:       ErrNoCurrentKey,
		},
		{
			name:   "replaces existing previous key",
			fake:   &fakeHashClient{},
			roomID: "room-1",
			setupFn: func(s *valkeyStore) {
				_ = s.Set(context.Background(), "room-1", "v1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				// First rotation creates a previous key.
				_ = s.Rotate(context.Background(), "room-1", "v2", RoomKeyPair{PublicKey: newPubKey, PrivateKey: newPrivKey})
			},
		},
		{
			name:   "pipeline error — returns wrapped error",
			fake:   &fakeHashClient{rotatePipelineErr: errors.New("pipeline broken")},
			roomID: "room-1",
			setupFn: func(s *valkeyStore) {
				// Temporarily clear the error so Set works.
				s.client.(*fakeHashClient).rotatePipelineErr = nil
				_ = s.Set(context.Background(), "room-1", "v1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				s.client.(*fakeHashClient).rotatePipelineErr = errors.New("pipeline broken")
			},
			wantErr:     true,
			errContains: "rotate room key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			if tt.setupFn != nil {
				tt.setupFn(store)
			}
			err := store.Rotate(context.Background(), tt.roomID, "v-new", RoomKeyPair{PublicKey: newPubKey, PrivateKey: newPrivKey})
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				if tt.errIs != nil {
					assert.True(t, errors.Is(err, tt.errIs), "expected errors.Is match for %v", tt.errIs)
				}
				return
			}
			require.NoError(t, err)

			// Verify new pair is current with correct version.
			got, err := store.Get(context.Background(), tt.roomID)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, "v-new", got.VersionID)
			assert.Equal(t, newPubKey, got.KeyPair.PublicKey)
			assert.Equal(t, newPrivKey, got.KeyPair.PrivateKey)
		})
	}
}

func TestValkeyStore_Delete(t *testing.T) {
	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		wantErr     bool
		errContains string
	}{
		{
			name: "happy path — deletes both current and previous keys",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				_ = s.Set(context.Background(), "room-1", "v1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				_ = s.Rotate(context.Background(), "room-1", "v2", RoomKeyPair{
					PublicKey:  bytes.Repeat([]byte{0x11}, 65),
					PrivateKey: bytes.Repeat([]byte{0x22}, 32),
				})
				return f
			}(),
			roomID: "room-1",
		},
		{
			name:   "missing key — no-op, no error",
			fake:   &fakeHashClient{},
			roomID: "nonexistent",
		},
		{
			name:        "deletePipeline error — returns wrapped error",
			fake:        &fakeHashClient{deletePipelineErr: errors.New("connection lost")},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "delete room key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			err := store.Delete(context.Background(), tt.roomID)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			// Verify both current and previous keys are gone.
			_, exists := tt.fake.store[roomkey(tt.roomID)]
			assert.False(t, exists, "current key should be removed after Delete")
			_, exists = tt.fake.store[roomprevkey(tt.roomID)]
			assert.False(t, exists, "previous key should be removed after Delete")
		})
	}
}
