package main

import (
	"context"
	"sync"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/roomkeystore"
)

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

func (s *stubKeyStore) Set(_ context.Context, roomID string, pair roomkeystore.RoomKeyPair) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.store[roomID] = &roomkeystore.VersionedKeyPair{Version: 0, KeyPair: pair}
	return 0, nil
}

func (s *stubKeyStore) Rotate(_ context.Context, roomID string, newPair roomkeystore.RoomKeyPair) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.store[roomID]
	if !ok {
		return 0, roomkeystore.ErrNoCurrentKey
	}
	v.Version++
	v.KeyPair = newPair
	return v.Version, nil
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
