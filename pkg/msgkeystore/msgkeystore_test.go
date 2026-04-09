package msgkeystore

import (
	"bytes"
	"context"
	"errors"
	"strconv"
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

func (f *fakeHashClient) hset(_ context.Context, key string, val string) error {
	if f.hsetErr != nil {
		return f.hsetErr
	}
	if f.store == nil {
		f.store = make(map[string]map[string]string)
	}
	f.store[key] = map[string]string{"key": val, "ver": "0"}
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

func (f *fakeHashClient) rotatePipeline(_ context.Context, currentKey, prevKey string, val string, _ time.Duration) (int, error) {
	if f.rotatePipelineErr != nil {
		return 0, f.rotatePipelineErr
	}
	if f.store == nil {
		return 0, ErrNoCurrentKey
	}
	cur, ok := f.store[currentKey]
	if !ok {
		return 0, ErrNoCurrentKey
	}
	curVer, _ := strconv.Atoi(cur["ver"])
	newVer := curVer + 1
	// Copy current to prev.
	f.store[prevKey] = map[string]string{"key": cur["key"], "ver": cur["ver"]}
	// Write new current.
	f.store[currentKey] = map[string]string{"key": val, "ver": strconv.Itoa(newVer)}
	return newVer, nil
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
	aesKey := bytes.Repeat([]byte{0xAB}, 32)

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		wantErr     bool
		errContains string
	}{
		{
			name:   "happy path — stores key with no TTL",
			fake:   &fakeHashClient{},
			roomID: "room-1",
		},
		{
			name:        "hset error — returns wrapped error",
			fake:        &fakeHashClient{hsetErr: errors.New("connection refused")},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "set db key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			ver, err := store.Set(context.Background(), tt.roomID, aesKey)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 0, ver)
			// Verify the hash was written under the correct Valkey key.
			stored := tt.fake.store[dbkey(tt.roomID)]
			require.NotNil(t, stored, "hash should exist in fake store")
			assert.NotEmpty(t, stored["key"], "key field should be set")
			assert.Equal(t, "0", stored["ver"], "ver field should be 0")
		})
	}
}

func TestValkeyStore_Get(t *testing.T) {
	aesKey := bytes.Repeat([]byte{0xAB}, 32)

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		wantKey     []byte
		wantVer     int
		wantErr     bool
		errContains string
	}{
		{
			name: "happy path — returns VersionedKey with correct Version",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				store := newTestStore(f)
				_, _ = store.Set(context.Background(), "room-1", aesKey)
				return f
			}(),
			roomID:  "room-1",
			wantKey: aesKey,
			wantVer: 0,
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
			errContains: "get db key",
		},
		{
			name: "corrupted key base64 — returns error",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					dbkey("room-1"): {"key": "!!!notbase64!!!", "ver": "0"},
				},
			},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "get db key",
		},
		{
			name: "non-numeric version — returns error containing parse version",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					dbkey("room-1"): {"key": "AQID", "ver": "not-a-number"},
				},
			},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "parse version",
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
			if tt.wantKey == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.wantVer, got.Version)
			assert.Equal(t, tt.wantKey, got.Key)
		})
	}
}

func TestValkeyStore_GetByVersion(t *testing.T) {
	aesKey := bytes.Repeat([]byte{0xAB}, 32)
	aesKey2 := bytes.Repeat([]byte{0x11}, 32)

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		version     int
		wantKey     []byte
		wantErr     bool
		errContains string
	}{
		{
			name: "matches current key",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				_, _ = s.Set(context.Background(), "room-1", aesKey)
				return f
			}(),
			roomID:  "room-1",
			version: 0,
			wantKey: aesKey,
		},
		{
			name: "matches previous key after rotation",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				_, _ = s.Set(context.Background(), "room-1", aesKey)
				_, _ = s.Rotate(context.Background(), "room-1", aesKey2)
				return f
			}(),
			roomID:  "room-1",
			version: 0,
			wantKey: aesKey,
		},
		{
			name: "no match — returns nil, nil",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				_, _ = s.Set(context.Background(), "room-1", aesKey)
				return f
			}(),
			roomID:  "room-1",
			version: 999,
		},
		{
			name:    "no keys at all — returns nil, nil",
			fake:    &fakeHashClient{},
			roomID:  "room-1",
			version: 0,
		},
		{
			name:        "hgetall error on current key — returns wrapped error",
			fake:        &fakeHashClient{hgetallErr: errors.New("connection reset")},
			roomID:      "room-1",
			version:     0,
			wantErr:     true,
			errContains: "get db key by version",
		},
		{
			name: "hgetall error on previous key — returns wrapped error",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				s := newTestStore(f)
				// Set a current key with a different version so the code falls through to check previous.
				_, _ = s.Set(context.Background(), "room-1", aesKey)
				f.hgetallErr = errors.New("connection reset")
				f.hgetallErrOnCall = 2 // error only on the second hgetall (previous key lookup)
				return f
			}(),
			roomID:      "room-1",
			version:     99,
			wantErr:     true,
			errContains: "get db key by version",
		},
		{
			name: "corrupted previous key base64 — returns error",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					dbkey("room-1"):     {"key": "AQID", "ver": "0"},
					dbprevkey("room-1"): {"key": "!!!bad!!!", "ver": "99"},
				},
			},
			roomID:      "room-1",
			version:     99,
			wantErr:     true,
			errContains: "get db key by version",
		},
		{
			name: "corrupted current key base64 — returns error when version matches current",
			fake: &fakeHashClient{
				store: map[string]map[string]string{
					dbkey("room-1"): {"key": "!!!bad!!!", "ver": "0"},
				},
			},
			roomID:      "room-1",
			version:     0,
			wantErr:     true,
			errContains: "get db key by version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			got, err := store.GetByVersion(context.Background(), tt.roomID, tt.version)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			if tt.wantKey == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.wantKey, got)
		})
	}
}

func TestValkeyStore_Rotate(t *testing.T) {
	aesKey := bytes.Repeat([]byte{0xAB}, 32)
	newAESKey := bytes.Repeat([]byte{0x11}, 32)

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		setupFn     func(s *valkeyStore)
		wantVer     int
		wantErr     bool
		errContains string
		errIs       error
	}{
		{
			name:   "happy path — new key becomes current, old becomes previous",
			fake:   &fakeHashClient{},
			roomID: "room-1",
			setupFn: func(s *valkeyStore) {
				_, _ = s.Set(context.Background(), "room-1", aesKey)
			},
			wantVer: 1,
		},
		{
			name:        "no current key — returns ErrNoCurrentKey",
			fake:        &fakeHashClient{},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "rotate db key",
			errIs:       ErrNoCurrentKey,
		},
		{
			name:   "replaces existing previous key",
			fake:   &fakeHashClient{},
			roomID: "room-1",
			setupFn: func(s *valkeyStore) {
				_, _ = s.Set(context.Background(), "room-1", aesKey)
				// First rotation creates a previous key.
				_, _ = s.Rotate(context.Background(), "room-1", newAESKey)
			},
			wantVer: 2,
		},
		{
			name:   "pipeline error — returns wrapped error",
			fake:   &fakeHashClient{rotatePipelineErr: errors.New("pipeline broken")},
			roomID: "room-1",
			setupFn: func(s *valkeyStore) {
				// Temporarily clear the error so Set works.
				s.client.(*fakeHashClient).rotatePipelineErr = nil
				_, _ = s.Set(context.Background(), "room-1", aesKey)
				s.client.(*fakeHashClient).rotatePipelineErr = errors.New("pipeline broken")
			},
			wantErr:     true,
			errContains: "rotate db key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			if tt.setupFn != nil {
				tt.setupFn(store)
			}
			ver, err := store.Rotate(context.Background(), tt.roomID, newAESKey)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errContains)
				if tt.errIs != nil {
					assert.True(t, errors.Is(err, tt.errIs), "expected errors.Is match for %v", tt.errIs)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantVer, ver)

			// Verify new key is current with correct version.
			got, err := store.Get(context.Background(), tt.roomID)
			require.NoError(t, err)
			require.NotNil(t, got)
			assert.Equal(t, tt.wantVer, got.Version)
			assert.Equal(t, newAESKey, got.Key)
		})
	}
}

func TestValkeyStore_Delete(t *testing.T) {
	aesKey := bytes.Repeat([]byte{0xAB}, 32)

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
				_, _ = s.Set(context.Background(), "room-1", aesKey)
				_, _ = s.Rotate(context.Background(), "room-1", bytes.Repeat([]byte{0x11}, 32))
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
			errContains: "delete db key",
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
			_, exists := tt.fake.store[dbkey(tt.roomID)]
			assert.False(t, exists, "current key should be removed after Delete")
			_, exists = tt.fake.store[dbprevkey(tt.roomID)]
			assert.False(t, exists, "previous key should be removed after Delete")
		})
	}
}
