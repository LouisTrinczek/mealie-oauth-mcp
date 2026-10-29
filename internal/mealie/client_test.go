package mealie

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
