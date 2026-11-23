package oauth

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
)

func browserRoutes(env *testEnv, oidc, password bool) *http.ServeMux {
	env.srv.cfg.EnableOIDC = oidc
	env.srv.cfg.EnablePasswordLogin = password
	mux := http.NewServeMux()
	env.srv.Routes(mux)
	return mux
}

func browserRequest(mux *http.ServeMux, method, target string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), method, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Forwarded", "proto=https")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	return resp
}

func startBrowserLogin(t *testing.T, mux *http.ServeMux) (*http.Cookie, url.Values, string) {
	t.Helper()
	resp := browserRequest(mux, http.MethodGet, "https://mcp.example/authorize?"+authorizeParams().Encode(), nil, nil)
	body := resp.Body.String()
	pending := pendingIDField.FindStringSubmatch(body)
	csrf := csrfField.FindStringSubmatch(body)
	cookies := resp.Result().Cookies()
	if resp.Code != http.StatusOK || pending == nil || csrf == nil || len(cookies) != 1 {
		t.Fatalf("authorize status=%d, form or cookie missing: %s", resp.Code, body)
	}
	if cookies[0].Name != "__Host-mmcp_auth" || !cookies[0].HttpOnly || !cookies[0].Secure ||
		cookies[0].SameSite != http.SameSiteLaxMode || resp.Header().Get("Cache-Control") != "no-store" ||
		!strings.Contains(resp.Header().Get("Content-Security-Policy"), "form-action 'self';") {
		t.Fatal("password page lacks browser protections")
	}
	return cookies[0], url.Values{
		"pending_id": {pending[1]}, "csrf": {csrf[1]}, "username": {"alice"}, "password": {"password-marker"},
	}, body
}

func TestBrowserLoginMethods(t *testing.T) {
	t.Run("oidc only", func(t *testing.T) {
		env := newTestServer(t)
		mux := browserRoutes(env, true, false)
		resp := browserRequest(mux, http.MethodGet, "https://mcp.example/authorize?"+authorizeParams().Encode(), nil, nil)
		if resp.Code != http.StatusFound || !strings.Contains(resp.Header().Get("Location"), env.idp.server.URL) {
			t.Fatalf("authorization did not redirect to OIDC, status=%d", resp.Code)
		}
		if got := browserRequest(mux, http.MethodPost, "https://mcp.example"+passwordPostPath, nil, nil).Code; got != http.StatusNotFound {
			t.Fatalf("password route returned %d", got)
		}
	})
	t.Run("password only", func(t *testing.T) {
		env := newTestServer(t)
		mux := browserRoutes(env, false, true)
		env.mealie.nativeConfig = func(context.Context) (mealie.NativeConfig, error) {
			t.Fatal("password-only authorization called native config")
			return mealie.NativeConfig{}, nil
		}
		plaintext := browserRequest(mux, http.MethodGet, "http://mcp.example/authorize?"+authorizeParams().Encode(), nil, nil)
		if plaintext.Code != http.StatusBadRequest || strings.Contains(plaintext.Body.String(), `type="password"`) {
			t.Fatalf("plaintext authorization served the sign-in form or returned an unexpected status: %d", plaintext.Code)
		}
		cookie, _, body := startBrowserLogin(t, mux)
		if strings.Contains(body, "Continue with OIDC") {
			t.Fatal("password-only page links to OIDC")
		}
		if got := browserRequest(mux, http.MethodGet, "https://mcp.example/callback", nil, cookie).Code; got != http.StatusNotFound {
			t.Fatalf("callback route returned %d", got)
		}
	})
	t.Run("dual", func(t *testing.T) {
		env := newTestServer(t)
		mux := browserRoutes(env, true, true)
		_, _, body := startBrowserLogin(t, mux)
		if !strings.Contains(body, "Continue with OIDC") || !strings.Contains(body, env.idp.server.URL) {
			t.Fatal("dual login page lacks OIDC link")
		}
	})
}

func TestPasswordLoginRejectsUnboundAndOversizedSubmissions(t *testing.T) {
	env := newTestServer(t)
	mux := browserRoutes(env, false, true)
	cookie, form, _ := startBrowserLogin(t, mux)
	for _, name := range []string{"plaintext", "bad csrf", "different attempt", "missing cookie", "invalid binding", "oversized", "expired"} {
		t.Run(name, func(t *testing.T) {
			target := "https://mcp.example" + passwordPostPath
			values, _ := url.ParseQuery(form.Encode())
			bound := cookie
			switch name {
			case "plaintext":
				target = "http://mcp.example" + passwordPostPath
			case "bad csrf":
				values.Set("csrf", "incorrect")
			case "different attempt":
				values.Set("pending_id", "incorrect")
			case "invalid binding":
				bound = env.srv.bindingCookie(form.Get("pending_id")+".incorrect", 0)
				values.Set("csrf", csrfToken("incorrect"))
			case "missing cookie":
				bound = nil
			case "oversized":
				values.Set("password", strings.Repeat("x", 64<<10))
			case "expired":
				env.clock.advance(pendingAuthLifetime + time.Second)
			}
			resp := browserRequest(mux, http.MethodPost, target, values, bound)
			if resp.Code < 400 || resp.Code >= 500 || strings.Contains(resp.Body.String(), form.Get("password")) {
				t.Fatalf("invalid request returned %d: %s", resp.Code, resp.Body.String())
			}
		})
	}
}

func TestPasswordLoginRejectsMalformedForm(t *testing.T) {
	env := newTestServer(t)
	mux := browserRoutes(env, false, true)
	cookie, form, _ := startBrowserLogin(t, mux)
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://mcp.example"+passwordPostPath, strings.NewReader(form.Encode()+"&extra=%ZZ"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(cookie)
	resp := httptest.NewRecorder()
	mux.ServeHTTP(resp, req)
	wantPage(t, reply{StatusCode: resp.Code, Header: resp.Header()}, resp.Body.String(), http.StatusBadRequest, msgInvalidPasswordForm)
}

func TestPasswordLoginPendingLookupFailure(t *testing.T) {
	env := newTestServer(t)
	mux := browserRoutes(env, false, true)
	cookie, form, _ := startBrowserLogin(t, mux)
	env.failStore("GetPendingAuth")
	resp := browserRequest(mux, http.MethodPost, "https://mcp.example"+passwordPostPath, form, cookie)
	body := resp.Body.String()
	wantPage(t, reply{StatusCode: resp.Code, Header: resp.Header()}, body, http.StatusInternalServerError, msgInternal)
	if strings.Contains(body, storeFailureDetail) {
		t.Fatal("password error page exposes the store error")
	}
}

func TestBrowserLoginMethodsShareOneClaim(t *testing.T) {
	env := newTestServer(t)
	mux := browserRoutes(env, true, true)
	var exchanges atomic.Int32
	tok := mealie.Token{AccessToken: secret.FromString("test-token"), ExpiresIn: time.Hour}
	env.mealie.nativeToken = func(context.Context, mealie.NativeTokenRequest) (mealie.Token, error) {
		exchanges.Add(1)
		return tok, nil
	}
	env.mealie.passwordToken = func(_ context.Context, username string, password secret.Secret) (mealie.Token, error) {
		if username != "alice" || password.Reveal() != "password-marker" {
			t.Error("unexpected credential inputs")
		}
		exchanges.Add(1)
		return tok, nil
	}
	cookie, form, body := startBrowserLogin(t, mux)
	link := regexp.MustCompile(`href="([^"]+)">Continue with OIDC`).FindStringSubmatch(body)
	if link == nil {
		t.Fatal("OIDC link missing")
	}
	upstream, err := url.Parse(html.UnescapeString(link[1]))
	if err != nil {
		t.Fatal(err)
	}
	callback := "https://mcp.example/callback?code=valid&state=" + url.QueryEscape(upstream.Query().Get("state"))
	target := "https://mcp.example" + passwordPostPath
	start := make(chan struct{})
	var result [2]*httptest.ResponseRecorder
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		result[0] = browserRequest(mux, http.MethodGet, callback, nil, cookie)
	})
	wg.Go(func() {
		<-start
		result[1] = browserRequest(mux, http.MethodPost, target, form, cookie)
	})
	close(start)
	wg.Wait()
	if exchanges.Load() != 1 {
		t.Fatalf("made %d upstream exchanges for one authorization", exchanges.Load())
	}
	successes := 0
	for _, resp := range result {
		if resp.Code == http.StatusOK && strings.Contains(resp.Body.String(), "Allow access to Mealie?") {
			successes++
		} else if resp.Code != http.StatusBadRequest {
			t.Fatalf("unexpected concurrent result %d: %s", resp.Code, resp.Body.String())
		}
	}
	if successes != 1 {
		t.Fatalf("got %d consent pages, want one", successes)
	}
	if got := browserRequest(mux, http.MethodPost, target, form, cookie).Code; got != http.StatusBadRequest {
		t.Fatalf("replayed password form returned %d", got)
	}
}

func TestPasswordLoginCompletesConsentOverTLS(t *testing.T) {
	env := newTestServer(t)
	tlsServer := httptest.NewTLSServer(browserRoutes(env, false, true))
	t.Cleanup(tlsServer.Close)
	client := newBrowser(t)
	client.Transport = tlsServer.Client().Transport
	env.mealie.passwordToken = func(_ context.Context, username string, password secret.Secret) (mealie.Token, error) {
		if username != "alice" || password.Reveal() != "password-marker" {
			t.Error("unexpected credential inputs")
		}
		return mealie.Token{AccessToken: secret.FromString("password-login-mealie-token"), ExpiresIn: time.Hour}, nil
	}
	resp, body := get(t, client, tlsServer.URL+"/authorize?"+authorizeParams().Encode())
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize returned %d: %s", resp.StatusCode, body)
	}
	pending := pendingIDField.FindStringSubmatch(body)
	csrf := csrfField.FindStringSubmatch(body)
	if pending == nil || csrf == nil {
		t.Fatal("password form missing proof")
	}
	resp, body = postForm(t, client, tlsServer.URL+passwordPostPath, url.Values{
		"pending_id": {pending[1]}, "csrf": {csrf[1]}, "username": {"alice"}, "password": {"password-marker"},
	})
	wantPage(t, resp, body, http.StatusOK, "Allow access to Mealie?")
	env.wantNoSession(t)
	resp, _ = postForm(t, client, tlsServer.URL+"/consent", url.Values{
		"pending_id": {pending[1]}, "csrf": {csrf[1]}, "decision": {"allow"},
	})
	env.wantCode(t, resp)
	session := env.onlySession(t)
	if session.MealieUserID != testUserID || strings.Contains(string(session.MealieTokenCT), "password-login-mealie-token") {
		t.Fatal("session is missing its expected identity or encrypted token")
	}
	if strings.Contains(env.logs.String(), "password-marker") {
		t.Fatal("logs contain the submitted password")
	}
}

func TestPasswordLoginFailureDoesNotLogOrReflectCredentialsAndRequiresRestart(t *testing.T) {
	env := newTestServer(t)
	mux := browserRoutes(env, false, true)
	var exchanges int
	env.mealie.passwordToken = func(_ context.Context, _ string, password secret.Secret) (mealie.Token, error) {
		exchanges++
		return mealie.Token{}, &mealie.APIError{Status: http.StatusUnauthorized, Message: password.Reveal()}
	}
	cookie, form, _ := startBrowserLogin(t, mux)
	target := "https://mcp.example" + passwordPostPath
	resp := browserRequest(mux, http.MethodPost, target, form, cookie)
	if resp.Code != http.StatusForbidden || !strings.Contains(resp.Body.String(), "Start again from your AI app") {
		t.Fatalf("rejected credentials returned %d: %s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), form.Get("password")) || strings.Contains(env.logs.String(), form.Get("password")) {
		t.Fatal("upstream password error escaped into logs or browser response")
	}
	if got := browserRequest(mux, http.MethodPost, target, form, cookie).Code; got != http.StatusBadRequest || exchanges != 1 {
		t.Fatalf("replayed password form returned %d after %d upstream exchanges", got, exchanges)
	}
}

func TestPasswordLoginWithPriorConsentUsesCredentialFreeContinuation(t *testing.T) {
	for _, decision := range []string{"continue", "cancel"} {
		t.Run(decision, func(t *testing.T) {
			env := newTestServer(t)
			if err := env.store.GrantConsent(t.Context(), testUserID, testClientID, env.clock.Now()); err != nil {
				t.Fatal(err)
			}
			mux := browserRoutes(env, false, true)
			env.mealie.passwordToken = func(context.Context, string, secret.Secret) (mealie.Token, error) {
				return mealie.Token{AccessToken: secret.FromString("test-token"), ExpiresIn: time.Hour}, nil
			}
			cookie, form, _ := startBrowserLogin(t, mux)
			resp := browserRequest(mux, http.MethodPost, "https://mcp.example"+passwordPostPath, form, cookie)
			wantContinuation(t, resp, decision)
			env.wantNoSession(t)
			resp = browserRequest(mux, http.MethodPost, "https://mcp.example/consent", url.Values{
				"pending_id": form["pending_id"], "csrf": form["csrf"], "decision": {decision},
			}, cookie)
			if resp.Code != http.StatusFound {
				t.Fatalf("consent returned %d", resp.Code)
			}
			if decision == "continue" {
				env.wantCode(t, reply{StatusCode: resp.Code, Header: resp.Header()})
				env.onlySession(t)
			} else {
				env.wantClientError(t, reply{StatusCode: resp.Code, Header: resp.Header()}, "access_denied")
				env.wantNoSession(t)
			}
			granted, err := env.store.HasConsent(t.Context(), testUserID, testClientID)
			if err != nil || !granted {
				t.Fatalf("existing consent lost: granted=%t, err=%v", granted, err)
			}
			if logs := env.logs.String(); strings.Contains(logs, "consent_granted") || strings.Contains(logs, "consent_denied") {
				t.Fatal("continuation recorded a new consent decision")
			}
		})
	}
}

func wantContinuation(t *testing.T, resp *httptest.ResponseRecorder, decision string) {
	t.Helper()
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), "Continue to "+testClientName) ||
		!strings.Contains(resp.Header().Get("Content-Security-Policy"), "127.0.0.1:7777") ||
		!strings.Contains(resp.Body.String(), `name="decision" value="`+decision+`"`) {
		t.Fatalf("expected a continuation page permitted to return to the client; got %d", resp.Code)
	}
}

func TestConsent_ContinueWithoutPriorGrantIsRejected(t *testing.T) {
	env := newTestServer(t)
	page := env.loginToConsent(t, authorizeParams())
	resp, body := env.decide(t, env.client, page, "continue")
	wantPage(t, resp, body, http.StatusBadRequest, msgInvalidConsent)
	env.wantNoSession(t)
}
