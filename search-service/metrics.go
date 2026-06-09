package main

import (
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/hmchangw/chat/pkg/errcode"
)

// All collectors register with the default Prometheus registry via
// promauto so a plain promhttp.Handler() exposes them on /metrics.
var (
	metricRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "search_service_requests_total",
		Help: "Total NATS request/reply invocations handled, partitioned by endpoint and terminal status.",
	}, []string{"kind", "status"})

	metricRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "search_service_request_duration_seconds",
		Help:    "End-to-end handler latency in seconds, from NATS request receipt to response emission.",
		Buckets: prometheus.DefBuckets,
	}, []string{"kind"})

	metricESDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "search_service_es_duration_seconds",
		Help:    "Elasticsearch _search call latency in seconds.",
		Buckets: prometheus.DefBuckets,
	})
)

// Per-kind handles for the request-path metrics. The `status` label on
// requests_total is resolved lazily (9 values × 4 kinds = 36 perms would
// clutter here); the duration handles are fully bound.
const (
	metricKindMessages = "messages"
	metricKindRooms    = "subscriptions"
	metricKindApps     = "apps"
	metricKindUsers    = "users"
)

var (
	durMessages = metricRequestDuration.WithLabelValues(metricKindMessages)
	durRooms    = metricRequestDuration.WithLabelValues(metricKindRooms)
	durApps     = metricRequestDuration.WithLabelValues(metricKindApps)
	durUsers    = metricRequestDuration.WithLabelValues(metricKindUsers)
)

// observeRequest captures a handler's total latency and terminal status.
// The status is classified at fire-time from the named `err` return, so
// late-bound error classification (wrapping, defer-assigned) is counted
// correctly. Usage:
//
//	func (h *handler) search(...) (resp *R, err error) {
//	    defer observeRequest(metricKindMessages, &err)()
//	    ...
//	}
func observeRequest(kind string, errPtr *error) func() {
	start := time.Now()
	dur := durFor(kind)
	return func() {
		dur.Observe(time.Since(start).Seconds())
		metricRequestsTotal.WithLabelValues(kind, statusLabel(*errPtr)).Inc()
	}
}

func observeES() func() {
	start := time.Now()
	return func() { metricESDuration.Observe(time.Since(start).Seconds()) }
}

// durFor falls back to the messages variant on an unknown label so a
// caller typo surfaces as misattributed metrics rather than a
// nil-observer panic at fire time.
func durFor(kind string) prometheus.Observer {
	switch kind {
	case metricKindRooms:
		return durRooms
	case metricKindApps:
		return durApps
	case metricKindUsers:
		return durUsers
	default:
		return durMessages
	}
}

// statusLabel maps a handler's returned error onto the requests_total
// `status` label. nil → "ok"; a non-empty *errcode.Error in the chain → its
// Code (one of the 8 canonical Codes below); everything else → "internal".
//
// The label set is pinned to keep Prometheus cardinality bounded — at most
// 9 × len(kinds) series. A non-canonical Code (e.g. a future Code constant
// added without updating this allowlist, or a foreign envelope on a federation
// path) collapses to "internal" rather than minting a fresh time series.
func statusLabel(err error) string {
	if err == nil {
		return "ok"
	}
	var ee *errcode.Error
	if errors.As(err, &ee) && ee.Code != "" {
		if _, ok := allowedStatusLabels[string(ee.Code)]; ok {
			return string(ee.Code)
		}
	}
	return string(errcode.CodeInternal)
}

// allowedStatusLabels pins the cardinality of the requests_total status label
// to the 8 canonical errcode Codes + "ok". Any label outside this set
// collapses to "internal" via statusLabel.
var allowedStatusLabels = map[string]struct{}{
	"ok":                                {},
	string(errcode.CodeBadRequest):      {},
	string(errcode.CodeUnauthenticated): {},
	string(errcode.CodeForbidden):       {},
	string(errcode.CodeNotFound):        {},
	string(errcode.CodeConflict):        {},
	string(errcode.CodeTooManyRequests): {},
	string(errcode.CodeUnavailable):     {},
	string(errcode.CodeInternal):        {},
}

func metricsHandler() http.Handler { return promhttp.Handler() }
