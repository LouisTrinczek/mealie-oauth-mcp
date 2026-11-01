package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/obs"
)

const instrumentationName = "github.com/louistrinczek/mealie-oauth-mcp"

type Registry struct {
	handler        http.Handler
	httpRequests   metric.Float64Histogram
	toolCalls      metric.Float64Histogram
	mealieRequests metric.Float64Histogram
	auditEvents    metric.Int64Counter
	activeSessions metric.Int64Gauge
}

func New() (*Registry, error) {
	scrapeRegistry := prometheus.NewRegistry()
	return newScraped(scrapeRegistry, scrapeRegistry)
}

func newScraped(registerer prometheus.Registerer, gatherer prometheus.Gatherer) (*Registry, error) {
	if err := registerRuntimeCollectors(registerer); err != nil {
		return nil, err
	}
	exporter, err := otelprometheus.New(
		otelprometheus.WithRegisterer(registerer),
		otelprometheus.WithoutScopeInfo(),
		otelprometheus.WithoutTargetInfo(),
	)
	if err != nil {
		return nil, fmt.Errorf("starting Prometheus exporter: %w", err)
	}
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter)).Meter(instrumentationName)
	return newInstruments(meter, promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{}))
}

func Discard() *Registry {
	return &Registry{
		handler:        http.NotFoundHandler(),
		httpRequests:   noop.Float64Histogram{},
		toolCalls:      noop.Float64Histogram{},
		mealieRequests: noop.Float64Histogram{},
		auditEvents:    noop.Int64Counter{},
		activeSessions: noop.Int64Gauge{},
	}
}

func registerRuntimeCollectors(registerer prometheus.Registerer) error {
	if err := registerer.Register(collectors.NewGoCollector()); err != nil {
		return fmt.Errorf("registering Go collector: %w", err)
	}
	if err := registerer.Register(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{})); err != nil {
		return fmt.Errorf("registering process collector: %w", err)
	}
	return nil
}

func newInstruments(meter metric.Meter, handler http.Handler) (*Registry, error) {
	httpRequests, httpErr := durationHistogram(meter, "mealie_mcp_http_request_duration", "HTTP request duration by route pattern and status code.")
	toolCalls, toolErr := durationHistogram(meter, "mealie_mcp_tool_call_duration", "MCP tool call duration by tool and outcome.")
	mealieRequests, mealieErr := durationHistogram(meter, "mealie_mcp_mealie_request_duration", "Mealie API request duration by method and outcome.")
	auditEvents, auditErr := meter.Int64Counter("mealie_mcp_audit_events", metric.WithDescription("Audit events by event name."))
	activeSessions, sessionsErr := meter.Int64Gauge("mealie_mcp_active_sessions", metric.WithDescription("Number of active sessions."))
	if err := errors.Join(httpErr, toolErr, mealieErr, auditErr, sessionsErr); err != nil {
		return nil, fmt.Errorf("creating metric instruments: %w", err)
	}
	return &Registry{
		handler:        handler,
		httpRequests:   httpRequests,
		toolCalls:      toolCalls,
		mealieRequests: mealieRequests,
		auditEvents:    auditEvents,
		activeSessions: activeSessions,
	}, nil
}

func durationHistogram(meter metric.Meter, name, description string) (metric.Float64Histogram, error) {
	return meter.Float64Histogram(name,
		metric.WithDescription(description),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10),
	)
}

func (r *Registry) Handler() http.Handler {
	return r.handler
}

func (r *Registry) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		recorder := obs.NewStatusRecorder(w)
		next.ServeHTTP(recorder, req)
		r.httpRequests.Record(req.Context(), time.Since(start).Seconds(), metric.WithAttributes(
			attribute.String("route", obs.Route(req)),
			attribute.String("code", strconv.Itoa(recorder.Status)),
		))
	})
}

func (r *Registry) ObserveTool(ctx context.Context, tool string, failed bool, elapsed time.Duration) {
	outcome := "ok"
	if failed {
		outcome = "error"
	}
	r.toolCalls.Record(ctx, elapsed.Seconds(), metric.WithAttributes(
		attribute.String("tool", tool),
		attribute.String("outcome", outcome),
	))
}

func (r *Registry) ObserveMealie(ctx context.Context, method, outcome string, elapsed time.Duration) {
	r.mealieRequests.Record(ctx, elapsed.Seconds(), metric.WithAttributes(
		attribute.String("method", method),
		attribute.String("outcome", outcome),
	))
}

func (r *Registry) CountAuditEvent(ctx context.Context, event string) {
	r.auditEvents.Add(ctx, 1, metric.WithAttributes(attribute.String("event", event)))
}

func (r *Registry) SetActiveSessions(ctx context.Context, n int64) {
	r.activeSessions.Record(ctx, n)
}
