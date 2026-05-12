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

// InitTracer registers a TracerProvider + TextMap propagator. Returns a
// shutdown function.
//
// Honors OTEL_SDK_DISABLED=true: when set, the TracerProvider is created
// WITHOUT any exporter so no spans are emitted off-host -- but the
// provider is still REAL, so tracer.Start produces real SpanContexts
// that the propagator can carry as `traceparent` to downstream
// services. The OTel-no-op fallback used previously prevented cross-
// process correlation because no-op spans inject nothing. Real-provider-
// without-exporter is the standard pattern for "I want correlation
// across services but no telemetry sink set up" (the e2e default).
//
// The TextMap propagator is registered unconditionally -- propagator
// and tracer provider are independent concerns: the provider creates
// spans, the propagator carries their context across boundaries.
func InitTracer(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.TraceContext{})

	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceNameKey.String(serviceName),
	))
	if err != nil {
		return nil, fmt.Errorf("resource: %w", err)
	}

	tpOpts := []trace.TracerProviderOption{trace.WithResource(res)}

	if !strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") {
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
