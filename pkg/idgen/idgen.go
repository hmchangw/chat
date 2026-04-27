// Package idgen produces identifiers for the chat system:
// UUIDv7 hex (32 chars) for entity MongoDB _ids via GenerateUUIDv7,
// 20-char base62 for message IDs via GenerateMessageID,
// 17-char base62 for channel room IDs via GenerateID, and
// sorted-concat user IDs for DM rooms via BuildDMRoomID.
package idgen

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"

	"github.com/google/uuid"
)

// base62 alphabet (0-9A-Za-z).
const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

const (
	// idLength: 17-char base62, ~101 bits of entropy. Channel room IDs and outbox dedup seeds.
	idLength = 17
	// messageIDLength: 20-char base62, ~119 bits of entropy. Message.ID and JetStream Nats-Msg-Id.
	messageIDLength = 20
)

// encodeBase62 renders n into a length-char base62 string. Mutates n.
func encodeBase62(n *big.Int, length int) string {
	base := big.NewInt(int64(len(alphabet)))
	mod := new(big.Int)
	buf := make([]byte, length)
	for i := length - 1; i >= 0; i-- {
		n.DivMod(n, base, mod)
		buf[i] = alphabet[mod.Int64()]
	}
	return string(buf)
}

// generateBase62 returns a uniformly-distributed random base62 string of the requested length via rejection sampling on bytes (rejects ≥248; ~3.1% rate).
func generateBase62(length int) string {
	out := make([]byte, length)
	bufSize := length + length/8 + 1
	buf := make([]byte, bufSize)
	written := 0
	for written < length {
		if _, err := rand.Read(buf); err != nil {
			panic("idgen: crypto/rand read failed: " + err.Error())
		}
		for _, b := range buf {
			if b >= 248 {
				continue
			}
			out[written] = alphabet[b%62]
			written++
			if written == length {
				break
			}
		}
	}
	return string(out)
}

// GenerateID returns a fresh random 17-char base62 identifier (channel room IDs).
func GenerateID() string {
	return generateBase62(idLength)
}

// DeriveID returns a deterministic 17-char base62 ID from seed (SHA-256 → 128-bit → base62).
func DeriveID(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return encodeBase62(new(big.Int).SetBytes(h[:16]), idLength)
}

// GenerateMessageID returns a fresh random 20-char base62 identifier (Message.ID and Nats-Msg-Id).
func GenerateMessageID() string {
	return generateBase62(messageIDLength)
}

// MessageIDFromRequestID returns a deterministic 20-char base62 from SHA-256(requestID+":"+suffix); stable across redeliveries so JetStream dedup catches retries.
func MessageIDFromRequestID(requestID, suffix string) string {
	h := sha256.Sum256([]byte(requestID + ":" + suffix))
	return encodeBase62(new(big.Int).SetBytes(h[:16]), messageIDLength)
}

// IsValidMessageID reports whether s is a well-formed 20-char base62 message ID.
func IsValidMessageID(s string) bool {
	if len(s) != messageIDLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= '0' && c <= '9':
		case c >= 'A' && c <= 'Z':
		case c >= 'a' && c <= 'z':
		default:
			return false
		}
	}
	return true
}

// GenerateUUIDv7 returns a fresh UUIDv7 as 32-char lowercase hex without hyphens (entity Mongo _id and request IDs).
func GenerateUUIDv7() string {
	u, err := uuid.NewV7()
	if err != nil {
		panic("idgen: uuid.NewV7 failed: " + err.Error())
	}
	var buf [32]byte
	hex.Encode(buf[:], u[:])
	return string(buf[:])
}

// BuildDMRoomID returns the lexicographically-sorted concat of two user IDs; BuildDMRoomID(a,b) == BuildDMRoomID(b,a).
func BuildDMRoomID(userA, userB string) string {
	if userA <= userB {
		return userA + userB
	}
	return userB + userA
}
