package otelutil

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// TestInitTracer_Modes covers the three operator modes documented on
// InitTracer:
//
//   - Mode 2 (SDK disabled, no propagation-only): no provider, no propagator.
//   - Mode 3 (SDK disabled + propagation-only): real provider w/o exporter
//   - W3C propagator.
//   - Mode 1 (default, no envs): would attach OTLP exporter; we exercise the
//     hot path that DOESN'T require an actual collector by checking the
//     propagator + provider are both set after init. The exporter constructor
//     is lazy at first use, so init succeeds against an unreachable endpoint.
//
// Each subtest reinitialises global OTel state, so we restore the global
// providers + propagator at function-end to avoid cross-test pollution.
func TestInitTracer_Modes(t *testing.T) {
	// Snapshot globals so we can restore on exit.
	origTP := otel.GetTracerProvider()
	origProp := otel.GetTextMapPropagator()
	t.Cleanup(func() {
		otel.SetTracerProvider(origTP)
		otel.SetTextMapPropagator(origProp)
	})

	t.Run("fully silent (SDK_DISABLED=true, no propagation-only)", func(t *testing.T) {
		// Reset globals to known no-op state for the assertion.
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())

		t.Setenv("OTEL_SDK_DISABLED", "true")
		t.Setenv("OTEL_PROPAGATION_ONLY", "")

		shutdown, err := InitTracer(context.Background(), "test")
		require.NoError(t, err)
		require.NotNil(t, shutdown, "shutdown must be non-nil even in silent mode")
		// noopShutdown returns nil without error.
		assert.NoError(t, shutdown(context.Background()))

		// The global propagator must NOT have been overridden -- this is
		// the contract that keeps OTEL_SDK_DISABLED=true semantically
		// equivalent to "zero per-operation overhead."
		prop := otel.GetTextMapPropagator()
		emptyComposite := propagation.NewCompositeTextMapPropagator()
		assert.Equal(t, len(emptyComposite.Fields()), len(prop.Fields()),
			"silent mode must not install the W3C propagator; got fields=%v", prop.Fields())

		// Tracer provider stays no-op (we never call SetTracerProvider).
		_, span := otel.Tracer("test").Start(context.Background(), "silent")
		defer span.End()
		assert.False(t, span.SpanContext().IsValid(),
			"no-op provider must not produce a valid SpanContext in silent mode")
	})

	t.Run("propagation-only (SDK_DISABLED=true + PROPAGATION_ONLY=true)", func(t *testing.T) {
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())

		t.Setenv("OTEL_SDK_DISABLED", "true")
		t.Setenv("OTEL_PROPAGATION_ONLY", "true")

		shutdown, err := InitTracer(context.Background(), "test")
		require.NoError(t, err)
		require.NotNil(t, shutdown)
		t.Cleanup(func() { _ = shutdown(context.Background()) })

		// W3C propagator must be installed.
		prop := otel.GetTextMapPropagator()
		assert.Contains(t, prop.Fields(), "traceparent",
			"propagation-only mode must install the W3C propagator")

		// Real provider creates real spans that carry SpanContext
		// (which the propagator can inject).
		_, span := otel.Tracer("test").Start(context.Background(), "propagation-only")
		defer span.End()
		assert.True(t, span.SpanContext().IsValid(),
			"real provider in propagation-only mode must produce a valid SpanContext")
	})

	t.Run("default mode (no envs) installs propagator and provider", func(t *testing.T) {
		// Default mode would attach an OTLP gRPC exporter to whatever
		// OTEL_EXPORTER_OTLP_ENDPOINT points at (or the default
		// localhost:4317). Construction is lazy w.r.t. the actual
		// network call, so init succeeds even without a collector
		// reachable. We assert both the propagator and provider are
		// installed; we don't verify the exporter writes anywhere.
		otel.SetTracerProvider(noop.NewTracerProvider())
		otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())

		// Clear both envs to exercise the default branch.
		t.Setenv("OTEL_SDK_DISABLED", "")
		t.Setenv("OTEL_PROPAGATION_ONLY", "")

		shutdown, err := InitTracer(context.Background(), "test")
		require.NoError(t, err)
		require.NotNil(t, shutdown)
		// Shutdown drains the batch processor by talking to the OTLP
		// endpoint, which is unreachable in unit tests. Short ctx so
		// the test doesn't pay the default 10s gRPC dial timeout.
		t.Cleanup(func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_ = shutdown(shutdownCtx)
		})

		assert.Contains(t, otel.GetTextMapPropagator().Fields(), "traceparent")

		// trace.SpanContext should be valid (real provider).
		_, span := otel.Tracer("test").Start(context.Background(), "default")
		defer span.End()
		assert.True(t, span.SpanContext().IsValid(),
			"default mode must produce a valid SpanContext")
		// Cross-check: this is NOT the no-op provider.
		tp := otel.GetTracerProvider()
		_, isNoop := tp.(noop.TracerProvider)
		assert.False(t, isNoop, "default mode must replace the no-op global provider")
	})
}

// TestEnvTrue covers the small helper that handles the
// case-insensitive + whitespace-tolerant env-bool parsing.
func TestEnvTrue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  bool
	}{
		{"unset", "", false},
		{"true lowercase", "true", true},
		{"true uppercase", "TRUE", true},
		{"true mixed", "True", true},
		{"true with whitespace", "  true  ", true},
		{"false", "false", false},
		{"empty whitespace", "   ", false},
		{"unrelated", "yes", false},
		{"unrelated 1", "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("X_OTEL_TEST_ENV", tc.value)
			assert.Equal(t, tc.want, envTrue("X_OTEL_TEST_ENV"))
		})
	}
}

// Compile-time guard that trace.Tracer is the standard surface we expect
// to call after InitTracer. Catches a future api drift that would make
// the doc comments lie.
var _ trace.Tracer = otel.Tracer("compile-guard")
