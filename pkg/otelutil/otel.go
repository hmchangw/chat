package otelutil

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"

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

func envSet(name string) bool {
	return strings.TrimSpace(os.Getenv(name)) != ""
}

// Pointer so tests can swap in a fresh gate.
var upgradeWarnOnce = &sync.Once{}

// InitTracer registers a TracerProvider + TextMap propagator. Modes:
//  1. default          → SDK + OTLP exporter + propagator
//  2. SDK_DISABLED + PROPAGATION_ONLY=false → fully silent
//  3. SDK_DISABLED + PROPAGATION_ONLY=true  → propagator only, no exporter
//  4. SDK_DISABLED + PROPAGATION_ONLY unset → mode 3 + one-time warn
func InitTracer(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	sdkDisabled := envTrue("OTEL_SDK_DISABLED")
	propagationOnlySet := envSet("OTEL_PROPAGATION_ONLY")
	propagationOnly := envTrue("OTEL_PROPAGATION_ONLY")

	if sdkDisabled && !propagationOnlySet {
		upgradeWarnOnce.Do(func() {
			slog.Warn("OTEL_SDK_DISABLED=true with OTEL_PROPAGATION_ONLY unset; defaulting to propagation-only. Set OTEL_PROPAGATION_ONLY=true to silence, =false for fully-silent semantics.")
		})
		propagationOnly = true
	}

	if sdkDisabled && !propagationOnly {
		return noopShutdown, nil
	}

	// Build provider before touching globals so a fallible setup can't leave a partial state.
	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceNameKey.String(serviceName),
	))
	if err != nil {
		return nil, fmt.Errorf("resource: %w", err)
	}

	tpOpts := []trace.TracerProviderOption{trace.WithResource(res)}

	if !sdkDisabled {
		exp, err := otlptracegrpc.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("otlp exporter: %w", err)
		}
		tpOpts = append(tpOpts, trace.WithBatcher(exp))
	}

	tp := trace.NewTracerProvider(tpOpts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})

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
