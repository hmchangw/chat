package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/cachestats"
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
	c := newValkeyCache(newStubValkey(), nil)

	require.NoError(t, c.SetRestricted(ctx, "alice", map[string]int64{"r1": 100}, time.Minute))
	got, hit, err := c.GetRestricted(ctx, "alice")
	require.NoError(t, err)
	assert.True(t, hit)
	assert.Equal(t, map[string]int64{"r1": 100}, got)
}

func TestValkeyCache_GetMiss(t *testing.T) {
	c := newValkeyCache(newStubValkey(), nil)
	got, hit, err := c.GetRestricted(context.Background(), "nobody")
	require.NoError(t, err)
	assert.False(t, hit)
	assert.Nil(t, got)
}

func TestValkeyCache_GetTransportError(t *testing.T) {
	stub := newStubValkey()
	stub.getErr = errors.New("conn refused")
	c := newValkeyCache(stub, nil)

	_, hit, err := c.GetRestricted(context.Background(), "alice")
	assert.False(t, hit)
	assert.Error(t, err)
}

func TestValkeyCache_SetError(t *testing.T) {
	stub := newStubValkey()
	stub.setErr = errors.New("disk full")
	c := newValkeyCache(stub, nil)

	err := c.SetRestricted(context.Background(), "alice", map[string]int64{}, time.Minute)
	assert.Error(t, err)
}

func TestValkeyCache_SetNilMapBecomesEmpty(t *testing.T) {
	stub := newStubValkey()
	c := newValkeyCache(stub, nil)

	require.NoError(t, c.SetRestricted(context.Background(), "alice", nil, time.Minute))
	// Read back the stored value — should be `{}` (marshalled empty map),
	// not `null`, so a subsequent cache hit returns an empty map rather
	// than a nil map that the handler would fall through on.
	assert.Equal(t, "{}", stub.store[restrictedKey("alice")])
}

func TestValkeyCache_GetJSONNullYieldsEmptyMap(t *testing.T) {
	stub := newStubValkey()
	stub.store[restrictedKey("alice")] = "null"
	c := newValkeyCache(stub, nil)

	got, hit, err := c.GetRestricted(context.Background(), "alice")
	require.NoError(t, err)
	assert.True(t, hit)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}

func TestRestrictedKey_Format(t *testing.T) {
	assert.Equal(t, "searchservice:restrictedrooms:alice", restrictedKey("alice"))
}

func TestValkeyCache_GetRestricted_RecordsHitOnReturn(t *testing.T) {
	ctx := context.Background()
	stats := cachestats.New(prometheus.NewRegistry())
	rec := stats.Register("restricted_rooms", nil)
	c := newValkeyCache(newStubValkey(), rec)

	require.NoError(t, c.SetRestricted(ctx, "alice", map[string]int64{"r1": 1}, time.Minute))
	_, hit, err := c.GetRestricted(ctx, "alice")
	require.NoError(t, err)
	require.True(t, hit)

	hits, misses := rec.Snapshot()
	assert.Equal(t, uint64(1), hits)
	assert.Equal(t, uint64(0), misses)
}

func TestValkeyCache_GetRestricted_RecordsMissOnCacheMiss(t *testing.T) {
	stats := cachestats.New(prometheus.NewRegistry())
	rec := stats.Register("restricted_rooms", nil)
	c := newValkeyCache(newStubValkey(), rec)

	_, hit, err := c.GetRestricted(context.Background(), "nobody")
	require.NoError(t, err)
	require.False(t, hit)

	hits, misses := rec.Snapshot()
	assert.Equal(t, uint64(0), hits)
	assert.Equal(t, uint64(1), misses)
}

func TestValkeyCache_GetRestricted_TransportErrorRecordsNothing(t *testing.T) {
	stub := newStubValkey()
	stub.getErr = errors.New("conn refused")

	stats := cachestats.New(prometheus.NewRegistry())
	rec := stats.Register("restricted_rooms", nil)
	c := newValkeyCache(stub, rec)

	_, hit, err := c.GetRestricted(context.Background(), "alice")
	require.Error(t, err)
	require.False(t, hit)

	hits, misses := rec.Snapshot()
	assert.Equal(t, uint64(0), hits, "transport errors do not count as hits")
	assert.Equal(t, uint64(0), misses, "transport errors do not count as misses")
}
