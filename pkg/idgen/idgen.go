// Package idgen produces 17-char base62 identifiers (random and deterministic).
package idgen

import (
	"crypto/rand"
	"crypto/sha256"
	"math/big"
)

// base62 alphabet (0-9A-Za-z). 17 chars ≈ 101 bits of entropy.
const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

const idLength = 17

// encodeBase62 renders n into an idLength-char base62 string. Mutates n.
func encodeBase62(n *big.Int) string {
	base := big.NewInt(int64(len(alphabet)))
	mod := new(big.Int)
	buf := make([]byte, idLength)
	for i := idLength - 1; i >= 0; i-- {
		n.DivMod(n, base, mod)
		buf[i] = alphabet[mod.Int64()]
	}
	return string(buf)
}

// GenerateID returns a fresh random 17-char base62 identifier.
// Uses rand.Int over 62^17 for a uniform distribution across the alphabet.
func GenerateID() string {
	max := new(big.Int).Exp(big.NewInt(int64(len(alphabet))), big.NewInt(idLength), nil)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		panic("idgen: crypto/rand read failed: " + err.Error())
	}
	return encodeBase62(n)
}

// DeriveID returns a deterministic 17-char base62 identifier from seed (SHA-256 → 128-bit → base62).
func DeriveID(seed string) string {
	h := sha256.Sum256([]byte(seed))
	return encodeBase62(new(big.Int).SetBytes(h[:16]))
}
