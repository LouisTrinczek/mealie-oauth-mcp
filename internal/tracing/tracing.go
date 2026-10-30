package tracing

import (
	"context"
	"fmt"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/obs"
)

const (
	serviceName         = "mealie-oauth-mcp"
	instrumentationName = "github.com/louistrinczek/mealie-oauth-mcp"
)

type Exporter func(context.Context) (sdktrace.SpanExporter, error)

func OTLP(ctx context.Context) (sdktrace.SpanExporter, error) {
	return otlptracehttp.New(ctx)
}

func Setup(ctx context.Context, enabled bool, version string, export Exporter) (trace.TracerProvider, func(context.Context) error, error) {
	if !enabled {
		return noop.NewTracerProvider(), func(context.Context) error { return nil }, nil
	}
	exporter, err := export(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("starting OTLP exporter: %w", err)
	}
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version),
		)),
	)
	return provider, provider.Shutdown, nil
}

func Tracer(tp trace.TracerProvider) trace.Tracer {
	return tp.Tracer(instrumentationName)
}

func Middleware(tp trace.TracerProvider) func(http.Handler) http.Handler {
	tracer := Tracer(tp)
	propagator := propagation.TraceContext{}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parent := propagator.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			spanCtx, span := tracer.Start(parent, r.Method, trace.WithSpanKind(trace.SpanKindServer))
			defer span.End()

			served := r.WithContext(spanCtx)
			recorder := obs.NewStatusRecorder(w)
			next.ServeHTTP(recorder, served)

			route := obs.Route(served)
			span.SetName(route)
			span.SetAttributes(
				attribute.String("http.request.method", r.Method),
				attribute.String("http.route", route),
				attribute.Int("http.response.status_code", recorder.Status),
			)
			if recorder.Status >= http.StatusInternalServerError {
				span.SetStatus(codes.Error, http.StatusText(recorder.Status))
			}
		})
	}
}
