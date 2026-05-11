package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"fmt"

	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// generateRoomKeyPair returns a fresh P-256 keypair for a new room.
func generateRoomKeyPair() (roomkeystore.RoomKeyPair, error) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return roomkeystore.RoomKeyPair{}, fmt.Errorf("generate P-256 key: %w", err)
	}
	return roomkeystore.RoomKeyPair{
		PublicKey:  priv.PublicKey().Bytes(),
		PrivateKey: priv.Bytes(),
	}, nil
}
