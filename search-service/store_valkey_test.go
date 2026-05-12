package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/valkeyutil"
)

// stubValkey is an in-memory stand-in for valkeyutil.Client — only the
// methods the valkeyCache actually uses are implemented.
type stubValkey struct {
	store    map[string]string
	getErr   error
	setErr   error
	lastTTL  time.Duration
	setCalls int
}

func newStubValkey() *stubValkey {
	return &stubValkey{store: map[string]string{}}
}

func (s *stubValkey) Get(_ context.Context, key string) (string, error) {
	if s.getErr != nil {
		return "", s.getErr
	}
	v, ok := s.store[key]
	if !ok {
		return "", valkeyutil.ErrCacheMiss
	}
	return v, nil
}

func (s *stubValkey) Set(_ context.Context, key, value string, ttl time.Duration) error {
	s.setCalls++
	s.lastTTL = ttl
	if s.setErr != nil {
		return s.setErr
	}
	s.store[key] = value
	return nil
}

func (s *stubValkey) Del(_ context.Context, keys ...string) error {
	for _, k := range keys {
		delete(s.store, k)
	}
	return nil
}

func (s *stubValkey) Close() error { return nil }

func TestValkeyCache_SetThenGet(t *testing.T) {
	ctx := context.Background()
	stub := newStubValkey()
	c := newValkeyCache(stub)

	// Pick a non-default TTL so a regression that silently swaps in
	// `time.Minute` (or drops the argument entirely) shows up in the
	// equality assertion below — not just `assert.Greater(0)`.
	wantTTL := 7*time.Minute + 42*time.Second
	require.NoError(t, c.SetRestricted(ctx, "alice", map[string]int64{"r1": 100}, wantTTL))

	// Round-trip the cached value through GetRestricted so this test
	// continues to exercise the read path.
	got, hit, err := c.GetRestricted(ctx, "alice")
	require.NoError(t, err)
	assert.True(t, hit)
	assert.Equal(t, map[string]int64{"r1": 100}, got)

	// TTL must reach the underlying redis client verbatim — neither
	// truncated, defaulted, nor silently overridden by valkeyutil. Without
	// this assertion the cache could leak entries forever (TTL=0) or
	// thrash hot keys with a too-short TTL.
	assert.Equal(t, 1, stub.setCalls, "exactly one Set must be issued")
	assert.Equal(t, wantTTL, stub.lastTTL,
		"valkeyCache must forward the caller's TTL to the underlying client unchanged")
}

func TestValkeyCache_GetMiss(t *testing.T) {
	c := newValkeyCache(newStubValkey())
	got, hit, err := c.GetRestricted(context.Background(), "nobody")
	require.NoError(t, err)
	assert.False(t, hit)
	assert.Nil(t, got)
}

func TestValkeyCache_GetTransportError(t *testing.T) {
	stub := newStubValkey()
	stub.getErr = errors.New("conn refused")
	c := newValkeyCache(stub)

	_, hit, err := c.GetRestricted(context.Background(), "alice")
	assert.False(t, hit)
	assert.Error(t, err)
}

func TestValkeyCache_SetError(t *testing.T) {
	stub := newStubValkey()
	stub.setErr = errors.New("disk full")
	c := newValkeyCache(stub)

	err := c.SetRestricted(context.Background(), "alice", map[string]int64{}, time.Minute)
	assert.Error(t, err)
}

func TestValkeyCache_SetNilMapBecomesEmpty(t *testing.T) {
	stub := newStubValkey()
	c := newValkeyCache(stub)

	require.NoError(t, c.SetRestricted(context.Background(), "alice", nil, time.Minute))
	// Read back the stored value — should be `{}` (marshalled empty map),
	// not `null`, so a subsequent cache hit returns an empty map rather
	// than a nil map that the handler would fall through on.
	assert.Equal(t, "{}", stub.store[restrictedKey("alice")])
}

func TestValkeyCache_GetJSONNullYieldsEmptyMap(t *testing.T) {
	stub := newStubValkey()
	stub.store[restrictedKey("alice")] = "null"
	c := newValkeyCache(stub)

	got, hit, err := c.GetRestricted(context.Background(), "alice")
	require.NoError(t, err)
	assert.True(t, hit)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

func TestRestrictedKey_Format(t *testing.T) {
	assert.Equal(t, "searchservice:restrictedrooms:alice", restrictedKey("alice"))
}
