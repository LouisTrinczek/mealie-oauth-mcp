package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/obs"
)

type Registry struct {
	registry       *prometheus.Registry
	httpRequests   *prometheus.HistogramVec
	toolCalls      *prometheus.HistogramVec
	mealieRequests *prometheus.HistogramVec
	auditEvents    *prometheus.CounterVec
	activeSessions prometheus.Gauge
}

func New() *Registry {
	r := &Registry{
		registry: prometheus.NewRegistry(),
		httpRequests: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mealie_mcp_http_request_duration_seconds",
			Help:    "HTTP request duration by route pattern and status code.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "code"}),
		toolCalls: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mealie_mcp_tool_call_duration_seconds",
			Help:    "MCP tool call duration by tool and outcome.",
			Buckets: prometheus.DefBuckets,
		}, []string{"tool", "outcome"}),
		mealieRequests: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "mealie_mcp_mealie_request_duration_seconds",
			Help:    "Mealie API request duration by method and outcome.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "outcome"}),
		auditEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mealie_mcp_audit_events_total",
			Help: "Audit events by event name.",
		}, []string{"event"}),
		activeSessions: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mealie_mcp_active_sessions",
			Help: "Number of active sessions.",
		}),
	}
	r.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		r.httpRequests,
		r.toolCalls,
		r.mealieRequests,
		r.auditEvents,
		r.activeSessions,
	)
	return r
}

func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.registry, promhttp.HandlerOpts{})
}

func (r *Registry) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		recorder := obs.NewStatusRecorder(w)
		next.ServeHTTP(recorder, req)
		r.httpRequests.WithLabelValues(obs.Route(req), strconv.Itoa(recorder.Status)).Observe(time.Since(start).Seconds())
	})
}

func (r *Registry) ObserveTool(tool string, failed bool, elapsed time.Duration) {
	outcome := "ok"
	if failed {
		outcome = "error"
	}
	r.toolCalls.WithLabelValues(tool, outcome).Observe(elapsed.Seconds())
}

func (r *Registry) ObserveMealie(method, outcome string, elapsed time.Duration) {
	r.mealieRequests.WithLabelValues(method, outcome).Observe(elapsed.Seconds())
}

func (r *Registry) CountAuditEvent(event string) {
	r.auditEvents.WithLabelValues(event).Inc()
}

func (r *Registry) SetActiveSessions(n int64) {
	r.activeSessions.Set(float64(n))
}
