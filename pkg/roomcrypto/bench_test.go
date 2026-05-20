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

// BenchmarkEncode measures end-to-end cost of the current ECIES-style Encode
// (parse pub + ephemeral keygen + ECDH + HKDF + AES-GCM seal).
func BenchmarkEncode(b *testing.B) {
	privKey, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	pub := privKey.PublicKey().Bytes()
	content := "hello, world — a typical short chat message"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Encode(content, pub, 0); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkEphemeralKeyGen isolates the cost of P-256 ephemeral key generation.
func BenchmarkEphemeralKeyGen(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := ecdh.P256().GenerateKey(rand.Reader); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkECDH isolates the cost of the P-256 ECDH scalar multiplication.
func BenchmarkECDH(b *testing.B) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	otherPriv, err := ecdh.P256().GenerateKey(rand.Reader)
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

// BenchmarkParsePub isolates the cost of parsing a 65-byte uncompressed P-256
// public key (which Encode does on every call).
func BenchmarkParsePub(b *testing.B) {
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	pubBytes := priv.PublicKey().Bytes()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ecdh.P256().NewPublicKey(pubBytes); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSymmetricOnly measures the proposed Stage-2 hot path: HKDF (cached
// outside the loop) + AES-GCM seal with a random nonce. This is what Encode
// would cost per-message after dropping ECIES.
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
