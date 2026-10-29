package oauth

import (
	"net/http"
	"net/url"
	"testing"
)

func (e *testEnv) postRevoke(t *testing.T, form url.Values) {
	t.Helper()
	resp, body := postForm(t, e.client, e.http.URL+"/revoke", form)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", got)
	}
}

func TestRevoke_UnknownTokenStill200(t *testing.T) {
	env := newTestServer(t)

	for _, tok := range []string{"mmcp_at_unknown", "mmcp_rt_unknown", "not-a-token", ""} {
		env.postRevoke(t, url.Values{"token": {tok}})
	}
}

func TestRevoke_RevokesSession(t *testing.T) {
	tests := []struct {
		name  string
		token func(tokenResponse) string
		hint  string
	}{
		{"access token", func(r tokenResponse) string { return r.AccessToken }, "access_token"},
		{"refresh token", func(r tokenResponse) string { return r.RefreshToken }, "refresh_token"},
		{"refresh token with wrong hint", func(r tokenResponse) string { return r.RefreshToken }, "access_token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestServer(t)
			tokens := env.wantTokens(t, codeGrant(env.loginToCode(t)))

			env.postRevoke(t, url.Values{"token": {tt.token(tokens)}, "token_type_hint": {tt.hint}})

			env.wantRevoked(t)
			env.wantAccessTokenGone(t, tokens.AccessToken)
			env.wantTokenError(t, refreshGrant(tokens.RefreshToken), http.StatusBadRequest, "invalid_grant")
			env.wantAudit(t, "session_revoked")
		})
	}
}

func TestRevoke_StoreFailure(t *testing.T) {
	for _, failing := range []string{"GetAccessToken", "RevokeSession"} {
		t.Run(failing, func(t *testing.T) {
			env := newTestServer(t)
			tokens := env.wantTokens(t, codeGrant(env.loginToCode(t)))
			env.failStore(failing)

			resp, body := postForm(t, env.client, env.http.URL+"/revoke", url.Values{"token": {tokens.AccessToken}})

			wantServerError(t, resp, body)
		})
	}
}
