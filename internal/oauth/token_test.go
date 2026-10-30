package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/token"
)

func (e *testEnv) loginToCode(t *testing.T) string {
	t.Helper()
	page := e.loginToConsent(t, authorizeParams())
	resp, _ := e.decide(t, e.client, page, "allow")
	return e.wantCode(t, resp)
}

func codeGrant(code string) url.Values {
	return url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {testClientRedirect},
		"client_id":     {testClientID},
		"code_verifier": {testClientVerifier},
	}
}

func refreshGrant(refreshToken string) url.Values {
	return url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {testClientID},
	}
}

func (e *testEnv) postToken(t *testing.T, form url.Values) (reply, string) {
	t.Helper()
	return postForm(t, e.client, e.http.URL+"/token", form)
}

func (e *testEnv) wantTokens(t *testing.T, form url.Values) tokenResponse {
	t.Helper()
	resp, body := e.postToken(t, form)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var tokens tokenResponse
	if err := json.Unmarshal([]byte(body), &tokens); err != nil {
		t.Fatalf("decoding token response: %v", err)
	}
	if !token.HasKind(tokens.AccessToken, token.Access) || !token.HasKind(tokens.RefreshToken, token.Refresh) {
		t.Fatalf("token kinds wrong: %s", body)
	}
	return tokens
}

func (e *testEnv) wantTokenError(t *testing.T, form url.Values, status int, code string) {
	t.Helper()
	resp, body := e.postToken(t, form)
	if resp.StatusCode != status {
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, status, body)
	}
	var got oauthError
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decoding error response: %v; body: %s", err, body)
	}
	if got.Error != code || got.Description == "" {
		t.Fatalf("error = %+v, want %q with a description", got, code)
	}
	if strings.Contains(body, "mmcp_") {
		t.Fatalf("error body leaks a token: %s", body)
	}
}

func (e *testEnv) wantAccessToken(t *testing.T, accessToken string) store.AccessToken {
	t.Helper()
	stored, err := e.store.GetAccessToken(t.Context(), token.Hash(accessToken))
	if err != nil {
		t.Fatalf("GetAccessToken: %v", err)
	}
	return stored
}

func (e *testEnv) wantAccessTokenGone(t *testing.T, accessToken string) {
	t.Helper()
	if _, err := e.store.GetAccessToken(t.Context(), token.Hash(accessToken)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("GetAccessToken err = %v, want ErrNotFound", err)
	}
}

func (e *testEnv) wantRevoked(t *testing.T) {
	t.Helper()
	if e.onlySession(t).RevokedAt.IsZero() {
		t.Fatal("session is not revoked")
	}
}

func (e *testEnv) wantNotRevoked(t *testing.T) {
	t.Helper()
	if !e.onlySession(t).RevokedAt.IsZero() {
		t.Fatal("session was revoked")
	}
}

func (e *testEnv) wantAudit(t *testing.T, event string) {
	t.Helper()
	if !strings.Contains(e.logs.String(), `"event":"`+event+`"`) {
		t.Fatalf("no %s audit event in logs: %s", event, e.logs.String())
	}
}

func TestToken_AuthCodeHappyPath(t *testing.T) {
	env := newTestServer(t)
	code := env.loginToCode(t)
	issuedAt := env.clock.Now()

	resp, body := env.postToken(t, codeGrant(code))

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", got)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	var tokens tokenResponse
	if err := json.Unmarshal([]byte(body), &tokens); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if tokens.TokenType != "Bearer" || tokens.ExpiresIn != 3600 || tokens.Scope != "mealie" {
		t.Fatalf("token response = %+v", tokens)
	}
	if !token.HasKind(tokens.AccessToken, token.Access) || !token.HasKind(tokens.RefreshToken, token.Refresh) {
		t.Fatalf("token kinds wrong: %+v", tokens)
	}
	session := env.onlySession(t)
	access := env.wantAccessToken(t, tokens.AccessToken)
	if access.SessionID != session.ID || !access.ExpiresAt.Equal(issuedAt.Add(time.Hour)) {
		t.Fatalf("access token = %+v, want session %s expiring at %v", access, session.ID, issuedAt.Add(time.Hour))
	}
	refresh, err := env.store.GetRefreshToken(t.Context(), token.Hash(tokens.RefreshToken))
	if err != nil {
		t.Fatalf("GetRefreshToken: %v", err)
	}
	if refresh.SessionID != session.ID || !refresh.ExpiresAt.Equal(session.ExpiresAt) {
		t.Fatalf("refresh token = %+v, want session %s expiring at %v", refresh, session.ID, session.ExpiresAt)
	}
	env.wantAudit(t, "token_issued")
}

func TestToken_CodeReuseRevokesSession(t *testing.T) {
	env := newTestServer(t)
	code := env.loginToCode(t)
	first := env.wantTokens(t, codeGrant(code))

	env.wantTokenError(t, codeGrant(code), http.StatusBadRequest, "invalid_grant")

	env.wantAccessTokenGone(t, first.AccessToken)
	env.wantRevoked(t)
	env.wantAudit(t, "refresh_reuse_detected")
	if !strings.Contains(env.logs.String(), `"kind":"code"`) {
		t.Fatalf("reuse audit lacks kind=code: %s", env.logs.String())
	}
}

func TestToken_CodeFromRevokedSession(t *testing.T) {
	env := newTestServer(t)
	code := env.loginToCode(t)
	if err := env.store.RevokeSession(t.Context(), env.onlySession(t).ID, env.clock.Now()); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	env.wantTokenError(t, codeGrant(code), http.StatusBadRequest, "invalid_grant")
}

func TestToken_MissingClient(t *testing.T) {
	env := newTestServer(t)
	form := codeGrant(env.loginToCode(t))
	form.Del("client_id")

	env.wantTokenError(t, form, http.StatusUnauthorized, "invalid_client")
}

func TestToken_WrongVerifier(t *testing.T) {
	env := newTestServer(t)
	form := codeGrant(env.loginToCode(t))
	form.Set("code_verifier", strings.Repeat("v", 43))

	env.wantTokenError(t, form, http.StatusBadRequest, "invalid_grant")
	env.wantNotRevoked(t)
}

func TestToken_WrongRedirectURI(t *testing.T) {
	env := newTestServer(t)
	form := codeGrant(env.loginToCode(t))
	form.Set("redirect_uri", "http://127.0.0.1:7778/callback")

	env.wantTokenError(t, form, http.StatusBadRequest, "invalid_grant")
}

func TestToken_WrongClient(t *testing.T) {
	env := newTestServer(t)
	form := codeGrant(env.loginToCode(t))
	form.Set("client_id", "another-client")

	env.wantTokenError(t, form, http.StatusBadRequest, "invalid_grant")
}

func TestToken_ExpiredCode(t *testing.T) {
	env := newTestServer(t)
	code := env.loginToCode(t)
	env.clock.advance(61 * time.Second)

	env.wantTokenError(t, codeGrant(code), http.StatusBadRequest, "invalid_grant")
	env.wantNotRevoked(t)
}

func TestToken_ResourceMismatch(t *testing.T) {
	env := newTestServer(t)
	form := codeGrant(env.loginToCode(t))
	form.Set("resource", "https://evil.example/mcp")

	env.wantTokenError(t, form, http.StatusBadRequest, "invalid_target")
}

func TestToken_MatchingResourceAccepted(t *testing.T) {
	env := newTestServer(t)
	form := codeGrant(env.loginToCode(t))
	form.Set("resource", env.http.URL+"/mcp/")

	env.wantTokens(t, form)
}

func TestToken_RefreshRotates(t *testing.T) {
	env := newTestServer(t)
	first := env.wantTokens(t, codeGrant(env.loginToCode(t)))
	firstExpiry := env.wantAccessToken(t, first.AccessToken).ExpiresAt
	env.clock.advance(10 * time.Minute)

	second := env.wantTokens(t, refreshGrant(first.RefreshToken))

	if second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh did not issue new tokens")
	}
	if got := env.wantAccessToken(t, first.AccessToken).ExpiresAt; !got.Equal(firstExpiry) {
		t.Fatalf("old access token expiry = %v, want unchanged %v", got, firstExpiry)
	}
	if got := env.wantAccessToken(t, second.AccessToken).ExpiresAt; !got.Equal(env.clock.Now().Add(time.Hour)) {
		t.Fatalf("new access token expiry = %v", got)
	}
	third := env.wantTokens(t, refreshGrant(second.RefreshToken))
	env.wantAccessToken(t, third.AccessToken)
	env.wantAudit(t, "token_refreshed")

	env.wantTokenError(t, refreshGrant(first.RefreshToken), http.StatusBadRequest, "invalid_grant")
}

func TestToken_RefreshReuseRevokesSession(t *testing.T) {
	env := newTestServer(t)
	first := env.wantTokens(t, codeGrant(env.loginToCode(t)))
	second := env.wantTokens(t, refreshGrant(first.RefreshToken))

	env.wantTokenError(t, refreshGrant(first.RefreshToken), http.StatusBadRequest, "invalid_grant")

	env.wantRevoked(t)
	env.wantAccessTokenGone(t, second.AccessToken)
	env.wantTokenError(t, refreshGrant(second.RefreshToken), http.StatusBadRequest, "invalid_grant")
	env.wantAudit(t, "refresh_reuse_detected")
	if !strings.Contains(env.logs.String(), `"kind":"refresh"`) {
		t.Fatalf("reuse audit lacks kind=refresh: %s", env.logs.String())
	}
}

func TestToken_RefreshWrongClient(t *testing.T) {
	env := newTestServer(t)
	first := env.wantTokens(t, codeGrant(env.loginToCode(t)))
	form := refreshGrant(first.RefreshToken)
	form.Set("client_id", "another-client")

	env.wantTokenError(t, form, http.StatusBadRequest, "invalid_grant")
	env.wantTokens(t, refreshGrant(first.RefreshToken))
}

func TestToken_RefreshUnknownToken(t *testing.T) {
	env := newTestServer(t)

	env.wantTokenError(t, refreshGrant("mmcp_rt_unknown"), http.StatusBadRequest, "invalid_grant")
}

func TestToken_RefreshAfterSessionCap(t *testing.T) {
	env := newTestServer(t)
	first := env.wantTokens(t, codeGrant(env.loginToCode(t)))
	env.clock.advance(721 * time.Hour)

	env.wantTokenError(t, refreshGrant(first.RefreshToken), http.StatusBadRequest, "invalid_grant")
}

func TestToken_RefreshAfterMealieExpiry(t *testing.T) {
	env := newTestServer(t)
	first := env.wantTokens(t, codeGrant(env.loginToCode(t)))
	env.clock.advance(49 * time.Hour)

	env.wantTokenError(t, refreshGrant(first.RefreshToken), http.StatusBadRequest, "invalid_grant")
}

func TestToken_RefreshAfterRevocation(t *testing.T) {
	env := newTestServer(t)
	first := env.wantTokens(t, codeGrant(env.loginToCode(t)))
	if err := env.store.RevokeSession(t.Context(), env.onlySession(t).ID, env.clock.Now()); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	env.wantTokenError(t, refreshGrant(first.RefreshToken), http.StatusBadRequest, "invalid_grant")
}

func TestToken_UnsupportedGrant(t *testing.T) {
	env := newTestServer(t)

	env.wantTokenError(t, url.Values{"grant_type": {"password"}, "client_id": {testClientID}}, http.StatusBadRequest, "unsupported_grant_type")
}

func TestToken_OversizedBody(t *testing.T) {
	env := newTestServer(t)
	form := refreshGrant("mmcp_rt_x")
	form.Set("padding", strings.Repeat("a", 8<<10))

	env.wantTokenError(t, form, http.StatusBadRequest, "invalid_request")
}

func TestToken_NoSecretsInLogsOrErrors(t *testing.T) {
	env := newTestServer(t)
	code := env.loginToCode(t)
	first := env.wantTokens(t, codeGrant(code))
	second := env.wantTokens(t, refreshGrant(first.RefreshToken))
	_, reuseBody := env.postToken(t, codeGrant(code))

	for _, s := range []string{code, testClientVerifier, first.AccessToken, first.RefreshToken, second.AccessToken, second.RefreshToken} {
		if strings.Contains(env.logs.String(), s) {
			t.Fatalf("log contains secret %q", s)
		}
		if strings.Contains(reuseBody, s) {
			t.Fatalf("error body contains secret %q", s)
		}
	}
}

const storeFailureDetail = "disk on fire"

type failingStore struct {
	Store
	failing string
	err     error
}

func (f *failingStore) fail(method string) error {
	if f.failing == method {
		return f.err
	}
	return nil
}

func (f *failingStore) ConsumeAuthCode(ctx context.Context, hash []byte, now time.Time) (store.AuthCode, error) {
	if err := f.fail("ConsumeAuthCode"); err != nil {
		return store.AuthCode{}, err
	}
	return f.Store.ConsumeAuthCode(ctx, hash, now)
}

func (f *failingStore) IssueTokens(ctx context.Context, access store.AccessToken, refresh store.RefreshToken) error {
	if err := f.fail("IssueTokens"); err != nil {
		return err
	}
	return f.Store.IssueTokens(ctx, access, refresh)
}

func (f *failingStore) GetAccessToken(ctx context.Context, hash []byte) (store.AccessToken, error) {
	if err := f.fail("GetAccessToken"); err != nil {
		return store.AccessToken{}, err
	}
	return f.Store.GetAccessToken(ctx, hash)
}

func (f *failingStore) GetRefreshToken(ctx context.Context, hash []byte) (store.RefreshToken, error) {
	if err := f.fail("GetRefreshToken"); err != nil {
		return store.RefreshToken{}, err
	}
	return f.Store.GetRefreshToken(ctx, hash)
}

func (f *failingStore) RotateRefreshToken(ctx context.Context, oldHash []byte, now time.Time, next store.AccessToken, nextRefresh store.RefreshToken) (string, error) {
	if err := f.fail("RotateRefreshToken"); err != nil {
		return "", err
	}
	return f.Store.RotateRefreshToken(ctx, oldHash, now, next, nextRefresh)
}

func (f *failingStore) GetSession(ctx context.Context, id string) (store.Session, error) {
	if err := f.fail("GetSession"); err != nil {
		return store.Session{}, err
	}
	return f.Store.GetSession(ctx, id)
}

func (f *failingStore) RevokeSession(ctx context.Context, id string, now time.Time) error {
	if err := f.fail("RevokeSession"); err != nil {
		return err
	}
	return f.Store.RevokeSession(ctx, id, now)
}

func (f *failingStore) UpsertClient(ctx context.Context, c store.Client) error {
	if err := f.fail("UpsertClient"); err != nil {
		return err
	}
	return f.Store.UpsertClient(ctx, c)
}

func (f *failingStore) GetClient(ctx context.Context, id string) (store.Client, error) {
	if err := f.fail("GetClient"); err != nil {
		return store.Client{}, err
	}
	return f.Store.GetClient(ctx, id)
}

func (f *failingStore) CreatePendingAuth(ctx context.Context, p store.PendingAuth) error {
	if err := f.fail("CreatePendingAuth"); err != nil {
		return err
	}
	return f.Store.CreatePendingAuth(ctx, p)
}

func (f *failingStore) SetPendingAuthLogin(ctx context.Context, id string, l store.Login) error {
	if err := f.fail("SetPendingAuthLogin"); err != nil {
		return err
	}
	return f.Store.SetPendingAuthLogin(ctx, id, l)
}

func (f *failingStore) DeletePendingAuth(ctx context.Context, id string) error {
	if err := f.fail("DeletePendingAuth"); err != nil {
		return err
	}
	return f.Store.DeletePendingAuth(ctx, id)
}

func (f *failingStore) HasConsent(ctx context.Context, userID, clientID string) (bool, error) {
	if err := f.fail("HasConsent"); err != nil {
		return false, err
	}
	return f.Store.HasConsent(ctx, userID, clientID)
}

func (f *failingStore) GrantConsent(ctx context.Context, userID, clientID string, now time.Time) error {
	if err := f.fail("GrantConsent"); err != nil {
		return err
	}
	return f.Store.GrantConsent(ctx, userID, clientID, now)
}

func (f *failingStore) CreateSessionWithCode(ctx context.Context, session store.Session, code store.AuthCode) error {
	if err := f.fail("CreateSessionWithCode"); err != nil {
		return err
	}
	return f.Store.CreateSessionWithCode(ctx, session, code)
}

func (e *testEnv) failStore(method string) {
	e.failStoreWith(method, errors.New(storeFailureDetail))
}

func (e *testEnv) failStoreWith(method string, err error) {
	e.recorder.Store = &failingStore{Store: e.store, failing: method, err: err}
}

func wantServerError(t *testing.T, resp reply, body string) {
	t.Helper()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", resp.StatusCode, body)
	}
	var got oauthError
	if err := json.Unmarshal([]byte(body), &got); err != nil || got.Error != "server_error" {
		t.Fatalf("body = %s, want a server_error JSON error", body)
	}
	if strings.Contains(body, storeFailureDetail) {
		t.Fatalf("error body leaks internal detail: %s", body)
	}
}

func TestToken_StoreFailure(t *testing.T) {
	freshCode := func(t *testing.T, env *testEnv) url.Values { return codeGrant(env.loginToCode(t)) }
	usedCode := func(t *testing.T, env *testEnv) url.Values {
		form := codeGrant(env.loginToCode(t))
		env.wantTokens(t, form)
		return form
	}
	freshRefresh := func(t *testing.T, env *testEnv) url.Values {
		return refreshGrant(env.wantTokens(t, codeGrant(env.loginToCode(t))).RefreshToken)
	}
	tests := []struct {
		failing string
		form    func(*testing.T, *testEnv) url.Values
	}{
		{"ConsumeAuthCode", freshCode},
		{"GetSession", freshCode},
		{"IssueTokens", freshCode},
		{"RevokeSession", usedCode},
		{"GetRefreshToken", freshRefresh},
		{"RotateRefreshToken", freshRefresh},
	}
	for _, tt := range tests {
		t.Run(tt.failing, func(t *testing.T) {
			env := newTestServer(t)
			form := tt.form(t, env)
			env.failStore(tt.failing)

			resp, body := env.postToken(t, form)

			wantServerError(t, resp, body)
		})
	}
}

func TestToken_GrantVanishesMidRequest(t *testing.T) {
	freshCode := func(t *testing.T, env *testEnv) url.Values { return codeGrant(env.loginToCode(t)) }
	freshRefresh := func(t *testing.T, env *testEnv) url.Values {
		return refreshGrant(env.wantTokens(t, codeGrant(env.loginToCode(t))).RefreshToken)
	}
	tests := []struct {
		name    string
		failing string
		form    func(*testing.T, *testEnv) url.Values
	}{
		{"refresh token gone before rotation", "RotateRefreshToken", freshRefresh},
		{"session gone during refresh", "GetSession", freshRefresh},
		{"session gone during code exchange", "GetSession", freshCode},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestServer(t)
			form := tt.form(t, env)
			env.failStoreWith(tt.failing, store.ErrNotFound)

			env.wantTokenError(t, form, http.StatusBadRequest, "invalid_grant")
			env.wantNotRevoked(t)
		})
	}
}
