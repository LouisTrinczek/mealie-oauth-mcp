//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/app"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/audit"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/config"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/obs"
)

const (
	itPublicURL    = "http://it:18080"
	listenAddr     = ":18080"
	mealieURL      = "http://mealie:9000"
	startupTimeout = 10 * time.Second
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

func (w leakWatch) check(t *testing.T, logs string) {
	t.Helper()
	if !strings.Contains(logs, string(audit.LoginStarted)) {
		t.Error("server logs have no login_started audit event, so the leak check saw no login traffic")
	}
	for _, kind := range slices.Sorted(maps.Keys(w)) {
		if slices.ContainsFunc(w[kind], func(v string) bool { return v != "" && strings.Contains(logs, v) }) {
			t.Errorf("server logs leak a secret of kind %q", kind)
		}
	}
	if regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.`).MatchString(logs) {
		t.Error("server logs contain a JWT")
	}
}

type oauthError struct {
	Error string `json:"error"`
}

func TestFullFlow(t *testing.T) {
	f := startedFlow(t)
	steps := []struct {
		name string
		run  func(*testing.T)
	}{
		{"unauthenticated_mcp_is_challenged", f.challenge},
		{"metadata_documents", f.fetchMetadata},
		{"register_client", f.register},
		{"authorize_through_mealie_shows_consent", f.authorizeToConsent},
		{"consent_allow_redirects_with_code", f.allowConsent},
		{"code_exchange_issues_tokens", f.exchangeCode},
		{"whoami_over_mcp", f.whoami},
		{"refresh_rotates_and_reuse_is_rejected", f.refreshAndReuse},
		{"second_authorize_skips_consent", f.authorizeWithoutConsent},
		{"revoke_ends_the_session", f.revoke},
		{"metrics", f.scrapeMetrics},
	}
	for _, step := range steps {
		if !t.Run(step.name, step.run) {
			t.FailNow()
		}
	}
}

func startedFlow(t *testing.T) *flow {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)
	logs := &syncBuffer{}
	leaks := leakWatch{}
	t.Cleanup(func() { leaks.check(t, logs.String()) })
	metricsAddr := startServer(ctx, t, logs, leaks)
	f := newFlow(ctx, t, leaks, itPublicURL, http.DefaultTransport)
	f.metricsURL = "http://" + metricsAddr + "/metrics"
	return f
}

func loggedInSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	f := startedFlow(t)
	for _, step := range []func(*testing.T){f.fetchMetadata, f.register, f.authorizeToConsent, f.allowConsent, f.exchangeCode} {
		step(t)
	}
	return f.connect(t)
}

func startServer(ctx context.Context, t *testing.T, logs io.Writer, leaks leakWatch) string {
	t.Helper()
	keyMaterial := make([]byte, 32)
	_, _ = rand.Read(keyMaterial)
	key := base64.StdEncoding.EncodeToString(keyMaterial)
	leaks.remember("encryption key", key)
	env := map[string]string{
		"PUBLIC_URL":          itPublicURL,
		"INSECURE_ALLOW_HTTP": "true",
		"MEALIE_URL":          mealieURL,
		"ENCRYPTION_KEYS":     "it:" + key,
		"DATA_DIR":            t.TempDir(),
		"LISTEN_ADDR":         listenAddr,
		"METRICS_ADDR":        freeLocalAddr(ctx, t),
		"LOG_LEVEL":           "debug",
	}
	cfg, err := config.Load(func(name string) (string, bool) { v, ok := env[name]; return v, ok }, os.ReadFile)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	a, err := app.New(ctx, cfg, obs.NewLogger(logs, cfg.LogFormat, cfg.LogLevel), "integration")
	if err != nil {
		t.Fatalf("starting app against Mealie: %v", err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Errorf("closing app: %v", err)
		}
	})
	run(ctx, t, a)
	return cfg.MetricsAddr
}

func freeLocalAddr(ctx context.Context, t *testing.T) string {
	t.Helper()
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the free port: %v", err)
	}
	return addr
}

func run(ctx context.Context, t *testing.T, a *app.App) {
	t.Helper()
	runCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan error, 1)
	go func() { done <- a.Run(runCtx) }()
	t.Cleanup(func() {
		stop()
		if err := <-done; err != nil {
			t.Errorf("running app: %v", err)
		}
	})
	waitForOK(ctx, t, itPublicURL+"/healthz", startupTimeout)
}

func (f *flow) challenge(t *testing.T) {
	got := f.postMCP(t, "")
	if got.status != http.StatusUnauthorized {
		t.Fatalf("POST /mcp without token: status %d, want 401", got.status)
	}
	challenge := got.header.Get("WWW-Authenticate")
	if !strings.Contains(challenge, `resource_metadata="`+f.resourceMetadataURL()+`"`) {
		t.Fatalf("WWW-Authenticate %q does not point at %s", challenge, f.resourceMetadataURL())
	}
}

func (f *flow) refreshAndReuse(t *testing.T) {
	previous := f.tokens
	next := f.issuedTokens(t, f.refresh(t, previous.RefreshToken))
	if next.AccessToken == previous.AccessToken || next.RefreshToken == previous.RefreshToken {
		t.Fatal("refresh returned the same tokens")
	}
	if status := f.postMCP(t, next.AccessToken).status; status != http.StatusOK {
		t.Fatalf("POST /mcp with the refreshed access token: status %d, want 200", status)
	}
	assertInvalidGrant(t, f.refresh(t, previous.RefreshToken))
	if status := f.postMCP(t, next.AccessToken).status; status != http.StatusUnauthorized {
		t.Fatalf("after refresh-token reuse, POST /mcp: status %d, want 401 because reuse ends the session", status)
	}
}

func (f *flow) authorizeWithoutConsent(t *testing.T) {
	f.upstreamCode = ""
	got := f.startLogin(t)
	if f.upstreamCode == "" {
		t.Fatal("second login did not pass through mock-oidc to /callback")
	}
	f.code = f.clientRedirectCode(t, got)
}

func (f *flow) revoke(t *testing.T) {
	f.exchangeCode(t)
	if status := f.postMCP(t, f.tokens.AccessToken).status; status != http.StatusOK {
		t.Fatalf("POST /mcp with the second login's token: status %d, want 200", status)
	}
	got := f.postForm(t, f.api, f.metadata.RevocationEndpoint, url.Values{
		"token":           {f.tokens.AccessToken},
		"token_type_hint": {"access_token"},
		"client_id":       {f.clientID},
	})
	if got.status != http.StatusOK {
		t.Fatalf("POST /revoke: status %d, body %s", got.status, got.body)
	}
	if status := f.postMCP(t, f.tokens.AccessToken).status; status != http.StatusUnauthorized {
		t.Fatalf("POST /mcp after revoke: status %d, want 401", status)
	}
	assertInvalidGrant(t, f.refresh(t, f.tokens.RefreshToken))
}

func (f *flow) scrapeMetrics(t *testing.T) {
	got := send(t, f.api, f.request(t, http.MethodGet, f.metricsURL, nil))
	if got.status != http.StatusOK {
		t.Fatalf("scraping metrics: status %d", got.status)
	}
	for _, want := range []string{
		`mealie_mcp_audit_events_total{event="` + string(audit.LoginSucceeded) + `"}`,
		`mealie_mcp_tool_call_duration_seconds_count{outcome="ok",tool="whoami"}`,
	} {
		if !strings.Contains(string(got.body), want) {
			t.Errorf("scrape missing %q", want)
		}
	}
}

func (f *flow) refresh(t *testing.T, refreshToken string) reply {
	t.Helper()
	return f.postForm(t, f.api, f.metadata.TokenEndpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {f.clientID},
	})
}

func assertInvalidGrant(t *testing.T, got reply) {
	t.Helper()
	var body oauthError
	decode(t, got.body, &body)
	if got.status != http.StatusBadRequest || body.Error != "invalid_grant" {
		t.Fatalf("status %d, error %q, want 400 invalid_grant", got.status, body.Error)
	}
}

func (f *flow) postMCP(t *testing.T, accessToken string) reply {
	t.Helper()
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"integration-test","version":"1"}}}`
	req := f.request(t, http.MethodPost, f.resourceURL(), strings.NewReader(initialize))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	return send(t, f.api, req)
}
