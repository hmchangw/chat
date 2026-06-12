package main

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// metrics holds the connector's observability instruments. For a single-replica
// lossless pump that fails softly (retry-forever), these — especially lag and
// the publish-error/throughput counters — are how a stall is detected before
// the source position ages out of the oplog. All methods are nil-safe so unit
// tests can run without an initialized meter.
type metrics struct {
	published metric.Int64Counter
	errors    metric.Int64Counter
	skipped   metric.Int64Counter
	lagMs     metric.Int64Gauge
}

func newMetrics() (*metrics, error) {
	m := otel.Meter("oplog-connector")
	published, err := m.Int64Counter("oplog_events_published_total",
		metric.WithDescription("CDC events published to MIGRATION_OPLOG, by collection"))
	if err != nil {
		return nil, fmt.Errorf("published counter: %w", err)
	}
	errs, err := m.Int64Counter("oplog_publish_errors_total",
		metric.WithDescription("publish attempts that failed and were retried, by collection"))
	if err != nil {
		return nil, fmt.Errorf("errors counter: %w", err)
	}
	skipped, err := m.Int64Counter("oplog_events_skipped_total",
		metric.WithDescription("poison events skipped (malformed or no dedup id), by collection"))
	if err != nil {
		return nil, fmt.Errorf("skipped counter: %w", err)
	}
	lag, err := m.Int64Gauge("oplog_replication_lag_ms",
		metric.WithDescription("now - clusterTime at publish, by collection"))
	if err != nil {
		return nil, fmt.Errorf("lag gauge: %w", err)
	}
	return &metrics{published: published, errors: errs, skipped: skipped, lagMs: lag}, nil
}

func collAttr(coll string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("collection", coll))
}

func (m *metrics) onPublished(ctx context.Context, coll string, lagMs int64) {
	if m == nil {
		return
	}
	m.published.Add(ctx, 1, collAttr(coll))
	m.lagMs.Record(ctx, lagMs, collAttr(coll))
}

func (m *metrics) onPublishError(ctx context.Context, coll string) {
	if m == nil {
		return
	}
	m.errors.Add(ctx, 1, collAttr(coll))
}

func (m *metrics) onSkipped(ctx context.Context, coll string) {
	if m == nil {
		return
	}
	m.skipped.Add(ctx, 1, collAttr(coll))
}
