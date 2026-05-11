package main

import (
	"bytes"
	"context"
	"sync"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// newKeyDepsForTest returns a (keyStore, client) pair with a non-empty stub key
// for any roomID. Use it when a test exercises a handler path that requires key
// wiring but doesn't otherwise care about the specific key bytes or version.
func newKeyDepsForTest() (*stubKeyStore, *stubInterSiteClient) {
	return newStubKeyStore(), &stubInterSiteClient{
		getResp: &model.RoomKeyEvent{
			RoomID:     "stub",
			Version:    1,
			PublicKey:  bytes.Repeat([]byte{0x04}, 65),
			PrivateKey: bytes.Repeat([]byte{0x05}, 32),
		},
	}
}

type stubKeyStore struct {
	mu     sync.Mutex
	store  map[string]*roomkeystore.VersionedKeyPair
	getErr error // when set, Get returns (nil, getErr)
}

func newStubKeyStore() *stubKeyStore {
	return &stubKeyStore{store: map[string]*roomkeystore.VersionedKeyPair{}}
}

func (s *stubKeyStore) Get(_ context.Context, roomID string) (*roomkeystore.VersionedKeyPair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.getErr != nil {
		return nil, s.getErr
	}
	v, ok := s.store[roomID]
	if !ok {
		return nil, nil
	}
	cp := *v
	return &cp, nil
}

func (s *stubKeyStore) SetWithVersion(_ context.Context, roomID string, pair roomkeystore.RoomKeyPair, version int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[roomID] = &roomkeystore.VersionedKeyPair{Version: version, KeyPair: pair}
	return nil
}

// Set/Rotate retained for tests that pre-seed the stub at known versions.
// inbox-worker's production code now only calls Get and SetWithVersion.
func (s *stubKeyStore) Set(_ context.Context, roomID string, pair roomkeystore.RoomKeyPair) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[roomID] = &roomkeystore.VersionedKeyPair{Version: 0, KeyPair: pair}
	return 0, nil
}

func (s *stubKeyStore) Close() error { return nil }

type stubInterSiteClient struct {
	getResp *model.RoomKeyEvent
	getErr  error
	calls   []string
	mu      sync.Mutex
}

func (s *stubInterSiteClient) GetRoomKey(_ context.Context, originSiteID, roomID string) (*model.RoomKeyEvent, error) {
	s.mu.Lock()
	s.calls = append(s.calls, originSiteID+":"+roomID)
	s.mu.Unlock()
	return s.getResp, s.getErr
}
