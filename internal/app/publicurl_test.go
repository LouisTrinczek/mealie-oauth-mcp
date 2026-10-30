package app

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
)

const (
	spoofedHost      = "evil.example"
	spoofClientState = "client-state"
	spoofRedirectURI = "http://127.0.0.1/cb"
)

var formField = regexp.MustCompile(`(action|name="pending_id" value|name="csrf" value)="([^"]*)"`)

type spoofingBrowser struct {
	handler http.Handler
	cookies []*http.Cookie
}

func (b *spoofingBrowser) do(t *testing.T, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, target, body)
	req.Host = spoofedHost
	req.Header.Set("X-Forwarded-Host", spoofedHost)
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Header.Set("Forwarded", "for=203.0.113.9;host="+spoofedHost+";proto=http")
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, cookie := range b.cookies {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	b.handler.ServeHTTP(rec, req)
	b.cookies = append(b.cookies, rec.Result().Cookies()...)
	return rec
}

func (b *spoofingBrowser) json(t *testing.T, target string) map[string]any {
	t.Helper()
	rec := b.do(t, http.MethodGet, target, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want 200", target, rec.Code)
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("decode %s: %v", target, err)
	}
	return doc
}

func redirectTarget(t *testing.T, rec *httptest.ResponseRecorder) *url.URL {
	t.Helper()
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302; body: %s", rec.Code, rec.Body)
	}
	target, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	return target
}

func consentFields(t *testing.T, page string) map[string]string {
	t.Helper()
	fields := map[string]string{}
	for _, match := range formField.FindAllStringSubmatch(page, -1) {
		fields[match[1]] = html.UnescapeString(match[2])
	}
	if len(fields) != 3 {
		t.Fatalf("consent form fields = %v, want action, pending_id and csrf; page: %s", fields, page)
	}
	return fields
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func wantValue(t *testing.T, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s = %#v, want %#v", what, got, want)
	}
}

func TestApp_PublicURLIgnoresSpoofedHostHeaders(t *testing.T) {
	a, fake := startApp(t)
	if err := a.store.UpsertClient(t.Context(), store.Client{ID: testClientID, Source: "dcr", Name: "App", RedirectURIs: []string{spoofRedirectURI}}); err != nil {
		t.Fatalf("UpsertClient: %v", err)
	}
	browser := &spoofingBrowser{handler: a.Handler()}
	resource := testPublicURL + "/mcp"

	asMetadata := browser.json(t, "/.well-known/oauth-authorization-server")
	wantValue(t, "issuer", asMetadata["issuer"], testPublicURL)
	endpoints := map[string]string{
		"authorization_endpoint": "/authorize",
		"token_endpoint":         "/token",
		"registration_endpoint":  "/register",
		"revocation_endpoint":    "/revoke",
	}
	for key, path := range endpoints {
		wantValue(t, key, asMetadata[key], testPublicURL+path)
	}
	for _, path := range []string{"/.well-known/oauth-protected-resource", testMetadataPath} {
		prMetadata := browser.json(t, path)
		wantValue(t, path+" resource", prMetadata["resource"], resource)
		wantValue(t, path+" authorization_servers", prMetadata["authorization_servers"], []any{testPublicURL})
	}

	unauthenticated := browser.do(t, http.MethodPost, "/mcp", nil)
	wantChallenge := `resource_metadata="` + testPublicURL + testMetadataPath + `"`
	if got := unauthenticated.Header().Get("WWW-Authenticate"); unauthenticated.Code != http.StatusUnauthorized || !strings.Contains(got, wantChallenge) {
		t.Errorf("/mcp = %d with WWW-Authenticate %q, want 401 with %s", unauthenticated.Code, got, wantChallenge)
	}

	authorize := url.Values{
		"response_type":         {"code"},
		"client_id":             {testClientID},
		"redirect_uri":          {spoofRedirectURI},
		"state":                 {spoofClientState},
		"code_challenge":        {pkceChallenge(strings.Repeat("v", 43))},
		"code_challenge_method": {"S256"},
		"resource":              {resource},
	}
	upstream := redirectTarget(t, browser.do(t, http.MethodGet, "/authorize?"+authorize.Encode(), nil))
	wantValue(t, "upstream redirect_uri", upstream.Query().Get("redirect_uri"), testPublicURL+"/callback")

	callback := url.Values{"code": {"upstream-code"}, "state": {upstream.Query().Get("state")}}
	consentPage := browser.do(t, http.MethodGet, "/callback?"+callback.Encode(), nil)
	if consentPage.Code != http.StatusOK {
		t.Fatalf("callback status = %d, want the consent page; body: %s", consentPage.Code, consentPage.Body)
	}
	wantValue(t, "Mealie exchange redirect_uri", fake.seenExchangeRedirects(), []string{testPublicURL + "/callback"})
	form := consentFields(t, consentPage.Body.String())
	wantValue(t, "consent form action", form["action"], testPublicURL+"/consent")

	decision := url.Values{"pending_id": {form[`name="pending_id" value`]}, "csrf": {form[`name="csrf" value`]}, "decision": {"allow"}}
	back := redirectTarget(t, browser.do(t, http.MethodPost, "/consent", strings.NewReader(decision.Encode())))
	wantValue(t, "client redirect", back.Scheme+"://"+back.Host+back.Path, spoofRedirectURI)
	wantValue(t, "iss", back.Query().Get("iss"), testPublicURL)
	wantValue(t, "state", back.Query().Get("state"), spoofClientState)
	if back.Query().Get("code") == "" {
		t.Error("redirect back to the client carries no code")
	}
}
