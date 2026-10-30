package audit

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/obs"
)

func TestAudit_HasLogTypeAndRequestID(t *testing.T) {
	var buf bytes.Buffer
	auditLog := New(obs.NewLogger(&buf, "json", slog.LevelInfo))

	handler := obs.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		auditLog.Event(r.Context(), LoginSucceeded, slog.String("user", "alice"))
	}))
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.Header.Set("X-Request-ID", "request-id-1234")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("decode log: %v", err)
	}
	want := map[string]any{
		"log_type":   "audit",
		"request_id": "request-id-1234",
		"event":      "login_succeeded",
		"user":       "alice",
	}
	for key, value := range want {
		if entry[key] != value {
			t.Fatalf("%s = %v, want %v (entry %v)", key, entry[key], value, entry)
		}
	}
}

func TestEventTypes_Distinct(t *testing.T) {
	events := []EventType{
		LoginStarted, LoginSucceeded, LoginFailed, ConsentGranted, ConsentDenied,
		ClientRegistered, TokenIssued, TokenRefreshed, RefreshReuseDetected,
		SessionRevoked, SessionExpired, MealieTokenRenewed,
	}
	seen := map[EventType]bool{}
	for _, ev := range events {
		if ev == "" || seen[ev] {
			t.Fatalf("event %q is empty or duplicated", ev)
		}
		seen[ev] = true
	}
}
