package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
)

const (
	stubAccess      = "mmcp_at_test"
	testMealieToken = "mealie-token-value"
	testSessionID   = "session-1"
)

type fakeTokens struct {
	mu       sync.Mutex
	err      error
	seen     []string
	reported []string
}

func (f *fakeTokens) MealieToken(_ context.Context, info *auth.TokenInfo) (secret.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, sessionOf(info))
	if f.err != nil {
		return secret.Secret{}, f.err
	}
	return secret.FromString(testMealieToken), nil
}

func (f *fakeTokens) ReportUnauthorized(_ context.Context, info *auth.TokenInfo) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reported = append(f.reported, sessionOf(info))
}

func (f *fakeTokens) calls() (seen, reported []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...), append([]string(nil), f.reported...)
}

func sessionOf(info *auth.TokenInfo) string {
	if info == nil {
		return ""
	}
	id, _ := info.Extra["session_id"].(string)
	return id
}

type fakeMealie struct {
	mu     sync.Mutex
	user   mealie.User
	err    error
	tokens []string
}

func (f *fakeMealie) Self(_ context.Context, tok secret.Secret) (mealie.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tokens = append(f.tokens, tok.Reveal())
	return f.user, f.err
}

func (f *fakeMealie) seenTokens() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tokens...)
}

type bearerTransport struct {
	token string
}

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(clone)
}

func stubVerifier(_ context.Context, tok string, _ *http.Request) (*auth.TokenInfo, error) {
	if tok != stubAccess {
		return nil, auth.ErrInvalidToken
	}
	return &auth.TokenInfo{
		UserID:     "user-1",
		Scopes:     []string{"mealie"},
		Expiration: time.Now().Add(time.Hour),
		Extra:      map[string]any{"session_id": testSessionID},
	}, nil
}

func connectOverHTTP(t *testing.T, d Deps) *mcp.ClientSession {
	t.Helper()
	srv := New(d)
	handler := auth.RequireBearerToken(stubVerifier, nil)(
		mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil),
	)
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   ts.URL,
		HTTPClient: &http.Client{Transport: bearerTransport{token: stubAccess}, Timeout: 10 * time.Second},
	}
	return connect(t, transport)
}

func connectInMemory(t *testing.T, d Deps) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := New(d).Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	return connect(t, clientTransport)
}

func connect(t *testing.T, transport mcp.Transport) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	cs, err := client.Connect(t.Context(), transport, nil)
	if err != nil {
		t.Fatalf("client Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callWhoami(t *testing.T, cs *mcp.ClientSession) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	return res
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("content has %d items, want 1", len(res.Content))
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", res.Content[0])
	}
	return text.Text
}

func requireToolError(t *testing.T, res *mcp.CallToolResult, want string) {
	t.Helper()
	if !res.IsError {
		t.Fatal("IsError = false, want a tool error")
	}
	if got := resultText(t, res); got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
	if res.StructuredContent != nil {
		t.Errorf("structured content = %v, want none on a tool error", res.StructuredContent)
	}
}

func TestWhoami_ReturnsUser(t *testing.T) {
	tokens := &fakeTokens{}
	profile := &fakeMealie{user: mealie.User{
		ID:            "user-1",
		Username:      "alice",
		FullName:      "Alice Example",
		Email:         "alice@example.com",
		GroupSlug:     "home",
		HouseholdSlug: "family",
		Admin:         true,
	}}
	cs := connectOverHTTP(t, Deps{Tokens: tokens, Mealie: profile, Version: "v1.2.3"})

	if got := cs.InitializeResult().ServerInfo; got.Name != "mealie-oauth-mcp" || got.Version != "v1.2.3" {
		t.Errorf("server info = %+v, want mealie-oauth-mcp v1.2.3", got)
	}
	res := callWhoami(t, cs)
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	var got map[string]any
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}
	want := map[string]any{
		"username":  "alice",
		"fullName":  "Alice Example",
		"email":     "alice@example.com",
		"household": "family",
		"group":     "home",
		"admin":     true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("output = %v, want %v", got, want)
	}
	if seen, _ := tokens.calls(); len(seen) != 1 || seen[0] != testSessionID {
		t.Errorf("MealieToken saw sessions %v, want [%s]", seen, testSessionID)
	}
	if got := profile.seenTokens(); len(got) != 1 || got[0] != testMealieToken {
		t.Errorf("Self got tokens %v, want the session's Mealie token", got)
	}
}

func TestWhoami_OmitsEmptyFields(t *testing.T) {
	profile := &fakeMealie{user: mealie.User{Username: "bob"}}
	cs := connectOverHTTP(t, Deps{Tokens: &fakeTokens{}, Mealie: profile})

	text := resultText(t, callWhoami(t, cs))
	for _, field := range []string{"fullName", "email", "household", "group"} {
		if strings.Contains(text, field) {
			t.Errorf("output %s contains empty field %q", text, field)
		}
	}
}

func TestWhoami_Mealie401ReportsAndErrors(t *testing.T) {
	tokens := &fakeTokens{}
	profile := &fakeMealie{err: &mealie.APIError{Status: http.StatusUnauthorized, Message: "Bearer secret-detail"}}
	cs := connectOverHTTP(t, Deps{Tokens: tokens, Mealie: profile})

	requireToolError(t, callWhoami(t, cs), "Your Mealie session ended. Reconnect this app to log in again.")
	if _, reported := tokens.calls(); len(reported) != 1 || reported[0] != testSessionID {
		t.Errorf("ReportUnauthorized calls = %v, want [%s]", reported, testSessionID)
	}
}

func TestWhoami_UnavailableMessage(t *testing.T) {
	const unavailable = "Mealie is unavailable right now. Try again shortly."
	tests := []struct {
		name      string
		tokenErr  error
		mealieErr error
	}{
		{"MealieOutage", nil, &mealie.APIError{Status: http.StatusBadGateway, Message: "upstream detail"}},
		{"MealieTimeout", nil, context.DeadlineExceeded},
		{"SessionUnavailable", fmt.Errorf("renewing: %w", mealie.ErrUnavailable), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokens := &fakeTokens{err: tt.tokenErr}
			cs := connectOverHTTP(t, Deps{Tokens: tokens, Mealie: &fakeMealie{err: tt.mealieErr}})

			requireToolError(t, callWhoami(t, cs), unavailable)
			if _, reported := tokens.calls(); len(reported) != 0 {
				t.Errorf("ReportUnauthorized called %d times on an outage", len(reported))
			}
		})
	}
}

func TestWhoami_SessionEndedWhenNoMealieToken(t *testing.T) {
	tokens := &fakeTokens{err: auth.ErrInvalidToken}
	profile := &fakeMealie{}
	cs := connectOverHTTP(t, Deps{Tokens: tokens, Mealie: profile})

	requireToolError(t, callWhoami(t, cs), "Your Mealie session ended. Reconnect this app to log in again.")
	if got := profile.seenTokens(); len(got) != 0 {
		t.Errorf("Self called %d times without a Mealie token", len(got))
	}
}

func TestWhoami_WithoutTokenInfoEndsSession(t *testing.T) {
	cs := connectInMemory(t, Deps{Tokens: &fakeTokens{err: auth.ErrInvalidToken}, Mealie: &fakeMealie{}})

	requireToolError(t, callWhoami(t, cs), "Your Mealie session ended. Reconnect this app to log in again.")
}

func TestWhoami_OtherErrorsAreGeneric(t *testing.T) {
	profile := &fakeMealie{err: &mealie.APIError{Status: http.StatusForbidden, Message: "Bearer eyJhbGciOi.leak.sig"}}
	cs := connectOverHTTP(t, Deps{Tokens: &fakeTokens{}, Mealie: profile})

	res := callWhoami(t, cs)
	requireToolError(t, res, "Could not read your Mealie profile. Try again.")
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	for _, leak := range []string{"eyJ", "Bearer", "403", testMealieToken} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("tool result leaks %q", leak)
		}
	}
}

func TestWhoami_Annotations(t *testing.T) {
	cs := connectInMemory(t, Deps{Tokens: &fakeTokens{}, Mealie: &fakeMealie{}})

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "whoami" {
		t.Fatalf("tools = %v, want only whoami", res.Tools)
	}
	tool := res.Tools[0]
	if tool.Description != "Show which Mealie user this connection acts as, including household and group." {
		t.Errorf("description = %q", tool.Description)
	}
	a := tool.Annotations
	if a == nil {
		t.Fatal("annotations missing")
	}
	if !a.ReadOnlyHint || !a.IdempotentHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations = %+v, want read-only, idempotent, closed world", a)
	}
	if tool.OutputSchema == nil {
		t.Error("output schema missing")
	}
}
