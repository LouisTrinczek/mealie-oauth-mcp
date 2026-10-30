package obs

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewLogger_Format(t *testing.T) {
	tests := []struct {
		format string
		want   string
	}{
		{"json", `"msg":"hello"`},
		{"text", `msg=hello`},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			var buf bytes.Buffer
			NewLogger(&buf, tt.format, slog.LevelInfo).Info("hello")
			if !strings.Contains(buf.String(), tt.want) {
				t.Fatalf("output %q does not contain %q", buf.String(), tt.want)
			}
		})
	}
}

func TestNewLogger_Level(t *testing.T) {
	var buf bytes.Buffer
	NewLogger(&buf, "json", slog.LevelWarn).Info("hidden")
	if buf.Len() != 0 {
		t.Fatalf("info logged at warn level: %q", buf.String())
	}
}

func TestRequestID_RejectsInjected(t *testing.T) {
	tests := []struct {
		name     string
		incoming string
		keep     bool
	}{
		{"newline injection", "\nfoo", false},
		{"too short", "abc", false},
		{"too long", strings.Repeat("a", 65), false},
		{"bad character", "abcdefgh!", false},
		{"valid", "abcd-1234-EFGH", true},
		{"missing", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen string
			handler := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = RequestIDFrom(r.Context())
			}))
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			req.Header["X-Request-Id"] = []string{tt.incoming}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if got := rec.Header().Get("X-Request-ID"); got != seen {
				t.Fatalf("response header %q != context id %q", got, seen)
			}
			if tt.keep != (seen == tt.incoming) {
				t.Fatalf("id = %q for incoming %q, keep = %v", seen, tt.incoming, tt.keep)
			}
			if !requestIDPattern.MatchString(seen) {
				t.Fatalf("id %q does not match pattern", seen)
			}
		})
	}
}

func TestRequestID_GeneratedIDsDiffer(t *testing.T) {
	first, second := newRequestID(), newRequestID()
	if first == second {
		t.Fatalf("ids equal: %q", first)
	}
}

func TestRequestIDFrom_Empty(t *testing.T) {
	if got := RequestIDFrom(t.Context()); got != "" {
		t.Fatalf("id = %q, want empty", got)
	}
}

func TestAccessLog_NoQueryNoHeaders(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "json", slog.LevelInfo)
	handler := RequestID(AccessLog(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/callback?code=SECRET", nil)
	req.Header.Set("Authorization", "Bearer X")
	req.Header.Set("X-Request-ID", "request-id-1234")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	out := buf.String()
	for _, forbidden := range []string{"SECRET", "Bearer", "code="} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("log contains %q: %s", forbidden, out)
		}
	}
	for _, want := range []string{`"method":"GET"`, `"path":"/callback"`, `"status":418`, `"request_id":"request-id-1234"`, `"duration"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %s: %s", want, out)
		}
	}
}

func TestAccessLog_DefaultStatus(t *testing.T) {
	var buf bytes.Buffer
	handler := AccessLog(NewLogger(&buf, "json", slog.LevelInfo))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if !strings.Contains(buf.String(), `"status":200`) {
		t.Fatalf("log missing status 200: %s", buf.String())
	}
}

func TestAccessLog_ExposesUnderlyingWriter(t *testing.T) {
	handler := AccessLog(NewLogger(&bytes.Buffer{}, "json", slog.LevelInfo))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush through access log: %v", err)
		}
	}))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if !recorder.Flushed {
		t.Fatal("flush did not reach the underlying writer")
	}
}

func TestRoute(t *testing.T) {
	unrouted := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/x/abc123", nil)
	if got := Route(unrouted); got != "unmatched" {
		t.Fatalf("unrouted = %q, want unmatched", got)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	routed := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
	mux.ServeHTTP(httptest.NewRecorder(), routed)
	if got := Route(routed); got != "GET /healthz" {
		t.Fatalf("routed = %q, want GET /healthz", got)
	}
}
