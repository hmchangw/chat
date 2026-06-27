// Package bucketmetrics publishes the configured Cassandra message-bucket
// window (MESSAGE_BUCKET_HOURS) as an OpenTelemetry observable gauge so that a
// drift between the services that write and read the bucketed history table is
// observable and alertable.
//
// The bucket key is floor(createdAt/window)*window, computed independently by
// each service from its own MESSAGE_BUCKET_HOURS. If message-worker writes at a
// 72h window while history-service reads at 48h, writes and reads target
// different Cassandra partitions and range reads silently return incomplete or
// empty results — Cassandra reports no error. The math is correct; the hazard
// is purely a config-drift / deploy-discipline gap, which a single cross-service
// gauge makes visible. Alert when the value diverges across scrape targets:
//
//	max(message_bucket_hours) != min(message_bucket_hours)
//
// Every service that reads or writes messages_by_room should Register this gauge
// at startup (after otelutil.InitMeter) with its own configured window.
package bucketmetrics

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/metric"
)

// Register installs an Int64 observable gauge named "message_bucket_hours" on m
// that reports the service's configured bucket window in hours. hours must be
// positive (matching the MESSAGE_BUCKET_HOURS >= 1 validation services already
// apply). The gauge re-observes the same constant on every scrape.
func Register(m metric.Meter, hours int) error {
	if hours < 1 {
		return fmt.Errorf("register message_bucket_hours gauge: hours must be positive, got %d", hours)
	}
	_, err := m.Int64ObservableGauge(
		"message_bucket_hours",
		metric.WithDescription("Configured Cassandra message-bucket window in hours (MESSAGE_BUCKET_HOURS). A value that differs across services means writes and reads target different partitions, silently hiding message history."),
		metric.WithUnit("h"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(hours))
			return nil
		}),
	)
	if err != nil {
		return fmt.Errorf("register message_bucket_hours gauge: %w", err)
	}
	return nil
}
