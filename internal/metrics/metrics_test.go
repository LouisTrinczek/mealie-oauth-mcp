package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

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
	registry := New()
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
	registry := New()
	registry.ObserveTool("recipes_read", false, time.Millisecond)
	registry.ObserveTool("recipes_read", true, time.Millisecond)
	registry.ObserveMealie("GET", "server_error", time.Millisecond)
	registry.CountAuditEvent("login_succeeded")
	registry.SetActiveSessions(3)

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
	first := New()
	second := New()
	first.CountAuditEvent("login_succeeded")

	if !strings.Contains(scrape(t, first), `mealie_mcp_audit_events_total{event="login_succeeded"} 1`+"\n") {
		t.Error("first registry missing its own count")
	}
	if strings.Contains(scrape(t, second), "login_succeeded") {
		t.Error("second registry shares counts with the first")
	}
}

func TestMiddleware_DefaultsToStatus200WhenHandlerNeverWritesHeader(t *testing.T) {
	registry := New()
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
