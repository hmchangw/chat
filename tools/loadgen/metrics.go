package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds the Prometheus collectors used across loadgen components.
type Metrics struct {
	Registry            *prometheus.Registry
	Published           *prometheus.CounterVec
	PublishErrors       *prometheus.CounterVec
	Requests            *prometheus.CounterVec
	RequestErrors       *prometheus.CounterVec
	RequestLatency      *prometheus.HistogramVec
	E1Latency           *prometheus.HistogramVec
	E2Latency           *prometheus.HistogramVec
	ConsumerPending     *prometheus.GaugeVec
	ConsumerAckPending  *prometheus.GaugeVec
	ConsumerRedelivered *prometheus.GaugeVec
	// RunInfo is an info-style gauge set to 1 per run with the labels
	// (run_id, preset, scenario, start_unix). Grafana template variables
	// pick a run by run_id; alerts join other counters against this
	// metric to keep run identity available without inflating every
	// counter's label cardinality.
	RunInfo *prometheus.GaugeVec
	// LivenessProbes counts the mid-run liveness probe results.
	// Labels: result="ok"|"fail". Separate from Requests so the
	// watcher's own traffic doesn't pollute scenario measurements.
	LivenessProbes *prometheus.CounterVec
	// OmissionDeficit tracks the coordinated-omission dispatch deficit —
	// the gap between when a tick was intended and when it actually ran
	// (or was dropped). Label "dropped"="true"|"false" separates pool-
	// saturation drops from serviced-but-late ticks.
	OmissionDeficit *prometheus.HistogramVec
	// RunQuality is a gauge with label "verdict" (TRUSTED|DEGRADED|UNTRUSTED).
	// Exactly one label value is set to 1 at run finalization; the others
	// are set to 0 so the metric is always fully populated for Grafana
	// state panels.
	RunQuality *prometheus.GaugeVec
}

// NewMetrics constructs a dedicated Prometheus registry with all loadgen
// collectors registered. A dedicated registry avoids colliding with default
// Go/process collectors.
func NewMetrics() *Metrics {
	r := prometheus.NewRegistry()
	buckets := []float64{
		0.001, 0.002, 0.005, 0.010, 0.025, 0.050, 0.100, 0.250, 0.500, 1.000, 2.500, 5.000,
	}
	m := &Metrics{
		Registry: r,
		Published: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "loadgen_published_total", Help: "Messages published by preset, phase, conn_id, and rate_bucket."},
			[]string{"preset", "phase", "conn_id", "rate_bucket"},
		),
		// PHASE-LABEL NOTE: dashboards that sum loadgen_publish_errors_total must
		// now sum across phase= to preserve pre-v2 semantics. See CHANGES.md (Phase 1a).
		PublishErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "loadgen_publish_errors_total", Help: "Publish-side errors by preset, phase, and reason."},
			[]string{"preset", "phase", "reason"},
		),
		Requests: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "loadgen_requests_total", Help: "Request/reply requests issued by scenario+kind."},
			[]string{"preset", "scenario", "kind", "phase"},
		),
		// PHASE-LABEL NOTE: dashboards that sum loadgen_request_errors_total must
		// now sum across phase= to preserve pre-v2 semantics. See CHANGES.md (Phase 1a).
		RequestErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "loadgen_request_errors_total", Help: "Request/reply errors by scenario+kind+phase+reason."},
			[]string{"preset", "scenario", "kind", "phase", "reason"},
		),
		RequestLatency: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{Name: "loadgen_request_latency_seconds", Help: "Request/reply latency by scenario+kind.", Buckets: buckets},
			[]string{"preset", "scenario", "kind"},
		),
		E1Latency: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{Name: "loadgen_e1_latency_seconds", Help: "Gatekeeper ack latency.", Buckets: buckets},
			[]string{"preset"},
		),
		E2Latency: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{Name: "loadgen_e2_latency_seconds", Help: "Broadcast-visible latency.", Buckets: buckets},
			[]string{"preset"},
		),
		ConsumerPending: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "loadgen_consumer_pending", Help: "JetStream consumer num_pending."},
			[]string{"stream", "durable"},
		),
		ConsumerAckPending: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "loadgen_consumer_ack_pending", Help: "JetStream consumer num_ack_pending."},
			[]string{"stream", "durable"},
		),
		ConsumerRedelivered: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "loadgen_consumer_redelivered", Help: "JetStream consumer num_redelivered."},
			[]string{"stream", "durable"},
		),
		RunInfo: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{Name: "loadgen_run_info", Help: "Per-run identification (value always 1)."},
			[]string{"run_id", "preset", "scenario", "start_unix"},
		),
		LivenessProbes: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "loadgen_liveness_probes_total", Help: "Mid-run liveness probe results."},
			[]string{"preset", "result"},
		),
		OmissionDeficit: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "loadgen_omission_deficit_seconds",
				Help:    "Dispatch deficit between intended tick time and actual tick start (or drop time).",
				Buckets: prometheus.ExponentialBuckets(1e-5, 2, 18), // 10µs ... ~2.6s
			},
			[]string{"dropped"},
		),
		RunQuality: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "loadgen_run_quality",
			Help: "RUN QUALITY verdict; gauge of 1 for the active verdict, 0 otherwise.",
		}, []string{"verdict"}),
	}
	r.MustRegister(
		m.Published, m.PublishErrors,
		m.Requests, m.RequestErrors, m.RequestLatency,
		m.E1Latency, m.E2Latency,
		m.ConsumerPending, m.ConsumerAckPending, m.ConsumerRedelivered,
		m.RunInfo, m.LivenessProbes,
		m.OmissionDeficit,
		m.RunQuality,
	)
	return m
}

// Handler returns an http.Handler serving this metrics registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}
