package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/config"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/token"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/tokencrypt"
)

const (
	testPublicURL    = "https://mcp.example"
	testMealieToken  = "mealie-session-token"
	testSessionID    = "session-1"
	testClientID     = "client-1"
	currentAbout     = `{"version":"v3.29.0","enableOidc":true,"production":true}`
	testMetadataPath = "/.well-known/oauth-protected-resource/mcp"
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
	mu         sync.Mutex
	userAgents []string
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
	ct, err := keyring.Seal([]byte(testMealieToken), []byte("session:"+testSessionID))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
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
	access, _, err := token.New(token.Access)
	if err != nil {
		t.Fatalf("token.New: %v", err)
	}
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
	if !strings.Contains(logs.String(), `"deleted":4`) {
		t.Errorf("logs = %s, want deleted count 4", logs)
	}
}

func listenAddr(t *testing.T, logs *syncBuffer) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(logs.String(), "\n") {
			var entry struct {
				Msg  string `json:"msg"`
				Addr string `json:"addr"`
			}
			if json.Unmarshal([]byte(line), &entry) == nil && entry.Msg == "listening" {
				return entry.Addr
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server never logged its listen address")
	return ""
}

func TestApp_RunServesUntilCanceled(t *testing.T) {
	fake := startFakeMealie(t, currentAbout, http.StatusOK)
	logs := &syncBuffer{}
	a := newTestApp(t, testConfig(t, fake.URL), logs)
	running := runApp(t, a, logs)

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, running.baseURL+"/healthz", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("healthz over the network: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz = %d, want 200", resp.StatusCode)
	}
	running.requireCleanStop(t, 5*time.Second)
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
