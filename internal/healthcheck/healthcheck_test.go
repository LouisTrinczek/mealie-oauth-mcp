package healthcheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthzURL(t *testing.T) {
	tests := map[string]string{
		":8080":          "http://127.0.0.1:8080/healthz",
		"0.0.0.0:8080":   "http://127.0.0.1:8080/healthz",
		"[::]:8080":      "http://127.0.0.1:8080/healthz",
		"127.0.0.1:9000": "http://127.0.0.1:9000/healthz",
		"10.0.0.5:8080":  "http://10.0.0.5:8080/healthz",
		"[::1]:8080":     "http://[::1]:8080/healthz",
		"localhost:8080": "http://localhost:8080/healthz",
	}
	for listenAddr, want := range tests {
		t.Run(listenAddr, func(t *testing.T) {
			got, err := healthzURL(listenAddr)
			if err != nil {
				t.Fatalf("healthzURL(%q) error: %v", listenAddr, err)
			}
			if got != want {
				t.Errorf("healthzURL(%q) = %q, want %q", listenAddr, got, want)
			}
		})
	}
}

func TestHealthzURL_RejectsAddressWithoutPort(t *testing.T) {
	for _, listenAddr := range []string{"8080", "", "localhost"} {
		if _, err := healthzURL(listenAddr); err == nil {
			t.Errorf("healthzURL(%q) returned no error", listenAddr)
		}
	}
}

func TestCheck_HealthyServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	if err := Check(context.Background(), srv.Client(), srv.Listener.Addr().String()); err != nil {
		t.Fatalf("Check() error: %v", err)
	}
}

func TestCheck_UnhealthyStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	err := Check(context.Background(), srv.Client(), srv.Listener.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("Check() error = %v, want one naming status 503", err)
	}
}

func TestCheck_ServerDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	addr := srv.Listener.Addr().String()
	srv.Close()

	if err := Check(context.Background(), http.DefaultClient, addr); err == nil {
		t.Fatal("Check() returned no error for a stopped server")
	}
}

func TestCheck_InvalidListenAddr(t *testing.T) {
	for _, listenAddr := range []string{"8080", "bad host:8080"} {
		if err := Check(context.Background(), http.DefaultClient, listenAddr); err == nil {
			t.Errorf("Check(%q) returned no error", listenAddr)
		}
	}
}
