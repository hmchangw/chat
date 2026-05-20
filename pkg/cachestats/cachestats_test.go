package cachestats

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newStats(t *testing.T) (*Stats, *prometheus.Registry) {
	t.Helper()
	reg := prometheus.NewRegistry()
	s := New(reg)
	return s, reg
}

func TestRegister_HitMissIncrementCounters(t *testing.T) {
	s, _ := newStats(t)
	r := s.Register("subscription", nil)
	r.Hit()
	r.Hit()
	r.Miss()

	hits, misses := r.Snapshot()
	assert.Equal(t, uint64(2), hits)
	assert.Equal(t, uint64(1), misses)
}

func TestRegister_NilSafeOnRecorder(t *testing.T) {
	var r *Recorder
	// must not panic
	r.Hit()
	r.Miss()
	hits, misses := r.Snapshot()
	assert.Equal(t, uint64(0), hits)
	assert.Equal(t, uint64(0), misses)
}

func TestRegister_PreCreatesLabeledSeries(t *testing.T) {
	s, reg := newStats(t)
	_ = s.Register("subscription", nil)

	// Series must exist with value 0 before any Hit/Miss is called.
	assert.Equal(t, float64(0), testutil.ToFloat64(s.hits.WithLabelValues("subscription")))
	assert.Equal(t, float64(0), testutil.ToFloat64(s.misses.WithLabelValues("subscription")))

	got, err := testutil.GatherAndCount(reg, "chat_cache_hits_total", "chat_cache_misses_total")
	require.NoError(t, err)
	assert.Equal(t, 2, got, "expected one hits + one misses series")
}

func TestRegister_SizeGaugeRegisteredWhenSizeFnProvided(t *testing.T) {
	s, reg := newStats(t)
	size := 0
	_ = s.Register("roommeta", func() int { return size })
	size = 42

	got, err := testutil.GatherAndCount(reg, "chat_cache_size")
	require.NoError(t, err)
	assert.Equal(t, 1, got, "expected exactly one chat_cache_size series")
}

func TestRegister_SizeGaugeOmittedWhenSizeFnNil(t *testing.T) {
	s, reg := newStats(t)
	_ = s.Register("subscription", nil)

	got, err := testutil.GatherAndCount(reg, "chat_cache_size")
	require.NoError(t, err)
	assert.Equal(t, 0, got)
}

func TestRegister_PanicsOnEmptyName(t *testing.T) {
	s, _ := newStats(t)
	assert.PanicsWithValue(t, "cachestats: empty cache name", func() {
		s.Register("", nil)
	})
}

func TestRegister_PanicsOnDuplicateName(t *testing.T) {
	s, _ := newStats(t)
	_ = s.Register("subscription", nil)
	defer func() {
		r := recover()
		require.NotNil(t, r)
		assert.True(t, strings.Contains(r.(string), "already registered"))
	}()
	s.Register("subscription", nil)
}

func TestNew_NilRegistererFallsBackToDefault(t *testing.T) {
	s := New(nil)
	t.Cleanup(func() {
		prometheus.DefaultRegisterer.Unregister(s.hits)
		prometheus.DefaultRegisterer.Unregister(s.misses)
	})
	require.NotNil(t, s)

	name := "test_default_registerer_" + t.Name()
	r := s.Register(name, nil)
	r.Hit()
	hits, _ := r.Snapshot()
	assert.Equal(t, uint64(1), hits)
}
