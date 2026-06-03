package atrest

import (
	"crypto/cipher"
	"fmt"
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

// TestDEKCache_HotEntrySurvivesScan exercises the 2Q-specific
// behaviour we picked the algorithm for: an entry that has been
// accessed at least twice (promoted to the "frequent" list) survives a
// burst of one-shot inserts that would have evicted it under plain
// LRU. This is the access pattern that motivates the swap — a small
// set of hot rooms vs. a long tail of cold ones.
func TestDEKCache_HotEntrySurvivesScan(t *testing.T) {
	c := newDEKCache(8, time.Hour)
	hot := newTestAEAD(t, 'H')

	// Touch "hot" twice so 2Q promotes it from recent → frequent.
	c.set("hot", hot)
	_, ok := c.get("hot")
	require.True(t, ok)

	// Inject a scan of cold one-shot entries far larger than the cache
	// can hold. Under LRU "hot" would be evicted long before we finish;
	// under 2Q it lives in the frequent list and survives.
	for i := 0; i < 100; i++ {
		c.set(fmt.Sprintf("cold-%d", i), newTestAEAD(t, byte(i)))
	}

	v, ok := c.get("hot")
	assert.True(t, ok, "hot entry must survive a one-shot scan under 2Q")
	assert.Same(t, hot, v)
}

// TestDEKCache_TinyCapacity_NoPanic exercises capacities below the
// underlying 2Q's minimum (it can't build with capacity < 2 because its
// internal ghost LRU would have size zero). newDEKCache normalises to
// the minimum; the cache must still satisfy basic round-trip even if
// it can hold fewer entries than the caller asked for.
func TestDEKCache_TinyCapacity_NoPanic(t *testing.T) {
	for _, cap := range []int{-1, 0, 1, 2} {
		c := newDEKCache(cap, time.Hour)
		aead := newTestAEAD(t, 'A')
		c.set("a", aead)
		v, ok := c.get("a")
		assert.True(t, ok, "capacity %d: just-stored entry must be retrievable", cap)
		assert.Same(t, aead, v)
	}
}
