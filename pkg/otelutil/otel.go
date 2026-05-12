package otelutil

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	promexporter "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// noopShutdown is the shutdown function returned when no TracerProvider
// is constructed (the fully-disabled mode).
func noopShutdown(context.Context) error { return nil }

// envTrue returns true when the named env var, after trimming whitespace and
// folding case, is "true".
func envTrue(name string) bool {
	return strings.EqualFold(strings.TrimSpace(os.Getenv(name)), "true")
}

// InitTracer registers a TracerProvider + TextMap propagator and returns
// a shutdown function. Three operator modes are supported:
//
//  1. Default (no env): real TracerProvider + OTLP gRPC exporter + W3C
//     propagator. Spans created, batched to the configured OTLP
//     endpoint. This is the production deployment.
//
//  2. OTEL_SDK_DISABLED=true (and OTEL_PROPAGATION_ONLY unset / false):
//     fully silent. No TracerProvider, no propagator, no span allocation.
//     Honors the literal OTel SDK contract -- operators who explicitly
//     disable the SDK get zero per-operation overhead.
//
//  3. OTEL_SDK_DISABLED=true AND OTEL_PROPAGATION_ONLY=true: a real
//     TracerProvider WITHOUT an exporter, plus the W3C propagator.
//     Spans are allocated and discarded after each operation; their
//     SpanContext flows in `traceparent` headers across services. This
//     is the e2e harness default: it gives cross-service correlation
//     without requiring an OTLP collector container.
//
// Why not collapse 2 and 3? Reviewer feedback (post-R3): operators who
// set OTEL_SDK_DISABLED=true expected zero overhead. Folding the
// propagation-only behavior under the same flag surprised them. A
// second knob preserves the literal SDK_DISABLED contract while still
// letting the e2e harness opt into propagation.
func InitTracer(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	sdkDisabled := envTrue("OTEL_SDK_DISABLED")
	propagationOnly := envTrue("OTEL_PROPAGATION_ONLY")

	// Mode 2: fully silent. No global propagator, no provider.
	if sdkDisabled && !propagationOnly {
		return noopShutdown, nil
	}

	otel.SetTextMapPropagator(propagation.TraceContext{})

	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceNameKey.String(serviceName),
	))
	if err != nil {
		return nil, fmt.Errorf("resource: %w", err)
	}

	tpOpts := []trace.TracerProviderOption{trace.WithResource(res)}

	// Mode 1: real exporter. Mode 3: no exporter, but real provider so
	// tracer.Start produces real SpanContexts the propagator can inject.
	if !sdkDisabled {
		exp, err := otlptracegrpc.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("otlp exporter: %w", err)
		}
		tpOpts = append(tpOpts, trace.WithBatcher(exp))
	}

	tp := trace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

// InitMeter creates a MeterProvider with Prometheus exporter.
// Returns a shutdown function.
func InitMeter(serviceName string) (func(context.Context) error, error) {
	exp, err := promexporter.New()
	if err != nil {
		return nil, fmt.Errorf("prometheus exporter: %w", err)
	}

	mp := metric.NewMeterProvider(metric.WithReader(exp))
	return mp.Shutdown, nil
}
