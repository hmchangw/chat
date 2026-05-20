package roomcrypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"io"
	"testing"

	"golang.org/x/crypto/hkdf"
)

// BenchmarkSymmetricOnly measures the hot path: HKDF (cached
// outside the loop) + AES-GCM seal with a random nonce.
func BenchmarkSymmetricOnly(b *testing.B) {
	// Setup outside the timer: derive a per-version AES key once.
	ikm := make([]byte, 32) // stands in for room private key scalar
	if _, err := io.ReadFull(rand.Reader, ikm); err != nil {
		b.Fatal(err)
	}
	aesKey := make([]byte, 32)
	r := hkdf.New(sha256.New, ikm, nil, []byte("room-message-encryption-v2"))
	if _, err := io.ReadFull(r, aesKey); err != nil {
		b.Fatal(err)
	}
	block, err := aes.NewCipher(aesKey)
	if err != nil {
		b.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		b.Fatal(err)
	}
	content := []byte("hello, world — a typical short chat message")
	nonce := make([]byte, gcm.NonceSize())

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
			b.Fatal(err)
		}
		_ = gcm.Seal(nil, nonce, content, nil)
	}
}

// BenchmarkX25519KeyGen and BenchmarkX25519ECDH are reference points so the
// design spec can quote the alternative-curve performance honestly.
func BenchmarkX25519KeyGen(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ecdh.X25519().GenerateKey(rand.Reader); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkX25519ECDH(b *testing.B) {
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	otherPriv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	otherPub := otherPriv.PublicKey()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := priv.ECDH(otherPub); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEncoder_Encode measures the proposed hot path: encoder cache
// hit + nonce read + AES-GCM seal. This is what broadcast-worker pays
// per message after migration.
func BenchmarkEncoder_Encode(b *testing.B) {
	priv := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, priv); err != nil {
		b.Fatal(err)
	}
	enc := NewEncoder()

	// Warm the cache so we measure the steady-state cost, not the
	// one-time HKDF derivation.
	if _, err := enc.Encode("room-1", "warm", priv, 1); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := enc.Encode("room-1", "hello, world — a typical short chat message", priv, 1); err != nil {
			b.Fatal(err)
		}
	}
}
