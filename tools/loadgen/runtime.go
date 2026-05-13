package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/http/pprof"
	"os"
	"strings"
	"time"

	"github.com/Marz32onE/instrumentation-go/otel-nats/otelnats"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/hmchangw/chat/pkg/natsutil"
)

// Runtime owns the per-run infrastructure: NATS connections, JetStream
// client, connection pool, Prometheus metrics, and auxiliary HTTP servers
// (metrics scrape endpoint + optional pprof). It is created once per run
// and torn down via Close after the run finishes.
//
// Phase 0: basic wiring only. The Collector is a placeholder (empty
// preset label). executeRun constructs a preset-named Collector that
// shares the same Metrics registry for real metric accounting. Phase 2
// will collapse this into a single Collector passed at construction time.
type Runtime struct {
	cfg        config
	runID      string
	nc         *otelnats.Conn
	js         jetstream.JetStream
	pool       *ConnPool
	metrics    *Metrics
	collector  *Collector
	omission   *OmissionTracker
	metricsSrv *http.Server
	pprofSrv   *http.Server
	// lastSettle is set by SetLastSettle (called from executeRun) so that
	// Finalize can include the settle outcome in the artifact bundle.
	lastSettle SettleOutcome
}

// NewRuntime dials NATS, initialises JetStream, builds a single-connection
// pool (observer only), starts the metrics HTTP server, and optionally
// starts the pprof HTTP server. On any construction error the partially
// built Runtime is cleaned up before the error is returned.
func NewRuntime(ctx context.Context, cfg *config, runID string) (*Runtime, error) {
	_ = ctx // reserved for future use (e.g., dialling with a deadline)

	nc, err := natsutil.Connect(cfg.NatsURL, cfg.NatsCredsFile)
	if err != nil {
		return nil, fmt.Errorf("connect NATS: %w", err)
	}

	js, err := jetstream.New(nc.NatsConn())
	if err != nil {
		_ = nc.Drain()
		return nil, fmt.Errorf("init JetStream: %w", err)
	}

	// observer-only pool: single connection for reply/broadcast subscriptions.
	// executeRun builds the full pool (with data connections) using this same
	// nc.NatsConn() as the observer so no extra dial is needed.
	pool := &ConnPool{observer: nc.NatsConn()}

	metrics := NewMetrics()

	// Phase 0 placeholder: empty preset label. executeRun replaces this with
	// NewCollector(rt.Metrics(), p.Name) so metric labels carry the real preset.
	collector := NewCollector(metrics, "")

	rt := &Runtime{
		cfg:       *cfg,
		runID:     runID,
		nc:        nc,
		js:        js,
		pool:      pool,
		metrics:   metrics,
		collector: collector,
		omission:  NewOmissionTracker(metrics),
	}

	rt.metricsSrv = rt.startMetricsServer()
	if cfg.PProfAddr != "" {
		rt.pprofSrv = rt.startPprofServer()
	}
	return rt, nil
}

// startMetricsServer starts the Prometheus scrape endpoint in a goroutine.
// Returns nil when MetricsAddr is empty (disabled).
func (r *Runtime) startMetricsServer() *http.Server {
	if r.cfg.MetricsAddr == "" {
		return nil
	}
	srv := &http.Server{
		Addr:              r.cfg.MetricsAddr,
		Handler:           r.metrics.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("metrics server stopped", "error", err)
		}
	}()
	return srv
}

// startPprofServer starts the pprof debug endpoint in a goroutine.
// The handler is registered on a dedicated mux to avoid leaking debug
// endpoints onto any other server using http.DefaultServeMux.
func (r *Runtime) startPprofServer() *http.Server {
	pprofMux := http.NewServeMux()
	pprofMux.HandleFunc("/debug/pprof/", pprof.Index)
	pprofMux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	pprofMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	pprofMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	pprofMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{
		Addr:              r.cfg.PProfAddr,
		Handler:           pprofMux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("pprof server stopped", "error", err)
		}
	}()
	slog.Info("pprof server listening", "addr", r.cfg.PProfAddr)
	return srv
}

// Accessor methods. Phase 2 scenarios call these to obtain their
// dependencies from the Runtime rather than constructing them inline.

// NC returns the traced NATS connection opened at construction time.
func (r *Runtime) NC() *otelnats.Conn { return r.nc }

// JS returns the JetStream client. For the InjectCanonical path,
// executeRun constructs a separate JetStream client with async-publish
// opts; this accessor is used by all other callers.
func (r *Runtime) JS() jetstream.JetStream { return r.js }

// Pool returns the connection pool. The Phase 0 pool is observer-only;
// executeRun expands it to the full N-connection pool using NewConnPoolWithCreds.
func (r *Runtime) Pool() *ConnPool { return r.pool }

// Collector returns the placeholder Collector created at construction.
// executeRun builds a preset-named Collector (sharing the same Metrics)
// for real accounting. Phase 2 collapses this.
func (r *Runtime) Collector() *Collector { return r.collector }

// Metrics returns the Prometheus metrics bundle.
func (r *Runtime) Metrics() *Metrics { return r.metrics }

// Omission returns the coordinated-omission tracker shared across the run.
func (r *Runtime) Omission() *OmissionTracker { return r.omission }

// RunID returns the per-run correlation identifier.
func (r *Runtime) RunID() string { return r.runID }

// Settle delegates to the free Settle function, providing a method-based entry
// point for callers that hold a *Runtime. The settle phase probes sampleIDs
// via probe until all are visible or the deadline in sf expires.
func (r *Runtime) Settle(ctx context.Context, sf SettleFlags, sampleIDs []string, probe ProbeFn) (SettleOutcome, error) {
	return Settle(ctx, sf, sampleIDs, probe)
}

// Preflight is a stub in Phase 0. Phase 2 will drive SUT readiness checks
// through the Scenario interface here.
func (r *Runtime) Preflight(_ context.Context) error { return nil }

// SetLastSettle stores the settle outcome so Finalize can include it in
// the artifact bundle. Called by executeRun after the settle phase completes.
func (r *Runtime) SetLastSettle(s SettleOutcome) {
	r.lastSettle = s
}

// Finalize performs post-run filesystem work (artifact bundle creation).
// When cfg.RunsDir is empty, Finalize is a no-op.
//
// Phase 1b §1.8: writes the full artifact bundle to cfg.RunsDir/<run_id>/.
// A fresh context (independent of the signal-cancelled ctx passed by the
// caller) is used for file I/O so that artifact writes survive a
// SIGTERM-terminated run. This addresses the Phase 1b pre-condition from
// the Task 0.5 code review.
func (r *Runtime) Finalize(_ context.Context, summary *Summary) error {
	if r.cfg.RunsDir == "" {
		return nil // bundle writing disabled
	}
	// Use a fresh timeout context so artifact writes survive a signal-cancelled ctx.
	// (Phase 1b pre-condition from Task 0.5 code review.)
	writeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = writeCtx // reserved for future I/O that needs ctx

	b := r.buildBundle(summary)
	return WriteBundle(r.cfg.RunsDir, b)
}

// buildBundle assembles an ArtifactBundle from the current Runtime state
// and the supplied summary.
func (r *Runtime) buildBundle(summary *Summary) *ArtifactBundle {
	var s Summary
	if summary != nil {
		s = *summary
	}
	return &ArtifactBundle{
		RunID:           r.runID,
		Summary:         s,
		Histograms:      r.collector.ExportHistograms(),
		Settle:          r.lastSettle,
		FlagsRaw:        nil, // TODO Phase 1b.6: populate from run flags
		EnvRedacted:     RedactEnv(envToMap(os.Environ())),
		StdoutLog:       nil, // TODO Phase 1b.6: log-tee plumbing deferred
		StderrLog:       nil, // TODO Phase 1b.6: log-tee plumbing deferred
		MetricsProm:     r.scrapeMetrics(),
		TimeseriesJSONL: nil, // TODO Phase 1b stretch: per-second collector ticker
	}
}

// scrapeMetrics captures the current Prometheus state by serving a synthetic
// scrape request against the metrics registry. Returns nil on error (non-fatal).
func (r *Runtime) scrapeMetrics() []byte {
	rec := httptest.NewRecorder()
	r.metrics.Handler().ServeHTTP(rec, newScrapeRequest())
	if rec.Code != http.StatusOK {
		slog.Warn("metrics scrape returned non-200", "code", rec.Code)
		return nil
	}
	return rec.Body.Bytes()
}

// newScrapeRequest returns a minimal GET /metrics request for the synthetic
// Prometheus scrape in scrapeMetrics.
func newScrapeRequest() *http.Request {
	req, _ := http.NewRequestWithContext(
		context.Background(), http.MethodGet, "/metrics", bytes.NewReader(nil),
	)
	req.Header.Set("Accept", "text/plain")
	return req
}

// envToMap converts a []string of "KEY=VALUE" entries (as returned by
// os.Environ) into a map[string]string. Values may contain "=" characters;
// only the first "=" is used as the key/value separator.
func envToMap(environ []string) map[string]string {
	m := make(map[string]string, len(environ))
	for _, e := range environ {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}

// Close shuts down auxiliary HTTP servers, drains the connection pool,
// and drains the NATS connection. Ordering:
//   - HTTP servers (metrics + pprof) first — they serve traffic until the
//     run is fully done and their handlers don't depend on NATS.
//   - Pool drain — data connections are idle after gen.Run returns.
//   - NC drain — flushes any remaining outbound messages and closes the
//     observer connection (which the pool also points to).
//
// All shutdown errors are logged but not returned; the caller already has
// the run's exit code.
func (r *Runtime) Close() error {
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if r.metricsSrv != nil {
		_ = r.metricsSrv.Shutdown(shutCtx)
	}
	if r.pprofSrv != nil {
		_ = r.pprofSrv.Shutdown(shutCtx)
	}
	if r.pool != nil {
		// Drain data conns (if any); the observer (nc) is drained separately.
		for _, c := range r.pool.conns {
			if c != nil {
				_ = c.Drain()
			}
		}
	}
	if r.nc != nil {
		_ = r.nc.Drain()
	}
	return nil
}
