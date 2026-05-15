package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// fakeRoomKeyStore records Set and Delete calls and optionally returns errors.
// Implements the narrow roomKeyStore interface defined in seed.go.
type fakeRoomKeyStore struct {
	mu      sync.Mutex
	sets    map[string]roomkeystore.RoomKeyPair
	deletes []string
	setErr  error
	delErr  error
}

func newFakeRoomKeyStore() *fakeRoomKeyStore {
	return &fakeRoomKeyStore{sets: map[string]roomkeystore.RoomKeyPair{}}
}

func (f *fakeRoomKeyStore) Set(_ context.Context, roomID string, pair roomkeystore.RoomKeyPair) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return 0, f.setErr
	}
	f.sets[roomID] = pair
	return 0, nil
}

func (f *fakeRoomKeyStore) Delete(_ context.Context, roomID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.delErr != nil {
		return f.delErr
	}
	f.deletes = append(f.deletes, roomID)
	return nil
}

func TestSeedRoomKeys_WritesOnePerRoom(t *testing.T) {
	ks := newFakeRoomKeyStore()
	fixtures := Fixtures{
		Rooms: []model.Room{
			{ID: "room-a"},
			{ID: "room-b"},
			{ID: "room-c"},
		},
		RoomKeys: map[string]roomkeystore.RoomKeyPair{
			"room-a": {PublicKey: []byte("pubA"), PrivateKey: []byte("privA")},
			"room-b": {PublicKey: []byte("pubB"), PrivateKey: []byte("privB")},
			"room-c": {PublicKey: []byte("pubC"), PrivateKey: []byte("privC")},
		},
	}

	require.NoError(t, SeedRoomKeys(context.Background(), ks, &fixtures))

	assert.Len(t, ks.sets, 3)
	assert.Equal(t, []byte("pubA"), ks.sets["room-a"].PublicKey)
	assert.Equal(t, []byte("privB"), ks.sets["room-b"].PrivateKey)
	assert.Equal(t, []byte("pubC"), ks.sets["room-c"].PublicKey)
}

func TestSeedRoomKeys_MissingKeyForRoom(t *testing.T) {
	ks := newFakeRoomKeyStore()
	fixtures := Fixtures{
		Rooms:    []model.Room{{ID: "room-a"}, {ID: "room-b"}},
		RoomKeys: map[string]roomkeystore.RoomKeyPair{"room-a": {PublicKey: []byte("pub"), PrivateKey: []byte("priv")}},
	}

	err := SeedRoomKeys(context.Background(), ks, &fixtures)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "room-b")
}

func TestSeedRoomKeys_KeystoreError(t *testing.T) {
	ks := newFakeRoomKeyStore()
	ks.setErr = errors.New("boom")
	fixtures := Fixtures{
		Rooms:    []model.Room{{ID: "room-a"}},
		RoomKeys: map[string]roomkeystore.RoomKeyPair{"room-a": {PublicKey: []byte("p"), PrivateKey: []byte("k")}},
	}

	err := SeedRoomKeys(context.Background(), ks, &fixtures)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom")
}

func TestSeedRoomKeys_NoRooms(t *testing.T) {
	ks := newFakeRoomKeyStore()
	require.NoError(t, SeedRoomKeys(context.Background(), ks, &Fixtures{}))
	assert.Empty(t, ks.sets)
}

func TestTeardownRoomKeys_DeletesEachRoom(t *testing.T) {
	ks := newFakeRoomKeyStore()
	fixtures := Fixtures{
		Rooms: []model.Room{
			{ID: "room-a"},
			{ID: "room-b"},
		},
	}

	require.NoError(t, TeardownRoomKeys(context.Background(), ks, &fixtures))

	assert.ElementsMatch(t, []string{"room-a", "room-b"}, ks.deletes)
}

func TestTeardownRoomKeys_KeystoreError(t *testing.T) {
	ks := newFakeRoomKeyStore()
	ks.delErr = errors.New("nope")
	fixtures := Fixtures{Rooms: []model.Room{{ID: "room-a"}}}

	err := TeardownRoomKeys(context.Background(), ks, &fixtures)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")
}

func TestTeardownRoomKeys_NoRooms(t *testing.T) {
	ks := newFakeRoomKeyStore()
	require.NoError(t, TeardownRoomKeys(context.Background(), ks, &Fixtures{}))
	assert.Empty(t, ks.deletes)
}
