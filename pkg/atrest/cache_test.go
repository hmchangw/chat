package atrest

import (
	"crypto/cipher"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestAEAD builds a real AES-256-GCM AEAD from a 32-byte key seeded with b.
// The cache test only cares about identity (does the value round-trip?), but a
// real AEAD keeps the type correct without needing a fake.
func newTestAEAD(t *testing.T, b byte) cipher.AEAD {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = b
	}
	a, err := newAEAD(key)
	require.NoError(t, err)
	return a
}

func TestDEKCache_GetSet(t *testing.T) {
	c := newDEKCache(3, time.Hour)
	v, ok := c.get("a")
	assert.False(t, ok)
	assert.Nil(t, v)

	aead := newTestAEAD(t, 'A')
	c.set("a", aead)
	v, ok = c.get("a")
	assert.True(t, ok)
	assert.Same(t, aead, v)
}

func TestDEKCache_LRUEviction(t *testing.T) {
	c := newDEKCache(2, time.Hour)
	c.set("a", newTestAEAD(t, 'A'))
	c.set("b", newTestAEAD(t, 'B'))
	_, _ = c.get("a")               // promote a
	c.set("c", newTestAEAD(t, 'C')) // evicts b (least-recent)

	_, okA := c.get("a")
	_, okB := c.get("b")
	_, okC := c.get("c")
	assert.True(t, okA)
	assert.False(t, okB)
	assert.True(t, okC)
}

func TestDEKCache_TTLExpiry(t *testing.T) {
	c := newDEKCache(10, 20*time.Millisecond)
	c.set("a", newTestAEAD(t, 'A'))
	time.Sleep(40 * time.Millisecond)
	_, ok := c.get("a")
	assert.False(t, ok)
}

func TestDEKCache_Invalidate(t *testing.T) {
	c := newDEKCache(10, time.Hour)
	c.set("a", newTestAEAD(t, 'A'))
	c.invalidate("a")
	_, ok := c.get("a")
	assert.False(t, ok)
}

func TestDEKCache_Concurrent(t *testing.T) {
	c := newDEKCache(100, time.Hour)
	aead := newTestAEAD(t, 'k')
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			for j := 0; j < 1000; j++ {
				c.set("k", aead)
				_, _ = c.get("k")
			}
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	// No assertion beyond "no race": run with -race.
}

func TestDEKCache_CapacityOne(t *testing.T) {
	c := newDEKCache(1, time.Hour)
	c.set("a", newTestAEAD(t, 'A'))
	c.set("b", newTestAEAD(t, 'B'))

	_, okA := c.get("a")
	_, okB := c.get("b")
	assert.False(t, okA, "first entry should be evicted at capacity=1")
	assert.True(t, okB, "second entry should remain")
}

func TestDEKCache_CapacityZeroNormalisesToOne(t *testing.T) {
	// Non-positive capacities are normalised to 1 to avoid panics and
	// silently-empty caches.
	c := newDEKCache(0, time.Hour)
	aead := newTestAEAD(t, 'A')
	c.set("a", aead)
	v, ok := c.get("a")
	assert.True(t, ok)
	assert.Same(t, aead, v)
}
