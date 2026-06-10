package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTTLCache_GetPutExpire(t *testing.T) {
	c := newTTLCache(2, 50*time.Millisecond)
	c.Put("a", "1")
	v, ok := c.Get("a")
	assert.True(t, ok)
	assert.Equal(t, "1", v)

	time.Sleep(60 * time.Millisecond)
	_, ok = c.Get("a")
	assert.False(t, ok)
}

func TestTTLCache_CapacityBound(t *testing.T) {
	c := newTTLCache(2, time.Minute)
	c.Put("a", "1")
	c.Put("b", "2")
	c.Put("c", "3")
	assert.LessOrEqual(t, c.len(), 2)
}
