package roomkeystore

import (
	"crypto/rand"
	"fmt"
)

// GenerateKeyPair returns a fresh 32-byte room secret used by roomcrypto
// as HKDF input keying material. The name retains "KeyPair" for source
// compatibility with existing call sites; cryptographically this is now
// a single symmetric secret, not an asymmetric keypair.
func GenerateKeyPair() (*RoomKeyPair, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate room key: %w", err)
	}
	return &RoomKeyPair{PrivateKey: buf}, nil
}
