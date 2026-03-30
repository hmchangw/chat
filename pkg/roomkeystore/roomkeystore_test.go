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
	store      map[string]map[string]string
	hsetErr    error
	expireErr  error
	hgetallErr error
	delErr     error
}

func (f *fakeHashClient) hset(_ context.Context, key string, pub, priv string) error {
	if f.hsetErr != nil {
		return f.hsetErr
	}
	if f.store == nil {
		f.store = make(map[string]map[string]string)
	}
	f.store[key] = map[string]string{"pub": pub, "priv": priv}
	return nil
}

func (f *fakeHashClient) expire(_ context.Context, _ string, _ time.Duration) error {
	return f.expireErr
}

func (f *fakeHashClient) hgetall(_ context.Context, key string) (map[string]string, error) {
	if f.hgetallErr != nil {
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

func (f *fakeHashClient) del(_ context.Context, key string) error {
	if f.delErr != nil {
		return f.delErr
	}
	if f.store != nil {
		delete(f.store, key)
	}
	return nil
}

// newTestStore creates a valkeyStore backed by the given fake for unit tests.
func newTestStore(fake *fakeHashClient) *valkeyStore {
	return &valkeyStore{client: fake, ttl: time.Hour}
}

func TestValkeyStore_Set(t *testing.T) {
	pubKey := bytes.Repeat([]byte{0xAB}, 65)
	privKey := bytes.Repeat([]byte{0xCD}, 32)
	pair := RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey}

	tests := []struct {
		name        string
		fake        *fakeHashClient
		roomID      string
		wantErr     bool
		errContains string
	}{
		{
			name:   "happy path — stores key pair",
			fake:   &fakeHashClient{},
			roomID: "room-1",
		},
		{
			name:        "hset error — returns wrapped error",
			fake:        &fakeHashClient{hsetErr: errors.New("connection refused")},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "set room key",
		},
		{
			name:        "expire error — returns wrapped error",
			fake:        &fakeHashClient{expireErr: errors.New("timeout")},
			roomID:      "room-1",
			wantErr:     true,
			errContains: "set room key ttl",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newTestStore(tt.fake)
			err := store.Set(context.Background(), tt.roomID, pair)
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
		want        *RoomKeyPair
		wantErr     bool
		errContains string
	}{
		{
			name: "happy path — returns stored key pair",
			fake: func() *fakeHashClient {
				f := &fakeHashClient{}
				store := newTestStore(f)
				err := store.Set(context.Background(), "room-1", RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey})
				if err != nil {
					panic("test setup failed: " + err.Error())
				}
				return f
			}(),
			roomID: "room-1",
			want:   &RoomKeyPair{PublicKey: pubKey, PrivateKey: privKey},
		},
		{
			name:   "missing key — returns nil, nil",
			fake:   &fakeHashClient{},
			roomID: "nonexistent",
			want:   nil,
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
					roomkey("room-1"): {"pub": "!!!notbase64!!!", "priv": "AQID"},
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
					roomkey("room-1"): {"pub": "AQID", "priv": "!!!notbase64!!!"},
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
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tt.want.PublicKey, got.PublicKey)
			assert.Equal(t, tt.want.PrivateKey, got.PrivateKey)
		})
	}
}
