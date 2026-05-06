package atrest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDEKCache_GetSet(t *testing.T) {
	c := newDEKCache(3, time.Hour)
	v, ok := c.get("a")
	assert.False(t, ok)
	assert.Nil(t, v)

	c.set("a", []byte("DEK-A"))
	v, ok = c.get("a")
	assert.True(t, ok)
	assert.Equal(t, []byte("DEK-A"), v)
}

func TestDEKCache_LRUEviction(t *testing.T) {
	c := newDEKCache(2, time.Hour)
	c.set("a", []byte("A"))
	c.set("b", []byte("B"))
	_, _ = c.get("a")       // promote a
	c.set("c", []byte("C")) // evicts b (least-recent)

	_, okA := c.get("a")
	_, okB := c.get("b")
	_, okC := c.get("c")
	assert.True(t, okA)
	assert.False(t, okB)
	assert.True(t, okC)
}

func TestDEKCache_TTLExpiry(t *testing.T) {
	c := newDEKCache(10, 20*time.Millisecond)
	c.set("a", []byte("A"))
	time.Sleep(40 * time.Millisecond)
	_, ok := c.get("a")
	assert.False(t, ok)
}

func TestDEKCache_Invalidate(t *testing.T) {
	c := newDEKCache(10, time.Hour)
	c.set("a", []byte("A"))
	c.invalidate("a")
	_, ok := c.get("a")
	assert.False(t, ok)
}

func TestDEKCache_Concurrent(t *testing.T) {
	c := newDEKCache(100, time.Hour)
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(i int) {
			for j := 0; j < 1000; j++ {
				c.set("k", []byte{byte(i)})
				_, _ = c.get("k")
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	// No assertion beyond "no race": run with -race.
}
