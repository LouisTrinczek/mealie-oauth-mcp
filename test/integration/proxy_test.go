//go:build integration && proxyintegration

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/app"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/config"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/obs"
)

const proxyTestUsername = "changeme@example.com"

func TestVerifiedProxyPasswordLogin(t *testing.T) {
	mealieTLS := mealieTLSForProxyTest(t)
	for _, name := range []string{"traefik", "nginx", "caddy", "haproxy"} {
		t.Run(name, func(t *testing.T) {
			f := startProxiedFlow(t, name, mealieTLS)
			f.checkHTTPSRedirectAndHSTS(t, name)
			f.fetchMetadata(t)
			f.register(t)
			form := f.passwordForm(t)
			f.rejectUnverifiedProxy(t, name, form)
			f.signInWithPassword(t, form)
			f.allowConsent(t)
			f.exchangeCode(t)
			f.assertPasswordAccount(t)
			f.renewMealieToken(t)
		})
	}
}

func mealieTLSForProxyTest(t *testing.T) string {
	t.Helper()
	upstream, err := url.Parse(mealieURL)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair("/tls/mealie.pem", "/tls/mealie.key")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(httputil.NewSingleHostReverseProxy(upstream))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server.URL
}

func startProxiedFlow(t *testing.T, name, upstream string) *flow {
	t.Helper()
	public := "https://" + name + ":8443"
	input := map[string]string{
		"PUBLIC_URL":            public,
		"MEALIE_URL":            upstream,
		"ENABLE_PASSWORD_LOGIN": "true",
		"TLS_CERT_FILE":         "/tls/mcp.pem",
		"TLS_KEY_FILE":          "/tls/mcp.key",
		"ENCRYPTION_KEYS":       "test:" + base64.StdEncoding.EncodeToString(randBytes()),
		"DATA_DIR":              t.TempDir(),
		"LISTEN_ADDR":           ":18443",
		"METRICS_ADDR":          "",
		"LOG_LEVEL":             "debug",
	}
	cfg, err := config.Load(func(key string) (string, bool) { value, ok := input[key]; return value, ok }, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	credential := os.Getenv("PROXY_TEST_MEALIE_PASSWORD")
	if credential == "" {
		t.Fatal("missing disposable Mealie credential")
	}
	leaks := leakWatch{}
	leaks.remember("password", credential)
	logs := &syncBuffer{}
	t.Cleanup(func() { leaks.check(t, logs.String()) })
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)
	a, err := app.New(ctx, cfg, obs.NewLogger(logs, cfg.LogFormat, cfg.LogLevel), "proxy-integration")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := a.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	run(ctx, t, a, public)
	f := newFlow(ctx, t, leaks, public, http.DefaultTransport)
	f.dataDir = cfg.DataDir
	return f
}

func randBytes() []byte {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return key
}

func (f *flow) checkHTTPSRedirectAndHSTS(t *testing.T, name string) {
	t.Helper()
	health := f.publicURL + "/healthz"
	redirect := send(t, f.api, f.request(t, http.MethodGet, "http://"+name+":8080/healthz", nil))
	if redirect.status < 300 || redirect.status >= 400 || redirect.header.Get("Location") != health {
		t.Fatalf("HTTP health request returned %d, want a redirect to %s", redirect.status, health)
	}
	secure := send(t, f.api, f.request(t, http.MethodGet, health, nil))
	if secure.status != http.StatusOK || !strings.Contains(secure.header.Get("Strict-Transport-Security"), "max-age=31536000") {
		t.Fatal("HTTPS health response did not include HSTS")
	}
}

func (f *flow) passwordForm(t *testing.T) url.Values {
	t.Helper()
	page := f.startLogin(t)
	if page.status != http.StatusOK || !bytes.Contains(page.body, []byte("Sign in with password")) {
		t.Fatalf("authorize returned %d without the password form", page.status)
	}
	return url.Values{
		"pending_id": {formField(t, page.body, `name="pending_id" value="([^"]+)"`)},
		"csrf":       {formField(t, page.body, `name="csrf" value="([^"]+)"`)},
		"username":   {proxyTestUsername},
		"password":   {os.Getenv("PROXY_TEST_MEALIE_PASSWORD")},
	}
}

func (f *flow) rejectUnverifiedProxy(t *testing.T, name string, form url.Values) {
	t.Helper()
	for _, check := range []struct {
		name string
		port string
	}{
		{"unrelated CA", "8444"},
		{"wrong hostname", "8445"},
	} {
		t.Run(check.name, func(t *testing.T) {
			endpoint := "https://" + name + ":" + check.port + "/login/password"
			req := f.request(t, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			for _, cookie := range f.browser.Jar.Cookies(req.URL) {
				req.AddCookie(cookie)
			}
			got := send(t, f.api, req)
			if got.status < 500 || got.status >= 600 {
				t.Fatalf("%s proxy returned %d, want a gateway failure", name, got.status)
			}
		})
	}

	req := f.request(t, http.MethodPost, "http://it:18443/login/password", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Forwarded", "proto=https")
	if got := send(t, f.api, req); got.status != http.StatusBadRequest {
		t.Fatalf("plaintext listener returned %d, want 400", got.status)
	}
}

func (f *flow) signInWithPassword(t *testing.T, form url.Values) {
	t.Helper()
	page := f.postForm(t, f.browser, f.publicURL+"/login/password", form)
	if page.status != http.StatusOK || !bytes.Contains(page.body, []byte("Allow access to Mealie?")) {
		t.Fatalf("password login returned %d without consent", page.status)
	}
	f.consentURL = f.publicURL + "/consent"
	f.consentForm = url.Values{
		"pending_id": {formField(t, page.body, `name="pending_id" value="([^"]+)"`)},
		"csrf":       {formField(t, page.body, `name="csrf" value="([^"]+)"`)},
	}
}

func (f *flow) assertPasswordAccount(t *testing.T) {
	t.Helper()
	session := f.connect(t)
	result, err := session.CallTool(f.ctx, &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		t.Fatal(err)
	}
	assertWhoami(t, result, proxyTestUsername)
}
