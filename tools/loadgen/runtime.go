package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
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
	metricsSrv *http.Server
	pprofSrv   *http.Server
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

// RunID returns the per-run correlation identifier.
func (r *Runtime) RunID() string { return r.runID }

// Preflight is a stub in Phase 0. Phase 2 will drive SUT readiness checks
// through the Scenario interface here.
func (r *Runtime) Preflight(_ context.Context) error { return nil }

// Finalize performs post-run filesystem work (artifact bundle creation).
// Phase 0 stub: no-ops when cfg.RunsDir is empty (the field is not yet
// on the config struct — it is added in Phase 1b). Phase 1b §1.8 tightens
// this to write the full run bundle.
func (r *Runtime) Finalize(_ context.Context) error { return nil }

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
