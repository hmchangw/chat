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

// envSet reports whether the named env var is non-empty after trimming
// whitespace. Used to distinguish "operator explicitly set this knob" from
// "operator left it unset" -- the two cases now have different defaults
// for OTEL_PROPAGATION_ONLY (see InitTracer).
func envSet(name string) bool {
	return strings.TrimSpace(os.Getenv(name)) != ""
}

// upgradeWarnOnce makes the SDK_DISABLED-upgrade slog.Warn fire at most
// once per process even if InitTracer is called multiple times (e.g.
// tests or services that reinit after config reload). Pointer-valued
// so tests can swap in a fresh gate without copying a sync.Once.
var upgradeWarnOnce = &sync.Once{}

// InitTracer registers a TracerProvider + TextMap propagator and returns
// a shutdown function. The operator modes are:
//
//  1. Default (no env): real TracerProvider + OTLP gRPC exporter + W3C
//     propagator. Spans created, batched to the configured OTLP
//     endpoint. This is the production deployment.
//
//  2. OTEL_SDK_DISABLED=true AND OTEL_PROPAGATION_ONLY=false (explicit):
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
//  4. OTEL_SDK_DISABLED=true AND OTEL_PROPAGATION_ONLY UNSET: same
//     behavior as mode 3 (propagation-only), but emits a one-time
//     slog.Warn telling the operator to set OTEL_PROPAGATION_ONLY
//     explicitly. This preserves the pre-split behavior for deployments
//     that set only OTEL_SDK_DISABLED=true under earlier versions --
//     otherwise the upgrade would silently drop traceparent.
func InitTracer(ctx context.Context, serviceName string) (func(context.Context) error, error) {
	sdkDisabled := envTrue("OTEL_SDK_DISABLED")
	propagationOnlySet := envSet("OTEL_PROPAGATION_ONLY")
	propagationOnly := envTrue("OTEL_PROPAGATION_ONLY")

	// Mode 4: legacy upgrade path. Operator set SDK_DISABLED=true but
	// did NOT explicitly set PROPAGATION_ONLY. Default to propagation-only
	// (the pre-split behavior) and emit a one-time warning so the operator
	// makes an explicit choice on next deploy.
	if sdkDisabled && !propagationOnlySet {
		upgradeWarnOnce.Do(func() {
			slog.Warn("OTEL_SDK_DISABLED=true with OTEL_PROPAGATION_ONLY unset; defaulting to propagation-only mode for backward compatibility. Set OTEL_PROPAGATION_ONLY=true to silence this warning, or OTEL_PROPAGATION_ONLY=false for fully-silent (zero-overhead) semantics.")
		})
		propagationOnly = true
	}

	// Mode 2: fully silent. No global propagator, no provider. Return
	// BEFORE touching any global state.
	if sdkDisabled && !propagationOnly {
		return noopShutdown, nil
	}

	// Modes 1, 3, 4: build the provider first. Only install the global
	// propagator after all fallible setup succeeds, so a resource.New or
	// exporter failure can't leave a partially-initialised global state
	// (propagator set, provider not).
	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceNameKey.String(serviceName),
	))
	if err != nil {
		return nil, fmt.Errorf("resource: %w", err)
	}

	tpOpts := []trace.TracerProviderOption{trace.WithResource(res)}

	// Mode 1: real exporter. Modes 3 & 4: no exporter, but real provider so
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
