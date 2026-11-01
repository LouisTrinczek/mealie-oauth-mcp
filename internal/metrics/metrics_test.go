package metrics

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"
)

func newRegistry(t *testing.T) *Registry {
	t.Helper()
	registry, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return registry
}

type refusingRegisterer struct {
	prometheus.Registerer
	accepted int
}

func (r *refusingRegisterer) Register(c prometheus.Collector) error {
	if r.accepted == 0 {
		return errors.New("registration refused")
	}
	r.accepted--
	return r.Registerer.Register(c)
}

type refusingMeter struct {
	noop.Meter
}

func (refusingMeter) Int64Gauge(string, ...metric.Int64GaugeOption) (metric.Int64Gauge, error) {
	return nil, errors.New("instrument refused")
}

func TestNewScraped_RegistrationFailures(t *testing.T) {
	for _, tc := range []struct {
		accepted int
		want     string
	}{
		{accepted: 0, want: "registering Go collector"},
		{accepted: 1, want: "registering process collector"},
		{accepted: 2, want: "starting Prometheus exporter"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			registry := prometheus.NewRegistry()
			_, err := newScraped(&refusingRegisterer{Registerer: registry, accepted: tc.accepted}, registry)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestNewInstruments_InstrumentFailure(t *testing.T) {
	_, err := newInstruments(refusingMeter{}, http.NotFoundHandler())
	if err == nil || !strings.Contains(err.Error(), "creating metric instruments") {
		t.Errorf("error = %v, want it to contain creating metric instruments", err)
	}
}

func TestDiscard_RecordsNothingAndServesNoScrape(t *testing.T) {
	registry := Discard()
	registry.ObserveTool(t.Context(), "recipes_read", false, time.Millisecond)
	registry.ObserveMealie(t.Context(), "GET", "ok", time.Millisecond)
	registry.CountAuditEvent(t.Context(), "login_succeeded")
	registry.SetActiveSessions(t.Context(), 1)
	registry.Middleware(http.NotFoundHandler()).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

	rec := httptest.NewRecorder()
	registry.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("scrape status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func scrape(t *testing.T, r *Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	body, err := io.ReadAll(rec.Body)
	if err != nil {
		t.Fatalf("reading scrape: %v", err)
	}
	return string(body)
}

func TestMiddleware_LabelsByRoutePattern(t *testing.T) {
	registry := newRegistry(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /callback", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusFound)
	})
	handler := registry.Middleware(mux)

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/callback?code=SECRET-CODE&state=S", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nope/abc123", nil))

	body := scrape(t, registry)
	for _, want := range []string{
		`mealie_mcp_http_request_duration_seconds_count{code="302",route="GET /callback"} 1` + "\n",
		`mealie_mcp_http_request_duration_seconds_count{code="404",route="unmatched"} 1` + "\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q", want)
		}
	}
	for _, leaked := range []string{"SECRET-CODE", "abc123"} {
		if strings.Contains(body, leaked) {
			t.Errorf("scrape leaks %q", leaked)
		}
	}
}

func TestObservers(t *testing.T) {
	registry := newRegistry(t)
	registry.ObserveTool(t.Context(), "recipes_read", false, time.Millisecond)
	registry.ObserveTool(t.Context(), "recipes_read", true, time.Millisecond)
	registry.ObserveMealie(t.Context(), "GET", "server_error", time.Millisecond)
	registry.CountAuditEvent(t.Context(), "login_succeeded")
	registry.SetActiveSessions(t.Context(), 3)

	body := scrape(t, registry)
	for _, want := range []string{
		`mealie_mcp_tool_call_duration_seconds_count{outcome="ok",tool="recipes_read"} 1` + "\n",
		`mealie_mcp_tool_call_duration_seconds_count{outcome="error",tool="recipes_read"} 1` + "\n",
		`mealie_mcp_mealie_request_duration_seconds_count{method="GET",outcome="server_error"} 1` + "\n",
		`mealie_mcp_audit_events_total{event="login_succeeded"} 1` + "\n",
		`mealie_mcp_active_sessions 3` + "\n",
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q", want)
		}
	}
}

func TestNew_RegistriesAreIndependent(t *testing.T) {
	first := newRegistry(t)
	second := newRegistry(t)
	first.CountAuditEvent(t.Context(), "login_succeeded")

	if !strings.Contains(scrape(t, first), `mealie_mcp_audit_events_total{event="login_succeeded"} 1`+"\n") {
		t.Error("first registry missing its own count")
	}
	if strings.Contains(scrape(t, second), "login_succeeded") {
		t.Error("second registry shares counts with the first")
	}
}

func TestMiddleware_DefaultsToStatus200WhenHandlerNeverWritesHeader(t *testing.T) {
	registry := newRegistry(t)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /plain", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	registry.Middleware(mux).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/plain", nil))

	want := `mealie_mcp_http_request_duration_seconds_count{code="200",route="GET /plain"} 1` + "\n"
	if !strings.Contains(scrape(t, registry), want) {
		t.Errorf("scrape missing %q", want)
	}
}
