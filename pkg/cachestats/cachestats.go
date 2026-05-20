// Package cachestats publishes hit/miss/size metrics for every cache
// in the owning service to a single unified Prometheus metric family
// (chat_cache_hits_total, chat_cache_misses_total, chat_cache_size),
// and optionally emits a periodic slog summary line per cache.
//
// The package exposes a Stats container rather than package-level
// state so each service owns its own registry and tests can use
// isolated *prometheus.Registry instances without cross-test bleed.
package cachestats

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// Stats owns the unified hit/miss CounterVecs and the per-cache
// Recorder registry for one process. Construct one per service.
type Stats struct {
	hits   *prometheus.CounterVec
	misses *prometheus.CounterVec

	reg prometheus.Registerer

	mu        sync.Mutex
	recorders map[string]*Recorder
}

// New constructs a Stats and registers the hit/miss vectors with reg.
// Pass prometheus.DefaultRegisterer in production; pass an isolated
// prometheus.NewRegistry() in tests. A nil reg falls back to the
// default registerer.
func New(reg prometheus.Registerer) *Stats {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	s := &Stats{
		hits: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "chat_cache_hits_total",
			Help: "Total cache hits, partitioned by cache name.",
		}, []string{"cache"}),
		misses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "chat_cache_misses_total",
			Help: "Total cache misses, partitioned by cache name.",
		}, []string{"cache"}),
		reg:       reg,
		recorders: map[string]*Recorder{},
	}
	reg.MustRegister(s.hits, s.misses)
	return s
}

// Recorder is the per-cache instrumentation handle. Hit and Miss each
// perform a single atomic increment on a pre-resolved Prometheus
// counter — no map lookup, no label resolution on the hot path.
type Recorder struct {
	name   string
	hits   prometheus.Counter
	misses prometheus.Counter
	sizeFn func() int
}

// Register returns a Recorder for the named cache. sizeFn may be nil
// for caches that cannot cheaply report length (e.g. Valkey-backed);
// when non-nil, a chat_cache_size{cache=name} GaugeFunc is registered
// and sampled only at scrape time.
//
// Panics on empty name or duplicate registration — both are
// programmer errors caught at startup.
func (s *Stats) Register(name string, sizeFn func() int) *Recorder {
	if name == "" {
		panic("cachestats: empty cache name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.recorders[name]; exists {
		panic("cachestats: cache already registered: " + name)
	}
	r := &Recorder{
		name:   name,
		hits:   s.hits.WithLabelValues(name),
		misses: s.misses.WithLabelValues(name),
		sizeFn: sizeFn,
	}
	// Touch both counters so the labeled series exist at scrape time
	// even before traffic arrives. Dashboards see 0, not "no data".
	r.hits.Add(0)
	r.misses.Add(0)
	if sizeFn != nil {
		gauge := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name:        "chat_cache_size",
			Help:        "Current size of an in-process cache, sampled at scrape time.",
			ConstLabels: prometheus.Labels{"cache": name},
		}, func() float64 { return float64(sizeFn()) })
		s.reg.MustRegister(gauge)
	}
	s.recorders[name] = r
	return r
}

// Hit increments the cache's hit counter. Safe for concurrent use.
// No-op on a nil receiver so callers and tests can omit a recorder
// without branching at the call site.
func (r *Recorder) Hit() {
	if r == nil {
		return
	}
	r.hits.Inc()
}

// Miss increments the cache's miss counter. See Hit for nil semantics.
func (r *Recorder) Miss() {
	if r == nil {
		return
	}
	r.misses.Inc()
}

// Snapshot returns the current counter values. Implemented by reading
// the Prometheus counters via prometheus.Metric.Write — allocates a
// dto.Metric, intentionally off the cache hot path. Used by tests and
// the periodic logger only.
func (r *Recorder) Snapshot() (hits, misses uint64) {
	if r == nil {
		return 0, 0
	}
	return readCounter(r.hits), readCounter(r.misses)
}

func readCounter(c prometheus.Counter) uint64 {
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		return 0
	}
	return uint64(m.GetCounter().GetValue())
}

// snapshotAll returns a stable slice of recorders. Used by the
// periodic logger.
func (s *Stats) snapshotAll() []*Recorder {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Recorder, 0, len(s.recorders))
	for _, r := range s.recorders {
		out = append(out, r)
	}
	return out
}

// StartLogger emits one slog.Info line per registered cache every
// interval, until ctx is cancelled OR the returned stop function is
// called. A non-positive interval starts no goroutine and returns a
// no-op stop. A nil logger defaults to slog.Default().
//
// Callers wire stop into their shutdown chain so the goroutine exits
// before the process terminates.
func (s *Stats) StartLogger(ctx context.Context, interval time.Duration, logger *slog.Logger) (stop func()) {
	if interval <= 0 {
		return func() {}
	}
	if logger == nil {
		logger = slog.Default()
	}
	loggerCtx, cancel := context.WithCancel(ctx)
	go s.runLogger(loggerCtx, interval, logger)
	return cancel
}

func (s *Stats) runLogger(ctx context.Context, interval time.Duration, logger *slog.Logger) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.emitLogLines(logger)
		}
	}
}

func (s *Stats) emitLogLines(logger *slog.Logger) {
	for _, r := range s.snapshotAll() {
		hits, misses := r.Snapshot()
		total := hits + misses
		var rate float64
		if total > 0 {
			rate = float64(hits) / float64(total)
		}
		attrs := []any{
			"cache", r.name,
			"hits", hits,
			"misses", misses,
			"hit_rate", rate,
		}
		if r.sizeFn != nil {
			attrs = append(attrs, "size", r.sizeFn())
		}
		logger.Info("cache stats", attrs...)
	}
}
