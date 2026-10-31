package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/config"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/mcpserver"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/token"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/tokencrypt"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/tracing"
)

const (
	testPublicURL      = "https://mcp.example"
	testMealieToken    = "mealie-session-token"
	testSessionID      = "session-1"
	testClientID       = "client-1"
	currentAbout       = `{"version":"v3.29.0","enableOidc":true,"production":true}`
	testMetadataPath   = "/.well-known/oauth-protected-resource/mcp"
	defaultMetricsAddr = "127.0.0.1:9090"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type fakeMealie struct {
	*httptest.Server
	mu                sync.Mutex
	userAgents        []string
	exchangeRedirects []string
}

func (f *fakeMealie) recordExchange(r *http.Request) {
	var body struct {
		RedirectURI string `json:"redirect_uri"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exchangeRedirects = append(f.exchangeRedirects, body.RedirectURI)
}

func (f *fakeMealie) seenExchangeRedirects() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.exchangeRedirects...)
}

func (f *fakeMealie) record(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userAgents = append(f.userAgents, r.Header.Get("User-Agent"))
}

func (f *fakeMealie) seenUserAgents() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.userAgents...)
}

func startFakeMealie(t *testing.T, about string, nativeConfigStatus int) *fakeMealie {
	t.Helper()
	fake := &fakeMealie{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/app/about", func(w http.ResponseWriter, r *http.Request) {
		fake.record(r)
		_, _ = io.WriteString(w, about)
	})
	mux.HandleFunc("GET /api/auth/oauth/native/config", func(w http.ResponseWriter, r *http.Request) {
		fake.record(r)
		if nativeConfigStatus != http.StatusOK {
			w.WriteHeader(nativeConfigStatus)
			return
		}
		_, _ = io.WriteString(w, `{"authorization_endpoint":"https://idp.example/authorize","client_id":"mealie","scope":"openid"}`)
	})
	mux.HandleFunc("POST /api/auth/oauth/native/token", func(w http.ResponseWriter, r *http.Request) {
		fake.record(r)
		fake.recordExchange(r)
		_, _ = io.WriteString(w, `{"access_token":"`+testMealieToken+`","token_type":"bearer","expires_in":172800}`)
	})
	mux.HandleFunc("GET /api/users/self", func(w http.ResponseWriter, r *http.Request) {
		fake.record(r)
		if r.Header.Get("Authorization") != "Bearer "+testMealieToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"id":"user-1","username":"alice","fullName":"Alice","email":"alice@example.com","admin":false,"groupSlug":"home","householdSlug":"family","unknownField":1}`)
	})
	fake.Server = httptest.NewServer(mux)
	t.Cleanup(fake.Close)
	return fake
}

func testConfig(t *testing.T, mealieURL string) *config.Config {
	t.Helper()
	mealieBase, err := url.Parse(mealieURL)
	if err != nil {
		t.Fatalf("parse mealie url: %v", err)
	}
	publicURL, err := url.Parse(testPublicURL)
	if err != nil {
		t.Fatalf("parse public url: %v", err)
	}
	return &config.Config{
		MealieURL:      mealieBase,
		PublicURL:      publicURL,
		EncryptionKeys: []config.EncryptionKey{{ID: "k1", Material: secret.New(bytes.Repeat([]byte{7}, 32))}},
		DataDir:        t.TempDir(),
		ListenAddr:     "127.0.0.1:0",
		SessionMaxAge:  720 * time.Hour,
		AccessTokenTTL: time.Hour,
	}
}

func newTestApp(t *testing.T, cfg *config.Config, logs io.Writer) *App {
	t.Helper()
	a, err := New(t.Context(), cfg, slog.New(slog.NewJSONHandler(logs, nil)), "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func startApp(t *testing.T) (*App, *fakeMealie) {
	t.Helper()
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	return newTestApp(t, testConfig(t, fake.URL), io.Discard), fake
}

func seedSession(t *testing.T, a *App) string {
	t.Helper()
	ctx := t.Context()
	now := time.Now()
	keyring, err := tokencrypt.NewKeyring(a.cfg.EncryptionKeys)
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	if err := a.store.UpsertClient(ctx, store.Client{ID: testClientID, Source: "dcr", Name: "App", RedirectURIs: []string{"http://127.0.0.1/cb"}}); err != nil {
		t.Fatalf("UpsertClient: %v", err)
	}
	ct := keyring.Seal([]byte(testMealieToken), []byte("session:"+testSessionID))
	resource := a.cfg.ResourceURL()
	session := store.Session{
		ID:                   testSessionID,
		ClientID:             testClientID,
		MealieUserID:         "user-1",
		MealieUsername:       "alice",
		MealieTokenCT:        ct,
		MealieTokenExpiresAt: now.Add(48 * time.Hour),
		MealieTokenLifetime:  48 * time.Hour,
		Resource:             resource,
		CreatedAt:            now,
		ExpiresAt:            now.Add(720 * time.Hour),
		LastUsedAt:           now,
	}
	code := store.AuthCode{Hash: token.Hash("code"), SessionID: testSessionID, ClientID: testClientID, RedirectURI: "http://127.0.0.1/cb", CodeChallenge: "c", Resource: resource, ExpiresAt: now.Add(time.Minute)}
	if err := a.store.CreateSessionWithCode(ctx, session, code); err != nil {
		t.Fatalf("CreateSessionWithCode: %v", err)
	}
	access, _ := token.New(token.Access)
	accessRow := store.AccessToken{Hash: token.Hash(access.Reveal()), SessionID: testSessionID, ExpiresAt: now.Add(time.Hour)}
	refresh := store.RefreshToken{Hash: token.Hash("refresh"), SessionID: testSessionID, ExpiresAt: now.Add(720 * time.Hour)}
	if err := a.store.IssueTokens(ctx, accessRow, refresh); err != nil {
		t.Fatalf("IssueTokens: %v", err)
	}
	return access.Reveal()
}

func serve(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, target, nil))
	return rec
}

func requireStartupError(t *testing.T, about string, nativeConfigStatus int, want string) {
	t.Helper()
	fake := startFakeMealie(t, about, nativeConfigStatus)
	cfg := testConfig(t, fake.URL)
	a, err := New(t.Context(), cfg, slog.New(slog.DiscardHandler), "test")
	if err == nil {
		_ = a.Close()
		t.Fatal("New succeeded, want a startup error")
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want it to contain %q", err, want)
	}
}

func TestApp_StartupFailsWhenOIDCDisabled(t *testing.T) {
	requireStartupError(t, `{"version":"v3.29.0","enableOidc":false}`, http.StatusOK,
		"Mealie has OIDC disabled; this server requires OIDC (see README)")
}

func TestApp_StartupFailsOnOldVersion(t *testing.T) {
	requireStartupError(t, `{"version":"v3.24.1","enableOidc":true}`, http.StatusOK,
		"Mealie v3.24.1 is older than v3.25.0")
}

func TestApp_StartupFailsWhenNativeConfigFails(t *testing.T) {
	requireStartupError(t, currentAbout, http.StatusNotFound, "native OIDC config")
}

func TestApp_StartupWarnsOnUnparsableVersion(t *testing.T) {
	fake := startFakeMealie(t, `{"version":"nightly","enableOidc":true}`, http.StatusOK)
	logs := &syncBuffer{}
	newTestApp(t, testConfig(t, fake.URL), logs)

	if !strings.Contains(logs.String(), `"version":"nightly"`) || !strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Errorf("logs = %s, want a warning naming the version", logs)
	}
}

func TestApp_StartupWarnsWithoutTrustedProxies(t *testing.T) {
	tests := []struct {
		name     string
		proxies  []netip.Prefix
		wantWarn bool
	}{
		{"unset", nil, true},
		{"set", []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16")}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := startFakeMealie(t, currentAbout, http.StatusOK)
			cfg := testConfig(t, fake.URL)
			cfg.TrustedProxies = tt.proxies
			logs := &syncBuffer{}
			newTestApp(t, cfg, logs)

			warned := strings.Contains(logs.String(), `"level":"WARN","msg":"TRUSTED_PROXIES is not set`)
			if warned != tt.wantWarn {
				t.Errorf("TRUSTED_PROXIES warning = %v, want %v; logs: %s", warned, tt.wantWarn, logs)
			}
		})
	}
}

func TestApp_StartupUsesVersionedUserAgent(t *testing.T) {
	_, fake := startApp(t)

	agents := fake.seenUserAgents()
	if len(agents) == 0 {
		t.Fatal("startup made no Mealie requests")
	}
	for _, ua := range agents {
		if ua != "mealie-oauth-mcp/test" {
			t.Errorf("User-Agent = %q, want mealie-oauth-mcp/test", ua)
		}
	}
}

type fakeProbe struct {
	aboutErrs []error
	calls     int
}

func (f *fakeProbe) About(context.Context) (mealie.About, error) {
	f.calls++
	if len(f.aboutErrs) > 0 {
		err := f.aboutErrs[0]
		f.aboutErrs = f.aboutErrs[1:]
		return mealie.About{}, err
	}
	return mealie.About{Version: mealie.MinVersion, EnableOIDC: true}, nil
}

func (f *fakeProbe) NativeConfig(context.Context) (mealie.NativeConfig, error) {
	return mealie.NativeConfig{}, nil
}

func TestStartupCheck_RetriesUnreachableMealie(t *testing.T) {
	outage := &mealie.APIError{Status: http.StatusServiceUnavailable, Message: "Service Unavailable"}
	notFound := &mealie.APIError{Status: http.StatusNotFound, Message: "Not Found"}
	tests := []struct {
		name      string
		errs      []error
		wantCalls int
		wantErr   bool
	}{
		{"RecoversWithinRetries", []error{outage, outage}, 3, false},
		{"GivesUpAfterFiveRetries", []error{outage, outage, outage, outage, outage, outage}, 6, true},
		{"DoesNotRetryOtherErrors", []error{notFound}, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe := &fakeProbe{aboutErrs: tt.errs}
			check := startupCheck{mealie: probe, mealieURL: "http://mealie.test", logger: slog.New(slog.DiscardHandler), retries: startupRetries, retryDelay: time.Millisecond, reachTimeout: time.Minute}
			err := check.run(t.Context())
			if probe.calls != tt.wantCalls {
				t.Errorf("About calls = %d, want %d", probe.calls, tt.wantCalls)
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("run: %v", err)
				}
				return
			}
			if err == nil || !strings.HasPrefix(err.Error(), "cannot reach Mealie at http://mealie.test") {
				t.Errorf("error = %v, want cannot reach Mealie at http://mealie.test", err)
			}
		})
	}
}

func TestStartupCheck_StopsWhenCanceled(t *testing.T) {
	outage := &mealie.APIError{Status: http.StatusServiceUnavailable}
	probe := &fakeProbe{aboutErrs: []error{outage, outage}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	check := startupCheck{mealie: probe, mealieURL: "http://mealie.test", logger: slog.New(slog.DiscardHandler), retries: startupRetries, retryDelay: time.Hour, reachTimeout: time.Minute}

	if err := check.run(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestStartupCheck_GivesUpOnHangingMealie(t *testing.T) {
	hanging := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(hanging.Close)
	base, err := url.Parse(hanging.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	check := startupCheck{
		mealie:       mealie.New(base, mealie.Options{}),
		mealieURL:    hanging.URL,
		logger:       slog.New(slog.DiscardHandler),
		retries:      startupRetries,
		retryDelay:   10 * time.Millisecond,
		reachTimeout: 200 * time.Millisecond,
	}

	started := time.Now()
	err = check.run(t.Context())
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("check took %v, want it bounded by the reachability timeout", elapsed)
	}
	if err == nil || !strings.HasPrefix(err.Error(), "cannot reach Mealie at "+hanging.URL) {
		t.Errorf("error = %v, want cannot reach Mealie at %s", err, hanging.URL)
	}
}

func TestApp_MCPWithoutTokenIs401WithMetadata(t *testing.T) {
	a, _ := startApp(t)

	rec := serve(t, a.Handler(), http.MethodPost, "/mcp")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	want := `resource_metadata="` + testPublicURL + testMetadataPath + `"`
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, want) {
		t.Errorf("WWW-Authenticate = %q, want %s", got, want)
	}
}

func TestApp_MCPRejectsCrossOriginBrowserRequests(t *testing.T) {
	a, _ := startApp(t)
	access := seedSession(t, a)
	tests := []struct {
		name          string
		authorization string
	}{
		{"WithToken", "Bearer " + access},
		{"WithoutToken", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", strings.NewReader(`{}`))
			if tt.authorization != "" {
				req.Header.Set("Authorization", tt.authorization)
			}
			req.Header.Set("Sec-Fetch-Site", "cross-site")
			rec := httptest.NewRecorder()
			a.Handler().ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403 before any token check", rec.Code)
			}
		})
	}
}

func TestApp_Healthz(t *testing.T) {
	a, _ := startApp(t)

	rec := serve(t, a.Handler(), http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Errorf("healthz = %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
}

func TestApp_Readyz(t *testing.T) {
	a, fake := startApp(t)
	h := a.Handler()

	if rec := serve(t, h, http.MethodGet, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("readyz with Mealie up = %d, want 200", rec.Code)
	}
	fake.Close()
	rec := serve(t, h, http.MethodGet, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz with Mealie down = %d, want 503", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "mealie") {
		t.Errorf("body = %q, want it to name the mealie check", body)
	}
	if strings.Contains(body, strings.TrimPrefix(fake.URL, "http://")) {
		t.Errorf("body = %q reveals the Mealie URL", body)
	}
}

func TestApp_ReadyzReportsDatabase(t *testing.T) {
	a, _ := startApp(t)
	if err := a.store.Close(); err != nil {
		t.Fatalf("closing store: %v", err)
	}

	rec := serve(t, a.Handler(), http.MethodGet, "/readyz")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "database") {
		t.Errorf("readyz = %d %q, want 503 naming the database", rec.Code, rec.Body.String())
	}
}

func TestApp_SecurityHeadersOnEveryRoute(t *testing.T) {
	a, _ := startApp(t)
	h := a.Handler()
	routes := []struct{ method, path string }{
		{http.MethodGet, "/healthz"},
		{http.MethodGet, "/readyz"},
		{http.MethodPost, "/mcp"},
		{http.MethodGet, "/.well-known/oauth-authorization-server"},
		{http.MethodGet, testMetadataPath},
		{http.MethodGet, "/missing"},
	}
	for _, route := range routes {
		t.Run(route.method+route.path, func(t *testing.T) {
			header := serve(t, h, route.method, route.path).Header()
			want := map[string]string{
				"Cache-Control":             "no-store",
				"X-Content-Type-Options":    "nosniff",
				"Strict-Transport-Security": "max-age=31536000",
			}
			for name, value := range want {
				if got := header.Get(name); got != value {
					t.Errorf("%s = %q, want %q", name, got, value)
				}
			}
			if header.Get("X-Request-ID") == "" {
				t.Error("X-Request-ID missing")
			}
		})
	}
}

type bearerTransport struct {
	token string
}

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(clone)
}

func TestApp_WhoamiOverMCP(t *testing.T) {
	a, _ := startApp(t)
	access := seedSession(t, a)
	ts := httptest.NewServer(a.Handler())
	t.Cleanup(ts.Close)

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "v0"}, nil)
	cs, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:   ts.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearerTransport{token: access}, Timeout: 10 * time.Second},
	}, nil)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	if got := cs.InitializeResult().ServerInfo; got.Name != "mealie-oauth-mcp" || got.Version != "test" {
		t.Errorf("server info = %+v, want mealie-oauth-mcp test", got)
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if res.IsError || !strings.Contains(string(raw), `"username":"alice"`) {
		t.Errorf("whoami = %s (error %v), want alice", raw, res.IsError)
	}
}

func initializeMCP(t *testing.T, ctx context.Context, endpoint, access string) string {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	sessionID := resp.Header.Get("Mcp-Session-Id")
	if resp.StatusCode != http.StatusOK || sessionID == "" {
		t.Fatalf("initialize = %d with session %q", resp.StatusCode, sessionID)
	}
	return sessionID
}

func openStream(t *testing.T, ctx context.Context, endpoint, access string) io.ReadCloser {
	t.Helper()
	sessionID := initializeMCP(t, ctx, endpoint, access)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Mcp-Session-Id", sessionID)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stream did not start before the handler returned: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d, want 200", resp.StatusCode)
	}
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	if err != nil {
		t.Fatalf("first event did not arrive while the stream was open: %v", err)
	}
	if !strings.HasPrefix(line, ":") {
		t.Errorf("first line = %q, want an SSE comment", line)
	}
	return resp.Body
}

func TestApp_MCPStreamIsFlushedThroughMiddleware(t *testing.T) {
	a, _ := startApp(t)
	access := seedSession(t, a)
	ts := httptest.NewServer(a.Handler())
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	openStream(t, ctx, ts.URL+"/mcp", access)
}

type runningApp struct {
	stop    context.CancelFunc
	done    chan error
	baseURL string
}

func runApp(t *testing.T, a *App, logs *syncBuffer) runningApp {
	t.Helper()
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	return runningApp{stop: stop, done: done, baseURL: "http://" + listenAddr(t, logs)}
}

func (r runningApp) requireCleanStop(t *testing.T, within time.Duration) time.Duration {
	t.Helper()
	started := time.Now()
	r.stop()
	select {
	case err := <-r.done:
		if err != nil {
			t.Errorf("Run = %v, want nil after a clean shutdown", err)
		}
	case <-time.After(within):
		t.Fatalf("Run did not return within %v", within)
	}
	return time.Since(started)
}

func TestApp_ShutdownEndsOpenStreams(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	logs := &syncBuffer{}
	a := newTestApp(t, testConfig(t, fake.URL), logs)
	a.shutdownGrace = 200 * time.Millisecond
	access := seedSession(t, a)
	running := runApp(t, a, logs)
	streamCtx, cancelStream := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelStream()
	stream := openStream(t, streamCtx, running.baseURL+"/mcp", access)

	elapsed := running.requireCleanStop(t, a.shutdownGrace+2*time.Second)
	if _, err := io.Copy(io.Discard, stream); err != nil {
		t.Errorf("stream ended with %v, want a clean close", err)
	}
	if elapsed < a.shutdownGrace {
		t.Errorf("stream ended after %v, want the %v grace period first", elapsed, a.shutdownGrace)
	}
}

func TestApp_ShutdownLetsInFlightRequestsFinish(t *testing.T) {
	var slow atomic.Bool
	entered := make(chan struct{})
	release := make(chan struct{})
	mealieServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/app/about" && slow.Load() {
			close(entered)
			<-release
		}
		if r.URL.Path == "/api/app/about" {
			_, _ = io.WriteString(w, currentAbout)
			return
		}
		_, _ = io.WriteString(w, `{"authorization_endpoint":"https://idp.example/authorize","client_id":"mealie","scope":"openid"}`)
	}))
	t.Cleanup(mealieServer.Close)
	logs := &syncBuffer{}
	a := newTestApp(t, testConfig(t, mealieServer.URL), logs)
	a.shutdownGrace = 5 * time.Second
	running := runApp(t, a, logs)
	slow.Store(true)

	status := make(chan int, 1)
	go func() {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, running.baseURL+"/readyz", nil)
		if err != nil {
			status <- 0
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			status <- 0
			return
		}
		_ = resp.Body.Close()
		status <- resp.StatusCode
	}()
	<-entered
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(release)
	}()

	running.requireCleanStop(t, 3*time.Second)
	if got := <-status; got != http.StatusOK {
		t.Errorf("in-flight readyz = %d, want 200 completed during the grace period", got)
	}
}

func TestApp_DeleteExpiredLogsCount(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	logs := &syncBuffer{}
	a := newTestApp(t, testConfig(t, fake.URL), logs)
	seedSession(t, a)
	a.now = func() time.Time { return time.Now().Add(1000 * time.Hour) }

	a.deleteExpired(t.Context())
	if !strings.Contains(logs.String(), `"deleted":5`) {
		t.Errorf("logs = %s, want deleted count 5", logs)
	}
}

func listenAddr(t *testing.T, logs *syncBuffer) string {
	t.Helper()
	return loggedAddr(t, logs, "listening")
}

func loggedAddr(t *testing.T, logs *syncBuffer, msg string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(logs.String(), "\n") {
			var entry struct {
				Msg  string `json:"msg"`
				Addr string `json:"addr"`
			}
			if json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == msg {
				return entry.Addr
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never logged %q with an address", msg)
	return ""
}

func TestApp_RunServesUntilCanceled(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	logs := &syncBuffer{}
	cfg := testConfig(t, fake.URL)
	cfg.MetricsAddr = "127.0.0.1:0"
	a := newTestApp(t, cfg, logs)
	running := runApp(t, a, logs)
	metricsAddr := loggedAddr(t, logs, "serving metrics")

	if status, _ := fetch(t, running.baseURL+"/healthz"); status != http.StatusOK {
		t.Errorf("healthz = %d, want 200", status)
	}
	if status, _ := fetch(t, "http://"+metricsAddr+"/metrics"); status != http.StatusOK {
		t.Errorf("metrics = %d, want 200", status)
	}
	running.requireCleanStop(t, 5*time.Second)
	requireClosed(t, strings.TrimPrefix(running.baseURL, "http://"), metricsAddr)
}

func TestRun_MetricsDisabled(t *testing.T) {
	probe, err := new(net.ListenConfig).Listen(t.Context(), "tcp", defaultMetricsAddr)
	if err != nil {
		t.Skipf("%s is taken on this machine, so the check cannot tell who holds it: %v", defaultMetricsAddr, err)
	}
	_ = probe.Close()
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	logs := &syncBuffer{}
	a := newTestApp(t, testConfig(t, fake.URL), logs)
	running := runApp(t, a, logs)

	if status, _ := fetch(t, running.baseURL+"/healthz"); status != http.StatusOK {
		t.Errorf("healthz = %d, want 200", status)
	}
	held, err := new(net.ListenConfig).Listen(t.Context(), "tcp", defaultMetricsAddr)
	if err != nil {
		t.Errorf("Run holds %s with METRICS_ADDR empty: %v", defaultMetricsAddr, err)
	} else {
		_ = held.Close()
	}
	running.requireCleanStop(t, 5*time.Second)
}

func TestRun_MetricsAddrInUse(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	cfg := testConfig(t, fake.URL)
	cfg.MetricsAddr = listenLocal(t).Addr().String()
	cfg.ListenAddr = freeAddr(t)
	a := newTestApp(t, cfg, io.Discard)

	if err := a.Run(t.Context()); err == nil || !strings.Contains(err.Error(), "METRICS_ADDR") {
		t.Fatalf("Run = %v, want an error naming METRICS_ADDR", err)
	}
	again, err := new(net.ListenConfig).Listen(t.Context(), "tcp", cfg.ListenAddr)
	if err != nil {
		t.Fatalf("main listener was not released: %v", err)
	}
	_ = again.Close()
}

func TestServe_MetricsOnSeparateListener(t *testing.T) {
	a, _ := startApp(t)
	seedSession(t, a)
	main, metricsListener := listenLocal(t), listenLocal(t)
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- a.serve(ctx, main, metricsListener) }()
	mainURL, metricsURL := "http://"+main.Addr().String(), "http://"+metricsListener.Addr().String()

	if status, _ := fetch(t, mainURL+"/healthz"); status != http.StatusOK {
		t.Fatalf("healthz = %d, want 200", status)
	}
	status, body := fetch(t, metricsURL+"/metrics")
	if status != http.StatusOK {
		t.Fatalf("metrics = %d, want 200", status)
	}
	for _, want := range []string{
		`mealie_mcp_http_request_duration_seconds_count{code="200",route="GET /healthz"} 1` + "\n",
		"mealie_mcp_active_sessions 1\n",
		`mealie_mcp_mealie_request_duration_seconds_count{method="GET"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape missing %q", want)
		}
	}
	if status, _ := fetch(t, mainURL+"/metrics"); status != http.StatusNotFound {
		t.Errorf("metrics on the main listener = %d, want 404", status)
	}

	stop()
	if err := <-done; err != nil {
		t.Fatalf("serve = %v, want nil after cancel", err)
	}
	requireClosed(t, main.Addr().String(), metricsListener.Addr().String())
}

func TestDeleteExpired_RefreshesActiveSessions(t *testing.T) {
	a, _ := startApp(t)
	seedSession(t, a)

	a.deleteExpired(t.Context())
	if body := serve(t, a.metrics.Handler(), http.MethodGet, "/metrics").Body.String(); !strings.Contains(body, "mealie_mcp_active_sessions 1\n") {
		t.Errorf("scrape missing mealie_mcp_active_sessions 1")
	}
}

func TestApp_ActiveSessionsCountFailureIsLogged(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	logs := &syncBuffer{}
	a := newTestApp(t, testConfig(t, fake.URL), logs)
	_ = a.store.Close()

	a.refreshActiveSessions(t.Context())
	if !strings.Contains(logs.String(), `"level":"ERROR","msg":"counting active sessions failed"`) {
		t.Errorf("logs = %s, want the count failure logged at error level", logs)
	}
}

func fetch(t *testing.T, target string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s: %v", target, err)
	}
	return resp.StatusCode, string(body)
}

func listenLocal(t *testing.T) net.Listener {
	t.Helper()
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	return listener
}

func freeAddr(t *testing.T) string {
	t.Helper()
	listener := listenLocal(t)
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

func requireClosed(t *testing.T, addrs ...string) {
	t.Helper()
	for _, addr := range addrs {
		conn, err := new(net.Dialer).DialContext(t.Context(), "tcp", addr)
		if err == nil {
			_ = conn.Close()
			t.Errorf("%s still accepts connections after shutdown", addr)
		}
	}
}

func TestApp_RunFailsOnInvalidListenAddr(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	cfg := testConfig(t, fake.URL)
	cfg.ListenAddr = "not-an-address"
	a := newTestApp(t, cfg, io.Discard)

	if err := a.Run(t.Context()); err == nil {
		t.Error("Run succeeded on an invalid listen address")
	}
}

func TestApp_NewFailures(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, cfg *config.Config)
		want   string
	}{
		{"no encryption keys", func(_ *testing.T, cfg *config.Config) { cfg.EncryptionKeys = nil }, "loading encryption keys"},
		{"data dir is a file", func(t *testing.T, cfg *config.Config) {
			file := filepath.Join(t.TempDir(), "not-a-dir")
			if err := os.WriteFile(file, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg.DataDir = file
		}, "opening store"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := startFakeMealie(t, currentAbout, http.StatusOK)
			cfg := testConfig(t, fake.URL)
			tt.mutate(t, cfg)

			a, err := New(t.Context(), cfg, slog.New(slog.DiscardHandler), "test")
			if err == nil {
				_ = a.Close()
				t.Fatal("New succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestApp_NewFailsWhenToolsDoNotBuild(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	cfg := testConfig(t, fake.URL)
	failing := func(mcpserver.Deps) (*mcp.Server, error) { return nil, errors.New("bad schema") }

	cfg.TracingEnabled = true
	exporter := retainingExporter{InMemoryExporter: tracetest.NewInMemoryExporter(), shutdownErr: errors.New("collector gone")}

	a, err := newApp(t.Context(), cfg, slog.New(slog.DiscardHandler), "test", failing, exportTo(exporter))
	if err == nil {
		_ = a.Close()
		t.Fatal("newApp succeeded with a failing tools builder")
	}
	for _, want := range []string{"building MCP tools: bad schema", "flushing traces: collector gone"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
	reopened, err := store.Open(t.Context(), cfg.DataDir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("store stayed open after the failure: %v", err)
	}
	_ = reopened.Close()
}

func TestApp_ToolsGetTheAppLogger(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	cfg := testConfig(t, fake.URL)
	logger := slog.New(slog.DiscardHandler)
	var got mcpserver.Deps
	capture := func(d mcpserver.Deps) (*mcp.Server, error) {
		got = d
		return mcpserver.New(d)
	}

	a, err := newApp(t.Context(), cfg, logger, "test", capture, tracing.OTLP)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if got.Logger != logger {
		t.Errorf("tools logger = %p, want the app logger %p", got.Logger, logger)
	}
	if got.Metrics != a.metrics || got.Tracing != a.tracing {
		t.Error("tools did not get the app's metrics registry and tracer provider")
	}
}

type retainingExporter struct {
	*tracetest.InMemoryExporter
	shutdownErr error
}

func (e retainingExporter) Shutdown(context.Context) error { return e.shutdownErr }

func exportTo(exporter sdktrace.SpanExporter) tracing.Exporter {
	return func(context.Context) (sdktrace.SpanExporter, error) { return exporter, nil }
}

func TestNewApp_TracingSetupFailure(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	cfg := testConfig(t, fake.URL)
	cfg.TracingEnabled = true
	failing := func(context.Context) (sdktrace.SpanExporter, error) { return nil, errors.New("no collector") }

	a, err := newApp(t.Context(), cfg, slog.New(slog.DiscardHandler), "test", mcpserver.New, failing)
	if err == nil {
		_ = a.Close()
		t.Fatal("newApp succeeded with a failing exporter")
	}
	if !strings.Contains(err.Error(), "starting tracing") {
		t.Errorf("error = %q, want it to contain starting tracing", err)
	}
	if agents := fake.seenUserAgents(); len(agents) != 0 {
		t.Errorf("Mealie was called %d times before tracing started", len(agents))
	}
	reopened, err := store.Open(t.Context(), cfg.DataDir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("store stayed open after the failure: %v", err)
	}
	_ = reopened.Close()
}

func TestApp_CloseReportsTracingFlushFailure(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	cfg := testConfig(t, fake.URL)
	cfg.TracingEnabled = true
	exporter := retainingExporter{InMemoryExporter: tracetest.NewInMemoryExporter(), shutdownErr: errors.New("collector gone")}
	a, err := newApp(t.Context(), cfg, slog.New(slog.DiscardHandler), "test", mcpserver.New, exportTo(exporter))
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}

	if err := a.Close(); err == nil || !strings.Contains(err.Error(), "flushing traces: collector gone") {
		t.Errorf("Close = %v, want the flush failure", err)
	}
	reopened, err := store.Open(t.Context(), cfg.DataDir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("store stayed open after the flush failure: %v", err)
	}
	_ = reopened.Close()
}

func TestHandler_SpansNamedByRoute(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	cfg := testConfig(t, fake.URL)
	cfg.TracingEnabled = true
	exporter := retainingExporter{InMemoryExporter: tracetest.NewInMemoryExporter()}
	a, err := newApp(t.Context(), cfg, slog.New(slog.DiscardHandler), "test", mcpserver.New, exportTo(exporter))
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}

	serve(t, a.Handler(), http.MethodGet, "/callback?code=SECRET-CODE")
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	spans := exporter.GetSpans()
	found := false
	for _, span := range spans {
		if span.SpanKind == trace.SpanKindServer && span.Name == "GET /callback" {
			found = true
		}
		if slices.ContainsFunc(spanTexts(span), func(text string) bool { return strings.Contains(text, "SECRET-CODE") }) {
			t.Errorf("span %q leaks the code in its name, status, attributes or events", span.Name)
		}
	}
	if !found {
		t.Errorf("no server span named GET /callback among %d spans", len(spans))
	}
}

func spanTexts(span tracetest.SpanStub) []string {
	texts := []string{span.Name, span.Status.Description}
	for _, attr := range span.Attributes {
		texts = append(texts, attr.Value.String())
	}
	for _, event := range span.Events {
		texts = append(texts, event.Name)
		for _, attr := range event.Attributes {
			texts = append(texts, attr.Value.String())
		}
	}
	return texts
}

func TestApp_ServeFailsWhenListenerBreaks(t *testing.T) {
	a, _ := startApp(t)
	listener, err := new(net.ListenConfig).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	_ = listener.Close()

	if err := a.serve(t.Context(), listener, nil); err == nil || !strings.Contains(err.Error(), "serving http") {
		t.Fatalf("serve = %v, want a serving http error", err)
	}
}

func TestServe_MetricsListenerFailureStopsMainServer(t *testing.T) {
	a, _ := startApp(t)
	main, broken := listenLocal(t), listenLocal(t)
	_ = broken.Close()

	if err := a.serve(t.Context(), main, broken); err == nil || !strings.Contains(err.Error(), "serving metrics") {
		t.Fatalf("serve = %v, want a serving metrics error", err)
	}
	requireClosed(t, main.Addr().String())
}

type closeFailingListener struct {
	net.Listener
}

func (l closeFailingListener) Close() error {
	_ = l.Listener.Close()
	return errors.New("listener close failed")
}

func TestApp_ShutdownReportsListenerCloseFailure(t *testing.T) {
	tests := []struct {
		name          string
		failMain      bool
		want, notWant string
	}{
		{"http", true, "shutting down http server", "shutting down metrics server"},
		{"metrics", false, "shutting down metrics server", "shutting down http server"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _ := startApp(t)
			main, metricsListener := listenLocal(t), listenLocal(t)
			mainURL, metricsURL := "http://"+main.Addr().String()+"/healthz", "http://"+metricsListener.Addr().String()+"/metrics"
			if tt.failMain {
				main = closeFailingListener{main}
			} else {
				metricsListener = closeFailingListener{metricsListener}
			}
			ctx, stop := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() { done <- a.serve(ctx, main, metricsListener) }()
			fetch(t, mainURL)
			fetch(t, metricsURL)

			stop()
			err := <-done
			if err == nil || !strings.Contains(err.Error(), tt.want) || strings.Contains(err.Error(), tt.notWant) {
				t.Fatalf("serve = %v, want only a %s error", err, tt.want)
			}
		})
	}
}

func TestApp_CleanupLoopDeletesOnEachTick(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	logs := &syncBuffer{}
	a := newTestApp(t, testConfig(t, fake.URL), logs)
	ctx, stop := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		a.cleanupLoop(ctx, time.Millisecond)
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "deleted expired rows") {
		if time.Now().After(deadline) {
			t.Fatal("cleanup loop never deleted expired rows")
		}
		time.Sleep(5 * time.Millisecond)
	}
	stop()
	<-done
}

func TestApp_DeleteExpiredLogsFailure(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	logs := &syncBuffer{}
	a := newTestApp(t, testConfig(t, fake.URL), logs)
	_ = a.store.Close()

	a.deleteExpired(t.Context())
	if !strings.Contains(logs.String(), "deleting expired rows failed") {
		t.Errorf("logs = %s, want the cleanup failure logged", logs)
	}
}

func TestWithMiddleware_PanicIsMeasuredAndTraced(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	cfg := testConfig(t, fake.URL)
	cfg.TracingEnabled = true
	exporter := retainingExporter{InMemoryExporter: tracetest.NewInMemoryExporter()}
	a, err := newApp(t.Context(), cfg, slog.New(slog.DiscardHandler), "test", mcpserver.New, exportTo(exporter))
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("boom") })

	rec := serve(t, a.withMiddleware(mux), http.MethodGet, "/boom")

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	scrape := serve(t, a.metrics.Handler(), http.MethodGet, "/metrics").Body.String()
	if want := `mealie_mcp_http_request_duration_seconds_count{code="500",route="GET /boom"} 1` + "\n"; !strings.Contains(scrape, want) {
		t.Errorf("scrape missing %q", want)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	spans := exporter.GetSpans()
	found := slices.ContainsFunc(spans, func(span tracetest.SpanStub) bool {
		return span.SpanKind == trace.SpanKindServer && span.Name == "GET /boom" && span.Status.Code == codes.Error
	})
	if !found {
		t.Errorf("no errored server span named GET /boom among %d spans", len(spans))
	}
}
