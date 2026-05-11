// Package roomkeymetrics exposes OTel metric instruments for the room-key
// fan-out and inter-site RPC code paths shared by room-worker and inbox-worker.
package roomkeymetrics

import (
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

var (
	// FanoutErrors counts the number of failed RoomKeyEvent sends to a single account.
	FanoutErrors metric.Int64Counter
	// RPCDuration measures inter-site key-fetch RPC latency in seconds.
	RPCDuration metric.Float64Histogram
	// KeyGenerated counts the number of new keys generated for rooms.
	KeyGenerated metric.Int64Counter
	// KeyRotated counts the number of successful key rotations.
	KeyRotated metric.Int64Counter
	// ValkeyErrors counts Valkey operation failures, tagged by operation name.
	ValkeyErrors metric.Int64Counter
	// KeyAbsentErrors fires when Valkey is healthy but no current key exists for a room
	// (TTL expired, Valkey wipe, etc.). Distinct from ValkeyErrors which counts I/O failures.
	KeyAbsentErrors metric.Int64Counter
	// ReplicationTerminated counts inbox-worker messages that exceeded ROOM_KEY_MAX_REDELIVER
	// and were Acked (terminated) to prevent indefinite NAK-loop on unreachable origin.
	ReplicationTerminated metric.Int64Counter
)

func init() {
	m := otel.Meter("room-key")

	var err error
	FanoutErrors, err = m.Int64Counter(
		"room_key_fanout_errors_total",
		metric.WithDescription("Number of failed RoomKeyEvent sends to a single account"),
	)
	if err != nil {
		// Fall back to a no-op counter so the program continues to run even if
		// the global meter provider is not yet initialised at package init time.
		FanoutErrors, _ = noop.NewMeterProvider().Meter("room-key").Int64Counter("room_key_fanout_errors_total")
	}

	RPCDuration, err = m.Float64Histogram(
		"room_key_rpc_duration_seconds",
		metric.WithDescription("Inter-site key-fetch RPC duration"),
		metric.WithUnit("s"),
	)
	if err != nil {
		RPCDuration, _ = noop.NewMeterProvider().Meter("room-key").Float64Histogram("room_key_rpc_duration_seconds")
	}

	KeyGenerated, err = m.Int64Counter(
		"room_key_generated_total",
		metric.WithDescription("Number of new room encryption keys generated"),
	)
	if err != nil {
		KeyGenerated, _ = noop.NewMeterProvider().Meter("room-key").Int64Counter("room_key_generated_total")
	}

	KeyRotated, err = m.Int64Counter(
		"room_key_rotated_total",
		metric.WithDescription("Number of successful room key rotations"),
	)
	if err != nil {
		KeyRotated, _ = noop.NewMeterProvider().Meter("room-key").Int64Counter("room_key_rotated_total")
	}

	ValkeyErrors, err = m.Int64Counter(
		"room_key_valkey_errors_total",
		metric.WithDescription("Number of Valkey operation failures"),
	)
	if err != nil {
		ValkeyErrors, _ = noop.NewMeterProvider().Meter("room-key").Int64Counter("room_key_valkey_errors_total")
	}

	KeyAbsentErrors, err = m.Int64Counter(
		"room_key_absent_errors_total",
		metric.WithDescription("Number of times Valkey returned (nil, nil) for a room key — key absent, not a transient error"),
	)
	if err != nil {
		KeyAbsentErrors, _ = noop.NewMeterProvider().Meter("room-key").Int64Counter("room_key_absent_errors_total")
	}

	ReplicationTerminated, err = m.Int64Counter(
		"room_key_replication_terminated_total",
		metric.WithDescription("Number of inbox-worker messages terminated after exceeding ROOM_KEY_MAX_REDELIVER to prevent indefinite NAK-loop"),
	)
	if err != nil {
		ReplicationTerminated, _ = noop.NewMeterProvider().Meter("room-key").Int64Counter("room_key_replication_terminated_total")
	}
}
