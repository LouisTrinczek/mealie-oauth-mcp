package httpx

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func requestFrom(t *testing.T, remoteAddr string) *http.Request {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.RemoteAddr = remoteAddr
	return req
}

func TestRateLimit_429(t *testing.T) {
	handler := RateLimit(60, 2, nil)(okHandler())

	codes := make([]int, 0, 3)
	var rejected *httptest.ResponseRecorder
	for range 3 {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, requestFrom(t, "203.0.113.9:1234"))
		codes = append(codes, rec.Code)
		rejected = rec
	}
	want := []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests}
	for i := range want {
		if codes[i] != want[i] {
			t.Fatalf("codes = %v, want %v", codes, want)
		}
	}
	seconds, err := strconv.Atoi(rejected.Header().Get("Retry-After"))
	if err != nil || seconds < 1 {
		t.Fatalf("Retry-After = %q, want positive integer", rejected.Header().Get("Retry-After"))
	}
}

func TestRateLimit_PerIP(t *testing.T) {
	handler := RateLimit(60, 1, nil)(okHandler())
	for _, addr := range []string{"203.0.113.1:1", "203.0.113.2:1"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, requestFrom(t, addr))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", addr, rec.Code)
		}
	}
}

func TestRateLimit_SpoofedForwardedForDoesNotBypass(t *testing.T) {
	handler := RateLimit(60, 1, nil)(okHandler())
	var last int
	for _, spoofed := range []string{"1.1.1.1", "2.2.2.2"} {
		req := requestFrom(t, "203.0.113.9:1234")
		req.Header.Set("X-Forwarded-For", spoofed)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		last = rec.Code
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("second request status = %d, want 429", last)
	}
}

func TestLimiterSet_EvictsIdleEntries(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	set := newLimiterSet(60, 1, 100, func() time.Time { return now })

	set.allow("203.0.113.1")
	set.allow("203.0.113.2")
	if got := set.size(); got != 2 {
		t.Fatalf("size = %d, want 2", got)
	}

	now = now.Add(idleLimiterTTL + sweepInterval)
	set.allow("203.0.113.3")
	if got := set.size(); got != 1 {
		t.Fatalf("size after eviction = %d, want 1", got)
	}
}

func TestLimiterSet_KeepsActiveEntries(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	set := newLimiterSet(60, 1, 100, func() time.Time { return now })

	set.allow("203.0.113.1")
	now = now.Add(idleLimiterTTL - time.Second)
	set.allow("203.0.113.1")
	now = now.Add(sweepInterval)
	set.allow("203.0.113.2")
	if got := set.size(); got != 2 {
		t.Fatalf("size = %d, want 2", got)
	}
}

func TestRateLimit_IPv6RotationWithinSlash64(t *testing.T) {
	handler := RateLimit(60, 2, nil)(okHandler())
	addrs := []string{"[2001:db8:1:2::1]:1", "[2001:db8:1:2::2]:1", "[2001:db8:1:2:aaaa::3]:1"}
	var codes []int
	for _, addr := range addrs {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, requestFrom(t, addr))
		codes = append(codes, rec.Code)
	}
	want := []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests}
	for i := range want {
		if codes[i] != want[i] {
			t.Fatalf("codes = %v, want %v", codes, want)
		}
	}
}

func TestRateLimit_DifferentSlash64sAreSeparate(t *testing.T) {
	handler := RateLimit(60, 1, nil)(okHandler())
	for _, addr := range []string{"[2001:db8:1:2::1]:1", "[2001:db8:1:3::1]:1"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, requestFrom(t, addr))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", addr, rec.Code)
		}
	}
}

func TestLimiterSet_CapRejectsNewKeysUntilIdleEviction(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	set := newLimiterSet(60, 1, 2, func() time.Time { return now })

	if !set.allow("a") || !set.allow("b") {
		t.Fatal("first two keys must be allowed")
	}
	if set.allow("c") {
		t.Fatal("new key allowed while full")
	}
	if got := set.size(); got != 2 {
		t.Fatalf("size = %d, want 2", got)
	}

	now = now.Add(idleLimiterTTL)
	if !set.allow("c") {
		t.Fatal("new key rejected after idle eviction")
	}
}
