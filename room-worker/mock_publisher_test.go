package main

import (
	"bytes"
	"context"
	"sync"

	"github.com/hmchangw/chat/pkg/roomkeysender"
	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// mockPublisher captures NATS publishes for use in unit tests.
type mockPublisher struct {
	mu       sync.Mutex
	subjects []string
	payloads [][]byte
}

func (p *mockPublisher) Publish(subj string, data []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.subjects = append(p.subjects, subj)
	p.payloads = append(p.payloads, append([]byte(nil), data...))
	return nil
}

func (p *mockPublisher) publishCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.subjects)
}

// stubRoomKeyStore is a zero-config RoomKeyStore that returns a valid
// version-0 key for any roomID. Used by tests that don't exercise key behavior
// (production now requires Valkey via the VALKEY_ADDR=required gate, so the
// Handler can no longer be constructed with a nil keyStore). Tests that DO
// exercise key behavior should build their own MockRoomKeyStore with explicit
// EXPECTations rather than using this stub.
type stubRoomKeyStore struct{}

func (stubRoomKeyStore) Get(_ context.Context, _ string) (*roomkeystore.VersionedKeyPair, error) {
	return &roomkeystore.VersionedKeyPair{
		Version: 0,
		KeyPair: roomkeystore.RoomKeyPair{
			PublicKey:  bytes.Repeat([]byte{0x04}, 65),
			PrivateKey: bytes.Repeat([]byte{0x05}, 32),
		},
	}, nil
}

// testKeyStore and testKeySender provide the default wiring used by tests that
// don't override key behavior. See stubRoomKeyStore above.
var (
	testKeyStore  RoomKeyStore          = stubRoomKeyStore{}
	testKeySender *roomkeysender.Sender = roomkeysender.NewSender(&mockPublisher{})
)
