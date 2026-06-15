package service

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Counters for silent-partial-failure paths; site/dest label cardinality is bounded by ALL_SITE_IDS.
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
