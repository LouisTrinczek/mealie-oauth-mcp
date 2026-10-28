package httpx

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func prefixes(t *testing.T, cidrs ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, cidr := range cidrs {
		out = append(out, netip.MustParsePrefix(cidr))
	}
	return out
}

func TestClientIP(t *testing.T) {
	proxy := []string{"10.0.0.0/8", "fd00::/8"}
	tests := []struct {
		name       string
		remoteAddr string
		xff        []string
		trusted    []string
		want       string
	}{
		{"untrusted remote ignores spoofed xff", "203.0.113.9:1234", []string{"1.2.3.4"}, proxy, "203.0.113.9"},
		{"no trusted proxies ignores xff", "10.0.0.1:1234", []string{"1.2.3.4"}, nil, "10.0.0.1"},
		{"trusted remote single hop", "10.0.0.1:1234", []string{"198.51.100.7"}, proxy, "198.51.100.7"},
		{"trusted remote multi hop rightmost untrusted", "10.0.0.1:1234", []string{"6.6.6.6, 198.51.100.7, 10.0.0.2"}, proxy, "198.51.100.7"},
		{"spoofed leftmost is not used", "10.0.0.1:1234", []string{"6.6.6.6", "198.51.100.7"}, proxy, "198.51.100.7"},
		{"all hops trusted falls back to remote", "10.0.0.1:1234", []string{"10.0.0.2, 10.0.0.3"}, proxy, "10.0.0.1"},
		{"garbage hop falls back to remote", "10.0.0.1:1234", []string{"not-an-ip"}, proxy, "10.0.0.1"},
		{"missing xff uses remote", "10.0.0.1:1234", nil, proxy, "10.0.0.1"},
		{"ipv6 remote", "[2001:db8::1]:443", []string{"1.2.3.4"}, proxy, "2001:db8::1"},
		{"ipv6 trusted proxy with ipv6 client", "[fd00::1]:443", []string{"2001:db8::7"}, proxy, "2001:db8::7"},
		{"ipv4 mapped ipv6 remote is unmapped", "[::ffff:10.0.0.1]:443", []string{"198.51.100.7"}, proxy, "198.51.100.7"},
		{"unparsable remote", "garbage", []string{"1.2.3.4"}, proxy, "invalid IP"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
			req.RemoteAddr = tt.remoteAddr
			for _, value := range tt.xff {
				req.Header.Add("X-Forwarded-For", value)
			}
			got := ClientIP(req, prefixes(t, tt.trusted...))
			if got.String() != tt.want {
				t.Fatalf("ClientIP = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestRecover(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	handler := Recover(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/token?code=SECRET", strings.NewReader("client_secret=SECRET"))
	req.Header.Set("Authorization", "Bearer SECRET")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("body leaks panic value: %q", rec.Body.String())
	}
	if !strings.Contains(buf.String(), "boom") || !strings.Contains(buf.String(), "stack") {
		t.Fatalf("log lacks panic or stack: %s", buf.String())
	}
	if strings.Contains(buf.String(), "SECRET") {
		t.Fatalf("log leaks request data: %s", buf.String())
	}
}

func TestRecover_PassesThroughNormalResponse(t *testing.T) {
	handler := Recover(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", rec.Code)
	}
}

func TestRecover_RepanicsAbortHandler(t *testing.T) {
	handler := Recover(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if err, isError := recover().(error); !isError || !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", err)
		}
	}()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))
}

func TestSecurityHeaders(t *testing.T) {
	tests := []struct {
		name     string
		hsts     bool
		wantHSTS string
	}{
		{"without hsts", false, ""},
		{"with hsts", true, "max-age=31536000"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := SecurityHeaders(tt.hsts)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

			want := map[string]string{
				"X-Content-Type-Options": "nosniff",
				"Referrer-Policy":        "no-referrer",
				"Cache-Control":          "no-store",
			}
			for name, value := range want {
				if got := rec.Header().Get(name); got != value {
					t.Fatalf("%s = %q, want %q", name, got, value)
				}
			}
			got := rec.Header().Get("Strict-Transport-Security")
			if got != tt.wantHSTS {
				t.Fatalf("Strict-Transport-Security = %q, want %q", got, tt.wantHSTS)
			}
			if _, present := rec.Header()["Strict-Transport-Security"]; present != tt.hsts {
				t.Fatalf("Strict-Transport-Security present = %v, want %v", present, tt.hsts)
			}
		})
	}
}
