package service

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Counters for the silent-partial-failure paths. These register with the
// default Prometheus registry via promauto, so main.go's plain promhttp.Handler()
// exposes them on /metrics. Each path currently only emits a log line; a counter
// makes the degradation observable in aggregate (alertable), not just via log
// scraping. Label cardinality is bounded by the finite set of site IDs in
// ALL_SITE_IDS, so site/dest labels are safe.
var (
	metricEnrichmentDegraded = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "user_service_room_enrichment_degraded_total",
		Help: "Subscription room-info enrichments left unenriched because a per-site room-service RPC failed, partitioned by site.",
	}, []string{"site"})

	metricUnreadFallback = promauto.NewCounter(prometheus.CounterOpts{
		Name: "user_service_unread_count_fallback_total",
		Help: "Unread-count requests that fell back to the total active count because a site room-service RPC failed.",
	})

	metricOutboxPublishFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "user_service_outbox_publish_failures_total",
		Help: "Cross-site UserStatusUpdated outbox publishes that failed, partitioned by destination site.",
	}, []string{"dest"})
)
