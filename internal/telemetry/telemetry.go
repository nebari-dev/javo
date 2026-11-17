// Package telemetry is what the server tells about itself: structured logs,
// and OpenTelemetry traces and metrics.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// propagators read and write the W3C trace context and baggage headers
// (traceparent, tracestate, baggage).
var propagators = propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})

// NewLogger returns a logger writing to w as format ("json" or "text"),
// records at level and above. A record logged with the context of a
// request has the request's trace_id and span_id.
func NewLogger(w io.Writer, format string, level slog.Level) (*slog.Logger, error) {
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	switch format {
	case "json":
		h = slog.NewJSONHandler(w, opts)
	case "text":
		h = slog.NewTextHandler(w, opts)
	default:
		return nil, fmt.Errorf("%q is not a log format: json or text", format)
	}
	return slog.New(traced{h}), nil
}

// traced adds the trace and span of a record's context to the record.
type traced struct{ slog.Handler }

func (t traced) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return t.Handler.Handle(ctx, r)
}

func (t traced) WithAttrs(as []slog.Attr) slog.Handler { return traced{t.Handler.WithAttrs(as)} }
func (t traced) WithGroup(name string) slog.Handler    { return traced{t.Handler.WithGroup(name)} }

// Handler wraps h so each request is a span, continuing the trace of a
// caller that sent one, and counts towards the HTTP server metrics. With
// Start not sending anywhere, the span still carries the caller's trace,
// so log lines name it.
func Handler(h http.Handler) http.Handler {
	return otelhttp.NewHandler(h, "javo", otelhttp.WithPropagators(propagators))
}

// Start sends traces and metrics over OTLP/HTTP to the collector the
// standard environment names (OTEL_EXPORTER_OTLP_ENDPOINT, or the
// _TRACES_ and _METRICS_ ones; headers, timeouts and the rest from their
// OTEL_EXPORTER_OTLP_* variables too). With none set, or
// OTEL_SDK_DISABLED=true, it sends nothing. The service is named service
// at version unless OTEL_SERVICE_NAME or OTEL_RESOURCE_ATTRIBUTES say
// otherwise. stop sends what is left and stops.
func Start(ctx context.Context, service, version string) (stop func(context.Context) error, err error) {
	off := func(context.Context) error { return nil }
	if os.Getenv("OTEL_SDK_DISABLED") == "true" || !anySet("OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT") {
		return off, nil
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(attribute.String("service.name", service), attribute.String("service.version", version)),
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		return off, fmt.Errorf("telemetry resource: %w", err)
	}
	spans, err := otlptracehttp.New(ctx)
	if err != nil {
		return off, fmt.Errorf("OTLP trace exporter: %w", err)
	}
	metrics, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return off, errors.Join(fmt.Errorf("OTLP metric exporter: %w", err), spans.Shutdown(ctx))
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(spans), sdktrace.WithResource(res))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metrics)), sdkmetric.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	otel.SetTextMapPropagator(propagators)
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { slog.Warn("telemetry", "error", err) }))
	return func(ctx context.Context) error { return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx)) }, nil
}

func anySet(names ...string) bool {
	for _, n := range names {
		if os.Getenv(n) != "" {
			return true
		}
	}
	return false
}
