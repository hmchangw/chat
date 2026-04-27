package idgen_test

import (
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/idgen"
)

func isBase62(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		default:
			return false
		}
	}
	return true
}

func TestGenerateID_LengthAndAlphabet(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := idgen.GenerateID()
		assert.Len(t, id, 17)
		assert.True(t, isBase62(id), "id %q contains non-base62 characters", id)
	}
}

func TestGenerateID_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := idgen.GenerateID()
		_, dup := seen[id]
		assert.False(t, dup, "duplicate ID %q at iteration %d", id, i)
		seen[id] = struct{}{}
	}
}

func TestDeriveID_StableAcrossCalls(t *testing.T) {
	seed := "addmembers:room-1:1735689600000"
	a := idgen.DeriveID(seed)
	b := idgen.DeriveID(seed)
	assert.Equal(t, a, b, "same seed must yield same ID")
	assert.Len(t, a, 17)
	assert.True(t, isBase62(a))
}

func TestDeriveID_DifferentSeedsDifferentIDs(t *testing.T) {
	a := idgen.DeriveID("addmembers:room-1:1")
	b := idgen.DeriveID("addmembers:room-1:2")
	assert.NotEqual(t, a, b)
}

func TestDeriveID_EmptySeed(t *testing.T) {
	id := idgen.DeriveID("")
	assert.Len(t, id, 17)
	assert.True(t, isBase62(id))
}

func TestGenerateMessageID_LengthAndAlphabet(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := idgen.GenerateMessageID()
		assert.Len(t, id, 20)
		assert.True(t, isBase62(id), "id %q contains non-base62 characters", id)
	}
}

func TestGenerateMessageID_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := idgen.GenerateMessageID()
		_, dup := seen[id]
		assert.False(t, dup, "duplicate message ID %q at iteration %d", id, i)
		seen[id] = struct{}{}
	}
}

func TestGenerateUUIDv7_LengthAndHex(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := idgen.GenerateUUIDv7()
		assert.Len(t, id, 32, "UUIDv7 hex must be 32 chars (no hyphens)")
		_, err := hex.DecodeString(id)
		assert.NoError(t, err, "id %q must be valid lowercase hex", id)
	}
}

func TestGenerateUUIDv7_Unique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for i := 0; i < 1000; i++ {
		id := idgen.GenerateUUIDv7()
		_, dup := seen[id]
		assert.False(t, dup, "duplicate UUIDv7 %q at iteration %d", id, i)
		seen[id] = struct{}{}
	}
}

func TestGenerateUUIDv7_VersionAndVariantBits(t *testing.T) {
	// UUIDv7 (RFC 9562): hex index 12 must be '7' (version), index 16 must be 8/9/a/b (variant).
	id := idgen.GenerateUUIDv7()
	require.Len(t, id, 32)
	assert.Equal(t, byte('7'), id[12], "version nibble must be 7, got %q", string(id[12]))
	assert.Contains(t, "89ab", string(id[16]), "variant nibble must be 8,9,a,b — got %q", string(id[16]))
}

func TestGenerateUUIDv7_TimeOrdered(t *testing.T) {
	// First 12 hex chars (48-bit Unix-ms timestamp) sort lexicographically by time when separated by >=2ms.
	a := idgen.GenerateUUIDv7()
	time.Sleep(2 * time.Millisecond)
	b := idgen.GenerateUUIDv7()
	assert.Less(t, a[:12], b[:12], "later UUIDv7 must have a larger timestamp prefix")
}

func TestGenerateUUIDv7_ConcurrentSafe(t *testing.T) {
	const goroutines = 50
	const perGoroutine = 200
	var (
		mu   sync.Mutex
		seen = make(map[string]struct{}, goroutines*perGoroutine)
		wg   sync.WaitGroup
	)
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func() {
			defer wg.Done()
			local := make([]string, perGoroutine)
			for i := 0; i < perGoroutine; i++ {
				local[i] = idgen.GenerateUUIDv7()
			}
			mu.Lock()
			for _, id := range local {
				_, dup := seen[id]
				assert.False(t, dup, "duplicate UUIDv7 under concurrency: %q", id)
				seen[id] = struct{}{}
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
}

func TestIsValidMessageID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"valid 20-char base62", "AbCdEfGhIjKlMnOpQrSt", true},
		{"valid all digits", "01234567890123456789", true},
		{"valid mixed", "0aZ1bY2cX3dW4eV5fU6g", true},
		{"empty string", "", false},
		{"too short (19)", "AbCdEfGhIjKlMnOpQrS", false},
		{"too long (21)", "AbCdEfGhIjKlMnOpQrStU", false},
		{"hyphen char", "AbCdEfGhIjKlMnOpQr-t", false},
		{"underscore char", "AbCdEfGhIjKlMnOpQr_t", false},
		{"unicode char", "AbCdEfGhIjKlMnOpQrSé", false},
		{"UUIDv4 with hyphens (36)", "550e8400-e29b-41d4-a716-446655440000", false},
		{"UUIDv7 hex no hyphens (32)", "01893f8b1c4a7000b8e2d4f6a1c3e5b7", false},
		{"17-char base62 (legacy)", "AbCdEfGhIjKlMnOpQ", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, idgen.IsValidMessageID(tc.in))
		})
	}
}

func TestIsValidMessageID_AcceptsGenerateMessageIDOutput(t *testing.T) {
	for i := 0; i < 50; i++ {
		assert.True(t, idgen.IsValidMessageID(idgen.GenerateMessageID()))
	}
}

func TestBuildDMRoomID_DeterministicRegardlessOfOrder(t *testing.T) {
	a := idgen.BuildDMRoomID("u-alice", "u-bob")
	b := idgen.BuildDMRoomID("u-bob", "u-alice")
	assert.Equal(t, a, b, "DM room ID must be the same regardless of caller argument order")
}

func TestBuildDMRoomID_SortedConcat(t *testing.T) {
	// Lexicographically smaller user ID comes first; no separator.
	id := idgen.BuildDMRoomID("u-bob", "u-alice")
	assert.Equal(t, "u-aliceu-bob", id)
}

func TestBuildDMRoomID_DifferentPairsDifferentIDs(t *testing.T) {
	ab := idgen.BuildDMRoomID("u-alice", "u-bob")
	ac := idgen.BuildDMRoomID("u-alice", "u-carol")
	assert.NotEqual(t, ab, ac)
}

func TestBuildDMRoomID_SelfDM(t *testing.T) {
	// Self-DMs are allowed at the idgen level; caller policy decides whether to permit them.
	id := idgen.BuildDMRoomID("u-alice", "u-alice")
	assert.Equal(t, "u-aliceu-alice", id)
}
