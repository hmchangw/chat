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
	// ThreadMessages counts published messages that carry a ThreadParentMessageID
	// (i.e., reply messages targeting a previously-published root). Label "preset"
	// mirrors the loadgen_published_total label for easy join in Grafana.
	ThreadMessages *prometheus.CounterVec
	// PublishedByRoomType counts successful publishes broken down by room type
	// ("channel" or "dm"). Separate from Published to avoid inflating that
	// counter's label cardinality. Only incremented when Preset.DMRatio > 0.
	PublishedByRoomType *prometheus.CounterVec
	// RAWLag records per-path read-after-write lag: first-visible − published.
	// Used by the raw-consistency scenario (Phase 3 §3.1) and consumed by the
	// v2 dashboard's RAW panels.
	RAWLag *prometheus.HistogramVec
	// RAWVisibilityWindow records per-path visibility window:
	// first-visible − last-not-visible. Companion to RAWLag.
	RAWVisibilityWindow *prometheus.HistogramVec
	// RoomOpen records per-leg latency for the room-open scenario (Phase 3 §3.14).
	// Label "leg" identifies the request (history, rooms_get, presence, read, restricted).
	RoomOpen *prometheus.HistogramVec
	// RoomOpenE2E records end-to-end (slowest-leg) latency for the room-open scenario.
	RoomOpenE2E prometheus.Histogram
	// MessageRead records the latency to send a MessageRead event, broken down by
	// room type (channel|dm). Used by the read-receipts scenario (Phase 3 §3.16).
	MessageRead *prometheus.HistogramVec
	// LargeRoomReceive records per-(message, recipient) receive latency for the
	// large-room-broadcast scenario (Phase 3 §3.2). Label "preset" identifies
	// the active preset (announce-room|firehose-room|bot-room).
	LargeRoomReceive *prometheus.HistogramVec
	// LargeRoomCompletion records per-message completion ratio at p99 receive-time
	// for the large-room-broadcast scenario (Phase 3 §3.2). Labels: "preset" and
	// "quantile" (p50|p99).
	LargeRoomCompletion *prometheus.GaugeVec
	// NotificationLag records publish→subscriber-receipt lag for the
	// notification-fanout scenario (Phase 3 §3.4). Label "channel" identifies
	// the delivery channel: "inapp" (Phase 3.4a, unconditional), "push" and
	// "email" (Phase 3.4b, gated on notif_routing_ready build tag).
	NotificationLag *prometheus.HistogramVec
	// SubsChurn records per-action latency for the subscription-churn scenario
	// (Phase 3 §3.7). Label "action" is "join" or "leave".
	SubsChurn *prometheus.HistogramVec
	// SubsChurnTotal counts subscription churn actions by action+outcome.
	// Labels: "action" (join|leave), "outcome" (ok|error).
	SubsChurnTotal *prometheus.CounterVec
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
		ThreadMessages: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "loadgen_thread_messages_total",
				Help: "Count of published messages that carry a ThreadParentMessageID (i.e., reply to a previous message).",
			},
			[]string{"preset"},
		),
		PublishedByRoomType: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "loadgen_published_by_room_type_total",
				Help: "Successful publishes broken down by preset and room type (channel|dm). Only incremented when Preset.DMRatio > 0.",
			},
			[]string{"preset", "room_type"},
		),
		RAWLag: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "loadgen_raw_lag_seconds",
			Help:    "Per-path read-after-write lag: first-visible - published.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14), // 1ms … ~16s
		}, []string{"path"}),
		RAWVisibilityWindow: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "loadgen_raw_visibility_window_seconds",
			Help:    "Per-path visibility window: first-visible - last-not-visible.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14),
		}, []string{"path"}),
		RoomOpen: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "loadgen_room_open_seconds",
			Help:    "Per-leg latency for room-open scenario (legs: history, rooms_get, presence, read, restricted).",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14), // 1ms … ~16s
		}, []string{"leg"}),
		RoomOpenE2E: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "loadgen_room_open_e2e_seconds",
			Help:    "End-to-end (slowest-leg) latency for room-open scenario.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14),
		}),
		MessageRead: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "loadgen_message_read_seconds",
			Help:    "Latency to send a MessageRead event, by room type (channel|dm).",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14), // 1ms … ~16s
		}, []string{"room_type"}),
		LargeRoomReceive: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "loadgen_largeroom_receive_latency_seconds",
			Help:    "Per-(message, recipient) receive latency for large-room-broadcast scenario.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14), // 1ms … ~16s
		}, []string{"preset"}),
		LargeRoomCompletion: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "loadgen_largeroom_completion_ratio",
			Help: "Per-message completion ratio at p99 receive-time. Quantile labels: p50|p99.",
		}, []string{"preset", "quantile"}),
		NotificationLag: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "loadgen_notification_lag_seconds",
			Help:    "Notification publish→subscriber-receipt lag, by channel (inapp|push|email).",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14), // 1ms … ~16s
		}, []string{"channel"}),
		SubsChurn: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "loadgen_subs_churn_seconds",
			Help:    "Per-action latency for subscription-churn scenario.",
			Buckets: prometheus.ExponentialBuckets(0.001, 2, 14), // 1ms … ~16s
		}, []string{"action"}),
		SubsChurnTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "loadgen_subs_churn_total",
			Help: "Count of subscription churn actions by action+outcome.",
		}, []string{"action", "outcome"}),
	}
	r.MustRegister(
		m.Published, m.PublishErrors,
		m.Requests, m.RequestErrors, m.RequestLatency,
		m.E1Latency, m.E2Latency,
		m.ConsumerPending, m.ConsumerAckPending, m.ConsumerRedelivered,
		m.RunInfo, m.LivenessProbes,
		m.OmissionDeficit,
		m.RunQuality,
		m.ThreadMessages,
		m.PublishedByRoomType,
		m.RAWLag, m.RAWVisibilityWindow,
		m.RoomOpen, m.RoomOpenE2E,
		m.MessageRead,
		m.LargeRoomReceive, m.LargeRoomCompletion,
		m.NotificationLag,
		m.SubsChurn, m.SubsChurnTotal,
	)
	return m
}

// Handler returns an http.Handler serving this metrics registry.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}
