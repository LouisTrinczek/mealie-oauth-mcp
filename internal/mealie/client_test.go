package mealie

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/metrics"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
)

const testJWT = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2lnbmF0dXJl"

func respond(w http.ResponseWriter, body string) {
	_, _ = io.WriteString(w, body)
}

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	c := New(base, Options{})
	c.backoff = []time.Duration{time.Millisecond, time.Millisecond}
	return c
}

func TestClient_RetriesGETOnly(t *testing.T) {
	var gets, posts atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			if gets.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			respond(w, `{"version":"v3.23.0","enableOidc":true}`)
			return
		}
		posts.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	if _, err := c.About(context.Background()); err != nil {
		t.Fatalf("About: %v", err)
	}
	if got := gets.Load(); got != 2 {
		t.Errorf("GET attempts = %d, want 2", got)
	}

	err := c.do(context.Background(), secret.Secret{}, http.MethodPost, "/api/x", nil, nil, nil)
	if err == nil {
		t.Fatal("POST returned no error")
	}
	if got := posts.Load(); got != 1 {
		t.Errorf("POST attempts = %d, want 1", got)
	}
}

func TestClient_GiveUpAfterTwoRetries(t *testing.T) {
	var gets atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	_, err := c.About(context.Background())
	if !IsUnavailable(err) {
		t.Fatalf("err = %v, want unavailable", err)
	}
	if got := gets.Load(); got != 3 {
		t.Errorf("attempts = %d, want 3", got)
	}
}

func TestClient_RetryRespectsContext(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	c.backoff = []time.Duration{time.Hour, time.Hour}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.About(ctx)
	if err == nil {
		t.Fatal("About returned no error")
	}
	if time.Since(start) > 5*time.Second {
		t.Error("retry ignored context cancellation")
	}
}

func TestClient_NoRetryOn4xx(t *testing.T) {
	var gets atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gets.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	if _, err := c.About(context.Background()); err == nil {
		t.Fatal("About returned no error")
	}
	if got := gets.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
}

func TestClient_SendsUserAgentAndKeepsBasePath(t *testing.T) {
	var gotUA, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.UserAgent()
		gotPath = r.URL.Path
		respond(w, `{}`)
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL + "/mealie")

	if _, err := New(base, Options{}).About(context.Background()); err != nil {
		t.Fatalf("About: %v", err)
	}
	if gotUA != "mealie-oauth-mcp/dev" {
		t.Errorf("User-Agent = %q", gotUA)
	}
	if gotPath != "/mealie/api/app/about" {
		t.Errorf("path = %q", gotPath)
	}

	if _, err := New(base, Options{UserAgent: "custom/1"}).About(context.Background()); err != nil {
		t.Fatalf("About: %v", err)
	}
	if gotUA != "custom/1" {
		t.Errorf("User-Agent = %q", gotUA)
	}
}

func TestClient_DoesNotSendAuthorizationWithoutToken(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.Header["Authorization"]; ok {
			t.Error("Authorization header sent without a token")
		}
		respond(w, `{}`)
	}))
	if _, err := c.About(context.Background()); err != nil {
		t.Fatalf("About: %v", err)
	}
}

func TestClient_LimitsResponseBody(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respond(w, `{"version":"`+strings.Repeat("a", maxResponseBytes+10)+`"}`)
	}))
	if _, err := c.About(context.Background()); err == nil {
		t.Fatal("oversized body decoded without error")
	}
}

func TestClient_RejectsMalformedBody(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respond(w, `not json`)
	}))
	if _, err := c.About(context.Background()); err == nil {
		t.Fatal("malformed body decoded without error")
	}
}

func TestClient_TransportFailureIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base, _ := url.Parse(srv.URL)
	srv.Close()
	c := New(base, Options{})
	c.backoff = nil
	_, err := c.About(context.Background())
	if !IsUnavailable(err) {
		t.Errorf("err = %v, want unavailable", err)
	}
}

func TestAPIError_422Summarized(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		respond(w, `{"detail":[{"loc":["body","a"],"msg":"first"},{"msg":"second"},{"msg":"third"},{"msg":"fourth"}]}`)
	}))
	err := c.do(context.Background(), secret.Secret{}, http.MethodPost, "/api/x", nil, nil, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Status != http.StatusUnprocessableEntity {
		t.Errorf("Status = %d", apiErr.Status)
	}
	if apiErr.Message != "first; second; third" {
		t.Errorf("Message = %q", apiErr.Message)
	}
}

func TestAPIError_DetailTruncated(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		respond(w, fmt.Sprintf(`{"detail":%q}`, strings.Repeat("é", 500)))
	}))
	_, err := c.About(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if got := len([]rune(apiErr.Message)); got != maxMessageRunes {
		t.Errorf("Message length = %d runes, want %d", got, maxMessageRunes)
	}
}

func TestAPIError_FallsBackToStatusText(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		respond(w, `<html>nope</html>`)
	}))
	_, err := c.About(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Message != "Forbidden" {
		t.Errorf("Message = %q", apiErr.Message)
	}
}

func TestAPIError_RedactsTokens(t *testing.T) {
	bodies := map[string]string{
		"bearer string detail": `{"detail":"bad header Bearer abc.def and ` + testJWT + `"}`,
		"jwt in 422 msg":       `{"detail":[{"msg":"got ` + testJWT + `"},{"msg":"Bearer abc.def"}]}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			status := http.StatusBadRequest
			if strings.HasPrefix(body, `{"detail":[`) {
				status = http.StatusUnprocessableEntity
			}
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				respond(w, body)
			}))
			_, err := c.About(context.Background())
			if err == nil {
				t.Fatal("About returned no error")
			}
			for _, leaked := range []string{"abc.def", "eyJ", "Bearer"} {
				if strings.Contains(err.Error(), leaked) {
					t.Errorf("error leaks %q: %v", leaked, err)
				}
			}
			if !strings.Contains(err.Error(), "[REDACTED]") {
				t.Errorf("error lacks redaction marker: %v", err)
			}
		})
	}
}

func TestRedact(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"bearer", "Authorization: Bearer abc.def", "Authorization: [REDACTED]"},
		{"bearer lowercase", "bearer   xyz", "[REDACTED]"},
		{"jwt", "token " + testJWT + " end", "token [REDACTED] end"},
		{"jwt empty signature", "eyJhbGci.eyJzdWIi. x", "[REDACTED] x"},
		{"clean", "nothing to hide", "nothing to hide"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redact(tt.in); got != tt.want {
				t.Errorf("redact(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsUnauthorized(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"401", &APIError{Status: 401}, true},
		{"wrapped 401", fmt.Errorf("x: %w", &APIError{Status: 401}), true},
		{"403", &APIError{Status: 403}, false},
		{"nil", nil, false},
		{"other", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUnauthorized(tt.err); got != tt.want {
				t.Errorf("IsUnauthorized = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsUnavailable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"500", &APIError{Status: 500}, true},
		{"503", &APIError{Status: 503}, true},
		{"404", &APIError{Status: 404}, false},
		{"deadline", context.DeadlineExceeded, true},
		{"wrapped deadline", fmt.Errorf("x: %w", context.DeadlineExceeded), true},
		{"wrapped sentinel", fmt.Errorf("x: %w", ErrUnavailable), true},
		{"net error", &url.Error{Op: "Get", URL: "http://x", Err: errors.New("connection refused")}, true},
		{"canceled", context.Canceled, false},
		{"canceled url error", &url.Error{Op: "Get", URL: "http://x", Err: context.Canceled}, false},
		{"nil", nil, false},
		{"other", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsUnavailable(tt.err); got != tt.want {
				t.Errorf("IsUnavailable = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAPIError_Error(t *testing.T) {
	got := (&APIError{Status: 404, Message: "Not Found"}).Error()
	if got != "mealie: status 404: Not Found" {
		t.Errorf("Error() = %q", got)
	}
}

func TestClient_SendsQuery(t *testing.T) {
	var gotQuery url.Values
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		respond(w, `{}`)
	}))
	query := url.Values{"page": {"2"}, "q": {"a b"}}
	if err := c.do(context.Background(), secret.Secret{}, http.MethodGet, "/api/x", query, nil, nil); err != nil {
		t.Fatalf("do: %v", err)
	}
	if gotQuery.Get("page") != "2" || gotQuery.Get("q") != "a b" {
		t.Errorf("query = %v", gotQuery)
	}
}

func TestClient_RequestFailuresBeforeSending(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	tests := []struct {
		name   string
		method string
		body   any
		want   string
	}{
		{"unencodable body", http.MethodPost, make(chan int), "encoding mealie request"},
		{"invalid method", "BAD METHOD", nil, "building mealie request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := c.do(context.Background(), secret.Secret{}, tt.method, "/api/x", nil, tt.body, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("requests sent = %d, want 0", got)
	}
}

func TestClient_TruncatedResponseBody(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		respond(w, `{"version":`)
	}))
	err := c.do(context.Background(), secret.Secret{}, http.MethodPost, "/api/x", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "reading mealie response") {
		t.Fatalf("err = %v, want a reading mealie response error", err)
	}
}

func TestAPIError_DetailShapes(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"validation entries without messages", http.StatusUnprocessableEntity, `{"detail":[{"msg":""},{"loc":["body"]}]}`, "Unprocessable Entity"},
		{"validation entries skip empty messages", http.StatusUnprocessableEntity, `{"detail":[{"msg":""},{"msg":"kept"}]}`, "kept"},
		{"object detail", http.StatusBadRequest, `{"detail":{"reason":"bad"}}`, `{"reason":"bad"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				respond(w, tt.body)
			}))
			err := c.do(context.Background(), secret.Secret{}, http.MethodPost, "/api/x", nil, nil, nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("err = %v, want *APIError", err)
			}
			if apiErr.Message != tt.want {
				t.Errorf("Message = %q, want %q", apiErr.Message, tt.want)
			}
		})
	}
}

func TestCall_ReturnsRawJSON(t *testing.T) {
	const payload = `{"a":1,"x":{"y":null}}`
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		respond(w, payload)
	}))

	got, err := c.Call(context.Background(), secret.Secret{}, http.MethodGet, "/api/x", nil, nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(got) != payload {
		t.Errorf("Call = %s, want %s", got, payload)
	}
}

func TestCall_EmptyBodyIsNil(t *testing.T) {
	for _, status := range []int{http.StatusNoContent, http.StatusOK} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			}))

			got, err := c.Call(context.Background(), secret.Secret{}, http.MethodDelete, "/api/x", nil, nil)
			if err != nil || got != nil {
				t.Errorf("status %d: Call = %q, %v; want nil, nil", status, got, err)
			}
		})
	}
}

func TestCall_ErrorsKeepClassification(t *testing.T) {
	tests := []struct {
		name   string
		status int
		check  func(error) bool
	}{
		{"unauthorized", http.StatusUnauthorized, IsUnauthorized},
		{"unavailable", http.StatusServiceUnavailable, IsUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))

			_, err := c.Call(context.Background(), secret.Secret{}, http.MethodPost, "/api/x", nil, nil)
			if err == nil || !tt.check(err) {
				t.Errorf("Call error = %v, classification failed", err)
			}
		})
	}
}

func TestCall_SendsQueryAndBody(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("reading body: %v", err)
		}
		if r.Method != http.MethodPatch || r.URL.Path != "/api/x" || r.URL.Query().Get("perPage") != "20" || string(raw) != `{"name":"x"}` {
			t.Errorf("got %s %s %q", r.Method, r.URL, raw)
		}
		respond(w, `{}`)
	}))

	_, err := c.Call(context.Background(), secret.Secret{}, http.MethodPatch, "/api/x", url.Values{"perPage": {"20"}}, map[string]any{"name": "x"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
}

func redirectingHandler(status int, sources, targets *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/target" {
			targets.Add(1)
			respond(w, `{}`)
			return
		}
		sources.Add(1)
		http.Redirect(w, r, "/api/target", status)
	}
}

func TestCall_RefusesRedirects(t *testing.T) {
	const wantMessage = "Mealie redirected the request. Check that MEALIE_URL points directly at Mealie."
	for _, tc := range []struct {
		method string
		status int
	}{
		{http.MethodGet, http.StatusFound},
		{http.MethodPost, http.StatusTemporaryRedirect},
	} {
		t.Run(fmt.Sprintf("%s %d", tc.method, tc.status), func(t *testing.T) {
			var sources, targets atomic.Int32
			c := newTestClient(t, redirectingHandler(tc.status, &sources, &targets))

			_, err := c.Call(context.Background(), secret.FromString("tok"), tc.method, "/api/source", nil, map[string]string{"a": "b"})
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != tc.status || apiErr.Message != wantMessage {
				t.Fatalf("err = %v, want *APIError %d %q", err, tc.status, wantMessage)
			}
			if IsUnavailable(err) {
				t.Error("a redirect is classified as unavailable")
			}
			if sources.Load() != 1 || targets.Load() != 0 {
				t.Errorf("source hits = %d, target hits = %d, want 1 and 0", sources.Load(), targets.Load())
			}
		})
	}
}

func TestOutcome(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, "ok"},
		{"client error", &APIError{Status: 404}, "client_error"},
		{"server error", &APIError{Status: 503}, "server_error"},
		{"unavailable", ErrUnavailable, "unreachable"},
		{"net error", &net.OpError{Op: "dial"}, "unreachable"},
		{"deadline", context.DeadlineExceeded, "unreachable"},
		{"canceled", fmt.Errorf("calling mealie: %w", context.Canceled), "canceled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outcome(tt.err); got != tt.want {
				t.Fatalf("outcome = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRequest_ObservesEachAttempt(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		respond(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	registry := newMetrics(t)
	exporter := tracetest.NewInMemoryExporter()
	c := New(base, Options{Metrics: registry, Tracing: sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))})
	c.backoff = []time.Duration{0, 0}

	if _, err := c.Call(t.Context(), secret.New([]byte("SECRET-TOKEN")), http.MethodGet, "/api/recipes", nil, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}

	body := scrapeMetrics(t, registry)
	for _, line := range []string{
		`mealie_mcp_mealie_request_duration_seconds_count{method="GET",outcome="server_error"} 1` + "\n",
		`mealie_mcp_mealie_request_duration_seconds_count{method="GET",outcome="ok"} 1` + "\n",
	} {
		if !strings.Contains(body, line) {
			t.Errorf("metrics missing %q", line)
		}
	}

	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(spans))
	}
	for _, span := range spans {
		if span.Name != "mealie GET" || span.SpanKind != trace.SpanKindClient {
			t.Errorf("span = %q kind %v", span.Name, span.SpanKind)
		}
		assertNoLeaks(t, span)
	}
	if spans[0].Status.Code != codes.Error {
		t.Errorf("first span status = %v, want Error", spans[0].Status.Code)
	}
	if spans[1].Status.Code == codes.Error {
		t.Errorf("second span status = Error, want not Error")
	}
	for i, wantOutcome := range []string{"server_error", "ok"} {
		assertSpanAttributes(t, spans[i], wantOutcome)
	}
}

func assertNoLeaks(t *testing.T, span tracetest.SpanStub) {
	t.Helper()
	for _, attr := range span.Attributes {
		value := attr.Value.String()
		if strings.Contains(value, "SECRET-TOKEN") || strings.Contains(value, "/api/recipes") {
			t.Errorf("attribute %s leaks %q", attr.Key, value)
		}
	}
}

func assertSpanAttributes(t *testing.T, span tracetest.SpanStub, wantOutcome string) {
	t.Helper()
	attrs := map[string]string{}
	for _, attr := range span.Attributes {
		attrs[string(attr.Key)] = attr.Value.String()
	}
	if attrs["http.request.method"] != "GET" || attrs["mealie.outcome"] != wantOutcome {
		t.Errorf("span attributes = %v, want GET and %s", attrs, wantOutcome)
	}
}

func newMetrics(t *testing.T) *metrics.Registry {
	t.Helper()
	registry, err := metrics.New()
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}
	return registry
}

func scrapeMetrics(t *testing.T, registry *metrics.Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	registry.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func TestRequest_ObservesEveryExhaustedAttempt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	registry := newMetrics(t)
	exporter := tracetest.NewInMemoryExporter()
	c := New(base, Options{Metrics: registry, Tracing: sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))})
	c.backoff = []time.Duration{0, 0}

	if _, err := c.Call(t.Context(), secret.New([]byte("t")), http.MethodGet, "/api/recipes", nil, nil); err == nil {
		t.Fatal("expected error")
	}

	want := `mealie_mcp_mealie_request_duration_seconds_count{method="GET",outcome="server_error"} 3` + "\n"
	if !strings.Contains(scrapeMetrics(t, registry), want) {
		t.Errorf("metrics missing %q", want)
	}
	if got := len(exporter.GetSpans()); got != 3 {
		t.Errorf("spans = %d, want 3", got)
	}
}

func TestRequest_ObservesCanceledInFlight(t *testing.T) {
	started := make(chan struct{})
	var startOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		startOnce.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	base, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	registry := newMetrics(t)
	c := New(base, Options{Metrics: registry})
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		<-started
		cancel()
	}()

	if _, err := c.Call(ctx, secret.New([]byte("t")), http.MethodGet, "/api/recipes", nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}

	want := `mealie_mcp_mealie_request_duration_seconds_count{method="GET",outcome="canceled"} 1` + "\n"
	if !strings.Contains(scrapeMetrics(t, registry), want) {
		t.Errorf("metrics missing %q", want)
	}
}
