package oauth

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
)

func TestAuthorize_InvalidRedirectRendersErrorNoRedirect(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(url.Values)
	}{
		{"unregistered redirect", func(p url.Values) { p.Set("redirect_uri", "https://evil.example.com/cb") }},
		{"missing redirect", func(p url.Values) { p.Del("redirect_uri") }},
		{"unknown client", func(p url.Values) { p.Set("client_id", "nobody") }},
		{"missing client", func(p url.Values) { p.Del("client_id") }},
		{"bad redirect and bad pkce", func(p url.Values) {
			p.Set("redirect_uri", "https://evil.example.com/cb")
			p.Del("code_challenge")
		}},
	}
	env := newTestServer(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := authorizeParams()
			tt.mutate(params)

			resp, body := env.authorize(t, params)

			wantPage(t, resp, body, http.StatusBadRequest, "")
			if strings.Contains(body, "evil.example.com") {
				t.Fatal("error page echoes the untrusted redirect URI")
			}
		})
	}
}

func TestAuthorize_MissingPKCERedirectsError(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(url.Values)
	}{
		{"missing challenge", func(p url.Values) { p.Del("code_challenge") }},
		{"missing method", func(p url.Values) { p.Del("code_challenge_method") }},
		{"plain method", func(p url.Values) { p.Set("code_challenge_method", "plain") }},
		{"lowercase method", func(p url.Values) { p.Set("code_challenge_method", "s256") }},
	}
	env := newTestServer(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := authorizeParams()
			tt.mutate(params)

			resp, _ := env.authorize(t, params)

			env.wantClientError(t, resp, "invalid_request")
		})
	}
}

func TestAuthorize_UnsupportedResponseType(t *testing.T) {
	env := newTestServer(t)
	params := authorizeParams()
	params.Set("response_type", "token")

	resp, _ := env.authorize(t, params)

	env.wantClientError(t, resp, "unsupported_response_type")
}

func TestAuthorize_MissingResourceDefaultsToMCP(t *testing.T) {
	env := newTestServer(t)
	page := env.loginToConsent(t, authorizeParams())

	resp, _ := env.decide(t, env.client, page, "allow")

	env.wantCode(t, resp)
	if got := env.onlySession(t).Resource; got != env.http.URL+"/mcp" {
		t.Fatalf("session resource = %q, want %q", got, env.http.URL+"/mcp")
	}
}

func TestAuthorize_WrongResource(t *testing.T) {
	env := newTestServer(t)
	params := authorizeParams()
	params.Set("resource", "https://other.example.com/mcp")

	resp, _ := env.authorize(t, params)

	env.wantClientError(t, resp, "invalid_target")
}

func TestAuthorize_RedirectsUpstreamWithMealieConfig(t *testing.T) {
	env := newTestServer(t)

	resp, _ := env.authorize(t, env.withResource(authorizeParams()))

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	upstream, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parsing Location: %v", err)
	}
	if got := upstream.Scheme + "://" + upstream.Host + upstream.Path; got != env.idp.server.URL+"/authorize" {
		t.Fatalf("redirected to %q", got)
	}
	q := upstream.Query()
	want := map[string]string{
		"response_type":         "code",
		"client_id":             testMealieClientID,
		"scope":                 testMealieScope,
		"redirect_uri":          env.http.URL + "/callback",
		"code_challenge_method": "S256",
	}
	for key, value := range want {
		if q.Get(key) != value {
			t.Errorf("%s = %q, want %q", key, q.Get(key), value)
		}
	}
	for _, key := range []string{"state", "nonce", "code_challenge"} {
		if q.Get(key) == "" {
			t.Errorf("%s is missing", key)
		}
	}
	if q.Get("state") == testClientState || q.Get("code_challenge") == S256(testClientVerifier) {
		t.Fatal("upstream leg reuses the client's state or PKCE challenge")
	}
	if !strings.Contains(env.logs.String(), `"event":"login_started"`) {
		t.Fatal("no login_started audit event")
	}
}

func TestAuthorize_SetsBindingCookie(t *testing.T) {
	tests := []struct {
		name    string
		secure  bool
		cookie  string
		wantSec bool
	}{
		{"secure", true, "__Host-mmcp_auth", true},
		{"insecure", false, "mmcp_auth", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestServer(t, func(c *Config) { c.SecureCookies = tt.secure })

			resp, _ := env.authorize(t, authorizeParams())

			cookies := resp.Cookies()
			if len(cookies) != 1 {
				t.Fatalf("got %d cookies, want 1", len(cookies))
			}
			c := cookies[0]
			if c.Name != tt.cookie || !c.HttpOnly || c.Secure != tt.wantSec || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 600 {
				t.Fatalf("cookie = %+v", c)
			}
			if !strings.Contains(c.Value, ".") {
				t.Fatalf("cookie value %q is not pendingID.binding", c.Value)
			}
		})
	}
}

func TestAuthorize_MealieUnreachable(t *testing.T) {
	tests := []struct {
		name   string
		config mealie.NativeConfig
		err    error
	}{
		{"config fetch fails", mealie.NativeConfig{}, &mealie.APIError{Status: http.StatusServiceUnavailable, Message: "connection refused"}},
		{"malformed authorization endpoint", mealie.NativeConfig{AuthorizationEndpoint: "://connection refused"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestServer(t)
			env.mealie.nativeConfig = func(context.Context) (mealie.NativeConfig, error) { return tt.config, tt.err }

			resp, body := env.authorize(t, authorizeParams())

			wantPage(t, resp, body, http.StatusBadGateway, "Mealie is unreachable")
			if strings.Contains(body, "connection refused") {
				t.Fatal("error page leaks the internal error")
			}
			if len(resp.Cookies()) != 0 {
				t.Fatal("binding cookie set although the login cannot start")
			}
		})
	}
}

func TestAuthorize_CIMDClient(t *testing.T) {
	env := newTestServer(t)
	clientID := "https://app.example.com/oauth/client.json"
	env.cimd.metadata[clientID] = ClientMetadata{ClientID: clientID, ClientName: "CIMD App", RedirectURIs: []string{testClientRedirect}}
	params := authorizeParams()
	params.Set("client_id", clientID)

	page := env.loginToConsent(t, params)

	stored, err := env.store.GetClient(t.Context(), clientID)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if stored.Source != "cimd" || stored.Name != "CIMD App" {
		t.Fatalf("stored client = %+v", stored)
	}
	if !strings.Contains(page.body, "app.example.com") {
		t.Fatalf("consent page lacks the client ID host: %s", page.body)
	}
	if strings.Contains(page.body, "not verified") {
		t.Fatal("CIMD client is labelled not verified")
	}
	resp, _ := env.decide(t, env.client, page, "allow")
	env.wantCode(t, resp)
	if got := env.onlySession(t).ClientID; got != clientID {
		t.Fatalf("session client = %q", got)
	}
}

func TestAuthorize_CIMDFetchFailureIsGeneric(t *testing.T) {
	env := newTestServer(t)
	env.cimd.err = errors.New("dialing app.example.com: 10.0.0.1: address is not publicly routable")
	params := authorizeParams()
	params.Set("client_id", "https://app.example.com/oauth/client.json")

	resp, body := env.authorize(t, params)

	wantPage(t, resp, body, http.StatusBadRequest, "invalid_client")
	for _, leak := range []string{"10.0.0.1", "routable", "dialing"} {
		if strings.Contains(body, leak) {
			t.Fatalf("error page leaks %q: %s", leak, body)
		}
	}
}

func TestCallback_UpstreamError(t *testing.T) {
	tests := []struct {
		upstream string
		want     string
	}{
		{"access_denied", "access_denied"},
		{"temporarily_unavailable", "temporarily_unavailable"},
		{"login_required", "access_denied"},
	}
	for _, tt := range tests {
		t.Run(tt.upstream, func(t *testing.T) {
			env := newTestServer(t)
			callback, err := url.Parse(env.callbackURL(t, authorizeParams()))
			if err != nil {
				t.Fatal(err)
			}
			callback.RawQuery = url.Values{
				"error":             {tt.upstream},
				"error_description": {"<b>upstream detail</b>"},
				"state":             {callback.Query().Get("state")},
			}.Encode()

			resp, _ := get(t, env.client, callback.String())

			env.wantClientError(t, resp, tt.want)
			if strings.Contains(resp.Header.Get("Location"), "upstream+detail") {
				t.Fatal("upstream error_description is forwarded")
			}
			env.wantNoSession(t)
			if !strings.Contains(env.logs.String(), `"event":"login_failed"`) {
				t.Fatal("no login_failed audit event")
			}
		})
	}
}

func TestCallback_ExpiredPending(t *testing.T) {
	env := newTestServer(t)
	callback := env.callbackURL(t, authorizeParams())
	env.clock.advance(11 * time.Minute)

	resp, body := get(t, env.client, callback)

	wantPage(t, resp, body, http.StatusBadRequest, expiredCopy)
	env.wantNoSession(t)
}

func TestCallback_StateReplay(t *testing.T) {
	env := newTestServer(t)
	callback := env.callbackURL(t, authorizeParams())
	first, _ := get(t, env.client, callback)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first callback status = %d", first.StatusCode)
	}

	resp, body := get(t, env.client, callback)

	wantPage(t, resp, body, http.StatusBadRequest, expiredCopy)
}

func TestCallback_MissingState(t *testing.T) {
	env := newTestServer(t)

	resp, body := get(t, env.client, env.http.URL+"/callback?code=x")

	wantPage(t, resp, body, http.StatusBadRequest, expiredCopy)
}

func TestCallback_WrongBrowserBinding(t *testing.T) {
	tests := []struct {
		name    string
		browser func(t *testing.T, env *testEnv) *http.Client
	}{
		{"different cookie jar", func(t *testing.T, env *testEnv) *http.Client {
			other := newBrowser(t)
			if resp, _ := get(t, other, env.http.URL+"/authorize?"+authorizeParams().Encode()); resp.StatusCode != http.StatusFound {
				t.Fatalf("authorize status = %d", resp.StatusCode)
			}
			return other
		}},
		{"no cookie", func(t *testing.T, _ *testEnv) *http.Client { return newBrowser(t) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestServer(t)
			callback := env.callbackURL(t, authorizeParams())
			browser := tt.browser(t, env)

			resp, body := get(t, browser, callback)

			wantPage(t, resp, body, http.StatusBadRequest, "different browser")
			if len(env.idp.issuedCodes()) != 1 || len(env.verifiers) != 0 {
				t.Fatal("the upstream code was redeemed despite the binding mismatch")
			}
			env.wantNoSession(t)
		})
	}
}

func TestCallback_MealieRejectsUser(t *testing.T) {
	env := newTestServer(t)
	env.mealie.nativeToken = func(context.Context, mealie.NativeTokenRequest) (mealie.Token, error) {
		return mealie.Token{}, &mealie.APIError{Status: http.StatusUnauthorized, Message: "secret-mealie-body"}
	}

	resp, body := env.callback(t, authorizeParams())

	wantPage(t, resp, body, http.StatusForbidden, mealieRefusedCopy)
	env.wantNoSession(t)
	if strings.Contains(body+env.logs.String(), "secret-mealie-body") {
		t.Fatal("Mealie error body reached the page or the log")
	}
}

func TestCallback_MealieReturnsUnusableToken(t *testing.T) {
	tests := []struct {
		name  string
		token mealie.Token
	}{
		{"empty access token", mealie.Token{ExpiresIn: time.Hour}},
		{"zero lifetime", mealie.Token{AccessToken: secret.FromString("tok")}},
		{"negative lifetime", mealie.Token{AccessToken: secret.FromString("tok"), ExpiresIn: -time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestServer(t)
			env.mealie.nativeToken = func(context.Context, mealie.NativeTokenRequest) (mealie.Token, error) {
				return tt.token, nil
			}

			resp, body := env.callback(t, authorizeParams())

			wantPage(t, resp, body, http.StatusForbidden, mealieRefusedCopy)
			env.wantNoSession(t)
		})
	}
}

func TestCallback_MealieUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		script func(*fakeMealie)
	}{
		{"native token 503", func(m *fakeMealie) {
			m.nativeToken = func(context.Context, mealie.NativeTokenRequest) (mealie.Token, error) {
				return mealie.Token{}, &mealie.APIError{Status: http.StatusServiceUnavailable, Message: "secret-mealie-body"}
			}
		}},
		{"self fails", func(m *fakeMealie) {
			m.self = func(context.Context, secret.Secret) (mealie.User, error) {
				return mealie.User{}, errors.New("secret-mealie-body")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestServer(t)
			tt.script(env.mealie)

			resp, body := env.callback(t, authorizeParams())

			wantPage(t, resp, body, http.StatusBadGateway, "Mealie")
			env.wantNoSession(t)
			if strings.Contains(body+env.logs.String(), "secret-mealie-body") {
				t.Fatal("Mealie error detail reached the page or the log")
			}
		})
	}
}

func TestCallback_SelfWithoutUserIDIsRefused(t *testing.T) {
	env := newTestServer(t)
	env.mealie.self = func(context.Context, secret.Secret) (mealie.User, error) {
		return mealie.User{Username: "ghost"}, nil
	}

	resp, body := env.callback(t, authorizeParams())

	wantPage(t, resp, body, http.StatusForbidden, mealieRefusedCopy)
	env.wantNoSession(t)
}

func TestConsent_FirstTimeShowsPage_SecondTimeSkips(t *testing.T) {
	env := newTestServer(t)
	page := env.loginToConsent(t, env.withResource(authorizeParams()))
	for _, want := range []string{testClientName, "not verified", "127.0.0.1", testUsername} {
		if !strings.Contains(page.body, want) {
			t.Fatalf("consent page lacks %q: %s", want, page.body)
		}
	}

	first, _ := env.decide(t, env.client, page, "allow")
	env.wantCode(t, first)
	second, body := env.callback(t, env.withResource(authorizeParams()))

	if second.StatusCode != http.StatusFound {
		t.Fatalf("second login status = %d, want 302 without consent; body: %s", second.StatusCode, body)
	}
	env.wantCode(t, second)
	if got := len(env.recorder.created()); got != 2 {
		t.Fatalf("created %d sessions, want 2", got)
	}
	for _, cookie := range env.client.Jar.Cookies(mustParse(t, env.http.URL)) {
		if cookie.Name == "mmcp_auth" {
			t.Fatal("binding cookie survives a finished login")
		}
	}
	if !strings.Contains(env.logs.String(), `"event":"consent_granted"`) || !strings.Contains(env.logs.String(), `"event":"login_succeeded"`) {
		t.Fatal("missing consent_granted or login_succeeded audit event")
	}
}

func TestConsent_SessionKeepsMealieExpiryFromCallback(t *testing.T) {
	env := newTestServer(t)
	callbackAt := env.clock.Now()
	page := env.loginToConsent(t, authorizeParams())
	env.clock.advance(5 * time.Minute)
	consentAt := env.clock.Now()

	resp, _ := env.decide(t, env.client, page, "allow")

	env.wantCode(t, resp)
	session := env.onlySession(t)
	if !session.MealieTokenExpiresAt.Equal(callbackAt.Add(testMealieLifetime)) {
		t.Fatalf("mealie expiry = %v, want %v", session.MealieTokenExpiresAt, callbackAt.Add(testMealieLifetime))
	}
	if session.MealieTokenLifetime != testMealieLifetime {
		t.Fatalf("mealie lifetime = %v, want %v", session.MealieTokenLifetime, testMealieLifetime)
	}
	if !session.ExpiresAt.Equal(consentAt.Add(720 * time.Hour)) {
		t.Fatalf("session expiry = %v, want %v", session.ExpiresAt, consentAt.Add(720*time.Hour))
	}
	sealed, err := env.keyring.Open(session.MealieTokenCT, []byte("session:"+session.ID))
	if err != nil || string(sealed) != env.mealieTokens[0] {
		t.Fatalf("session token does not open under the session AAD: %v", err)
	}
}

func TestConsent_Deny(t *testing.T) {
	env := newTestServer(t)
	page := env.loginToConsent(t, authorizeParams())

	resp, _ := env.decide(t, env.client, page, "deny")

	env.wantClientError(t, resp, "access_denied")
	env.wantNoSession(t)
	if _, err := env.store.GetPendingAuth(t.Context(), page.pendingID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("pending auth after deny: err = %v, want ErrNotFound", err)
	}
	if !strings.Contains(env.logs.String(), `"event":"consent_denied"`) {
		t.Fatal("no consent_denied audit event")
	}
}

func TestConsent_CSRFMismatch(t *testing.T) {
	tests := []struct {
		name    string
		browser func(t *testing.T, env *testEnv) *http.Client
		mutate  func(*consentPage)
	}{
		{"wrong csrf", func(_ *testing.T, env *testEnv) *http.Client { return env.client }, func(p *consentPage) { p.csrf = strings.Repeat("0", 64) }},
		{"missing csrf", func(_ *testing.T, env *testEnv) *http.Client { return env.client }, func(p *consentPage) { p.csrf = "" }},
		{"other pending id", func(_ *testing.T, env *testEnv) *http.Client { return env.client }, func(p *consentPage) { p.pendingID = "other" }},
		{"no cookie", func(t *testing.T, _ *testEnv) *http.Client { return newBrowser(t) }, func(*consentPage) {}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestServer(t)
			page := env.loginToConsent(t, authorizeParams())
			tt.mutate(&page)

			resp, body := env.decide(t, tt.browser(t, env), page, "allow")

			wantPage(t, resp, body, http.StatusBadRequest, "")
			env.wantNoSession(t)
		})
	}
}

func TestConsent_UnknownDecision(t *testing.T) {
	env := newTestServer(t)
	page := env.loginToConsent(t, authorizeParams())

	resp, body := env.decide(t, env.client, page, "maybe")

	wantPage(t, resp, body, http.StatusBadRequest, "")
	env.wantNoSession(t)
}

func TestConsent_ExpiredPending(t *testing.T) {
	env := newTestServer(t)
	page := env.loginToConsent(t, authorizeParams())
	env.clock.advance(11 * time.Minute)

	resp, body := env.decide(t, env.client, page, "allow")

	wantPage(t, resp, body, http.StatusBadRequest, expiredCopy)
	env.wantNoSession(t)
}

func TestConsent_PageEscapesClientName(t *testing.T) {
	env := newTestServer(t)
	env.seedClient(t, store.Client{ID: testClientID, Source: "dcr", Name: "<script>alert(1)</script>", RedirectURIs: []string{testClientRedirect}})

	page := env.loginToConsent(t, authorizeParams())

	if strings.Contains(page.body, "<script>") {
		t.Fatal("client name is not escaped")
	}
	if !strings.Contains(page.body, "&lt;script&gt;") {
		t.Fatalf("escaped client name missing: %s", page.body)
	}
}

func TestConsent_PageShowsRedirectHostOnly(t *testing.T) {
	tests := []struct {
		name     string
		redirect string
		want     string
		hidden   []string
	}{
		{"https with userinfo", "https://user:hunter2@app.example.com/cb", "app.example.com", []string{"hunter2", "user:"}},
		{"private-use scheme", "com.example.app:/oauth/cb", "com.example.app", []string{"/oauth/cb"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestServer(t)
			env.seedClient(t, store.Client{ID: testClientID, Source: "dcr", Name: testClientName, RedirectURIs: []string{tt.redirect}})
			params := authorizeParams()
			params.Set("redirect_uri", tt.redirect)

			page := env.loginToConsent(t, params)

			if !strings.Contains(page.body, tt.want) {
				t.Fatalf("consent page lacks %q: %s", tt.want, page.body)
			}
			for _, hidden := range tt.hidden {
				if strings.Contains(page.body, hidden) {
					t.Fatalf("consent page shows %q: %s", hidden, page.body)
				}
			}
		})
	}
}

func TestConsent_UnnamedClient(t *testing.T) {
	env := newTestServer(t)
	env.seedClient(t, store.Client{ID: testClientID, Source: "dcr", RedirectURIs: []string{testClientRedirect}})

	page := env.loginToConsent(t, authorizeParams())

	if !strings.Contains(page.body, "Unnamed app") || !strings.Contains(page.body, "not verified") {
		t.Fatalf("consent page for an unnamed client: %s", page.body)
	}
}

func TestLogin_StoreFailureShowsGenericError(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, env *testEnv) (reply, string)
	}{
		{"authorize", func(t *testing.T, env *testEnv) (reply, string) {
			_ = env.store.Close()
			return env.authorize(t, authorizeParams())
		}},
		{"callback", func(t *testing.T, env *testEnv) (reply, string) {
			callback := env.callbackURL(t, authorizeParams())
			_ = env.store.Close()
			return get(t, env.client, callback)
		}},
		{"consent", func(t *testing.T, env *testEnv) (reply, string) {
			page := env.loginToConsent(t, authorizeParams())
			_ = env.store.Close()
			return env.decide(t, env.client, page, "allow")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestServer(t)

			resp, body := tt.run(t, env)

			wantPage(t, resp, body, http.StatusInternalServerError, "Something went wrong")
			if strings.Contains(strings.ToLower(body), "sql") || strings.Contains(body, "closed") {
				t.Fatalf("error page leaks the store error: %s", body)
			}
		})
	}
}

func TestHTMLPages_SecurityHeaders(t *testing.T) {
	env := newTestServer(t)
	params := authorizeParams()
	params.Set("redirect_uri", "https://evil.example.com/cb")
	errorPage, _ := env.authorize(t, params)
	callback := env.callbackURL(t, authorizeParams())
	consentPage, _ := get(t, env.client, callback)

	policies := map[string]string{
		"error":   "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'",
		"consent": "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' http://127.0.0.1:7777; frame-ancestors 'none'",
	}
	for name, resp := range map[string]reply{"error": errorPage, "consent": consentPage} {
		t.Run(name, func(t *testing.T) {
			want := map[string]string{
				"Content-Security-Policy": policies[name],
				"X-Frame-Options":         "DENY",
				"Content-Type":            "text/html; charset=utf-8",
			}
			for header, value := range want {
				if got := resp.Header.Get(header); got != value {
					t.Errorf("%s = %q, want %q", header, got, value)
				}
			}
		})
	}
}

func TestConsent_FormActionAllowsRedirectTarget(t *testing.T) {
	tests := []struct {
		name     string
		redirect string
		source   string
	}{
		{"loopback http with port", "http://127.0.0.1:7777/callback", "http://127.0.0.1:7777"},
		{"loopback ipv6", "http://[::1]:9000/cb", "http://[::1]:9000"},
		{"https", "https://app.example.com/oauth/cb?x=1", "https://app.example.com"},
		{"private-use scheme", "com.example.app:/oauth/cb", "com.example.app:"},
		{"https with userinfo", "https://user:hunter2@app.example.com:8443/cb", "https://app.example.com:8443"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestServer(t)
			env.seedClient(t, store.Client{ID: testClientID, Source: "dcr", Name: testClientName, RedirectURIs: []string{tt.redirect}})
			params := authorizeParams()
			params.Set("redirect_uri", tt.redirect)

			resp, body := env.callback(t, params)

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("callback status = %d; body: %s", resp.StatusCode, body)
			}
			want := "default-src 'none'; style-src 'unsafe-inline'; form-action 'self' " + tt.source + "; frame-ancestors 'none'"
			if got := resp.Header.Get("Content-Security-Policy"); got != want {
				t.Fatalf("CSP = %q, want %q", got, want)
			}
		})
	}
}

func TestFormActionSource(t *testing.T) {
	tests := []struct {
		redirect string
		source   string
		ok       bool
	}{
		{"http://localhost/cb", "http://localhost", true},
		{"https://APP.example.com/cb", "https://APP.example.com", true},
		{"https://user:pw@app.example.com/cb", "https://app.example.com", true},
		{"com.example.app:/cb", "com.example.app:", true},
		{"https://evil.com;script-src'unsafe-inline'/cb", "", false},
		{"https://a'b.example.com/cb", "", false},
		{"https://a*.example.com/cb", "", false},
		{"https:///cb", "", false},
		{"http://[fe80::1%25en0]:80/cb", "", false},
		{"::not a uri", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.redirect, func(t *testing.T) {
			source, ok := formActionSource(tt.redirect)

			if source != tt.source || ok != tt.ok {
				t.Fatalf("formActionSource = (%q, %v), want (%q, %v)", source, ok, tt.source, tt.ok)
			}
		})
	}
}

func TestConsent_UnsafeRedirectKeepsSelfOnlyFormAction(t *testing.T) {
	env := newTestServer(t)
	redirect := "https://evil.com;script-src'unsafe-inline'/cb"
	env.seedClient(t, store.Client{ID: testClientID, Source: "dcr", Name: testClientName, RedirectURIs: []string{redirect}})
	params := authorizeParams()
	params.Set("redirect_uri", redirect)

	resp, body := env.callback(t, params)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d; body: %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Security-Policy"); got != "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'" {
		t.Fatalf("CSP = %q", got)
	}
}

func TestLogin_NoSecretsInLogsOrPages(t *testing.T) {
	env := newTestServer(t)
	callback := env.callbackURL(t, authorizeParams())
	consent, consentBody := get(t, env.client, callback)
	if consent.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", consent.StatusCode)
	}
	page := consentPage{body: consentBody, pendingID: pendingIDField.FindStringSubmatch(consentBody)[1], csrf: csrfField.FindStringSubmatch(consentBody)[1]}
	resp, _ := env.decide(t, env.client, page, "allow")
	code := env.wantCode(t, resp)

	upstreamState := mustParse(t, callback).Query().Get("state")
	secrets := append([]string{code, upstreamState, env.mealieTokens[0], env.idp.issuedCodes()[0]}, env.verifiers...)
	for _, s := range secrets {
		if strings.Contains(env.logs.String(), s) {
			t.Fatalf("log contains secret %q", s)
		}
		if strings.Contains(consentBody, s) {
			t.Fatalf("consent page contains secret %q", s)
		}
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	return u
}

func TestLogin_StoreStepFailureShowsGenericError(t *testing.T) {
	cimdParams := func(env *testEnv) url.Values {
		clientID := "https://app.example.com/oauth/client.json"
		env.cimd.metadata[clientID] = ClientMetadata{ClientID: clientID, ClientName: "CIMD App", RedirectURIs: []string{testClientRedirect}}
		params := authorizeParams()
		params.Set("client_id", clientID)
		return params
	}
	failAtAuthorize := func(params func(*testEnv) url.Values) func(*testing.T, *testEnv, string) (reply, string) {
		return func(t *testing.T, env *testEnv, failing string) (reply, string) {
			env.failStore(failing)
			return env.authorize(t, params(env))
		}
	}
	failAtCallback := func(t *testing.T, env *testEnv, failing string) (reply, string) {
		callback := env.callbackURL(t, authorizeParams())
		env.failStore(failing)
		return get(t, env.client, callback)
	}
	failAtDecision := func(decision string) func(*testing.T, *testEnv, string) (reply, string) {
		return func(t *testing.T, env *testEnv, failing string) (reply, string) {
			page := env.loginToConsent(t, authorizeParams())
			env.failStore(failing)
			return env.decide(t, env.client, page, decision)
		}
	}
	tests := []struct {
		failing string
		run     func(*testing.T, *testEnv, string) (reply, string)
	}{
		{"CreatePendingAuth", failAtAuthorize(func(*testEnv) url.Values { return authorizeParams() })},
		{"UpsertClient", failAtAuthorize(cimdParams)},
		{"SetPendingAuthLogin", failAtCallback},
		{"HasConsent", failAtCallback},
		{"GetClient", failAtCallback},
		{"GrantConsent", failAtDecision("allow")},
		{"CreateSessionWithCode", failAtDecision("allow")},
		{"DeletePendingAuth", failAtDecision("deny")},
	}
	for _, tt := range tests {
		t.Run(tt.failing, func(t *testing.T) {
			env := newTestServer(t)

			resp, body := tt.run(t, env, tt.failing)

			wantPage(t, resp, body, http.StatusInternalServerError, "Something went wrong")
			if strings.Contains(body, storeFailureDetail) {
				t.Fatalf("error page leaks the store error: %s", body)
			}
			env.wantNoSession(t)
		})
	}
}

func TestCallback_PendingCleanupFailureIsLogged(t *testing.T) {
	env := newTestServer(t)
	env.mealie.nativeToken = func(context.Context, mealie.NativeTokenRequest) (mealie.Token, error) {
		return mealie.Token{}, &mealie.APIError{Status: http.StatusUnauthorized}
	}
	callback := env.callbackURL(t, authorizeParams())
	env.failStore("DeletePendingAuth")

	resp, body := get(t, env.client, callback)

	wantPage(t, resp, body, http.StatusForbidden, mealieRefusedCopy)
	if !strings.Contains(env.logs.String(), `"msg":"deleting pending auth"`) {
		t.Fatalf("cleanup failure not logged: %s", env.logs.String())
	}
}

func TestConsent_PendingCleanupFailureStillIssuesCode(t *testing.T) {
	env := newTestServer(t)
	page := env.loginToConsent(t, authorizeParams())
	env.failStore("DeletePendingAuth")

	resp, _ := env.decide(t, env.client, page, "allow")

	env.wantCode(t, resp)
	if !strings.Contains(env.logs.String(), `"msg":"deleting pending auth"`) {
		t.Fatalf("cleanup failure not logged: %s", env.logs.String())
	}
}

func TestConsent_PendingVanished(t *testing.T) {
	env := newTestServer(t)
	page := env.loginToConsent(t, authorizeParams())
	if err := env.store.DeletePendingAuth(t.Context(), page.pendingID); err != nil {
		t.Fatalf("DeletePendingAuth: %v", err)
	}

	resp, body := env.decide(t, env.client, page, "allow")

	wantPage(t, resp, body, http.StatusBadRequest, expiredCopy)
	env.wantNoSession(t)
}

func TestConsent_BeforeLoginIsRejected(t *testing.T) {
	env := newTestServer(t)
	env.callbackURL(t, authorizeParams())
	base, err := url.Parse(env.http.URL)
	if err != nil {
		t.Fatal(err)
	}
	var page consentPage
	for _, cookie := range env.client.Jar.Cookies(base) {
		if cookie.Name == bindingCookieName {
			pendingID, binding, _ := strings.Cut(cookie.Value, ".")
			page = consentPage{pendingID: pendingID, csrf: csrfToken(binding)}
		}
	}
	if page.pendingID == "" {
		t.Fatal("authorize set no binding cookie")
	}

	resp, body := env.decide(t, env.client, page, "allow")

	wantPage(t, resp, body, http.StatusBadRequest, "")
	env.wantNoSession(t)
}

func TestConsent_UnreadableMealieToken(t *testing.T) {
	env := newTestServer(t)
	page := env.loginToConsent(t, authorizeParams())
	err := env.store.SetPendingAuthLogin(t.Context(), page.pendingID, store.Login{
		MealieUserID:         "user-1",
		MealieUsername:       "alice",
		MealieTokenCT:        []byte("not a sealed token"),
		MealieTokenExpiresAt: env.clock.Now().Add(time.Hour),
		MealieTokenLifetime:  time.Hour,
	})
	if err != nil {
		t.Fatalf("SetPendingAuthLogin: %v", err)
	}

	resp, body := env.decide(t, env.client, page, "allow")

	wantPage(t, resp, body, http.StatusInternalServerError, "Something went wrong")
	env.wantNoSession(t)
}

func TestDisplayHost(t *testing.T) {
	tests := map[string]string{
		"https://app.example.com/cb": "app.example.com",
		"myapp:/callback":            "myapp",
		"http://[::1":                "",
	}
	for raw, want := range tests {
		if got := displayHost(raw); got != want {
			t.Errorf("displayHost(%q) = %q, want %q", raw, got, want)
		}
	}
}
