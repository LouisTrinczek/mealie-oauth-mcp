package oauth

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func (e *testEnv) register(t *testing.T, body string) (reply, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.http.URL+"/register", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return send(t, e.client, req)
}

func registrationJSON(t *testing.T, fields map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func TestRegister_Valid(t *testing.T) {
	env := newTestServer(t)

	resp, body := env.register(t, registrationJSON(t, map[string]any{
		"redirect_uris":              []string{"http://127.0.0.1:5555/cb", "https://app.example.com/cb"},
		"client_name":                "My App",
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"token_endpoint_auth_method": "none",
		"logo_uri":                   "https://app.example.com/logo.png",
	}))

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", resp.StatusCode, body)
	}
	doc := decodeJSON(t, body)
	clientID, _ := doc["client_id"].(string)
	if clientID == "" {
		t.Fatalf("client_id missing: %s", body)
	}
	if _, ok := doc["client_secret"]; ok {
		t.Fatal("response carries a client_secret")
	}
	wantFields(t, doc, map[string]any{
		"client_id_issued_at":        float64(env.clock.Now().Unix()),
		"client_name":                "My App",
		"redirect_uris":              []any{"http://127.0.0.1:5555/cb", "https://app.example.com/cb"},
		"grant_types":                []any{"authorization_code", "refresh_token"},
		"token_endpoint_auth_method": "none",
	})
	stored, err := env.store.GetClient(t.Context(), clientID)
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if stored.Source != "dcr" || stored.Name != "My App" || len(stored.RedirectURIs) != 2 {
		t.Fatalf("stored client = %+v", stored)
	}
	if !strings.Contains(env.logs.String(), `"event":"client_registered"`) {
		t.Fatal("no client_registered audit event")
	}
}

func TestRegister_DefaultsGrantTypes(t *testing.T) {
	env := newTestServer(t)

	resp, body := env.register(t, `{"redirect_uris":["http://localhost/cb"]}`)

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", resp.StatusCode, body)
	}
	wantFields(t, decodeJSON(t, body), map[string]any{
		"grant_types":                []any{"authorization_code", "refresh_token"},
		"token_endpoint_auth_method": "none",
	})
}

func TestRegister_SanitizesClientName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"control characters", "My\x07 App\n\t", "My App"},
		{"bidi override", "Safe\u202eppA", "SafeppA"},
		{"long name", strings.Repeat("é", 150), strings.Repeat("é", 100)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestServer(t)

			resp, body := env.register(t, registrationJSON(t, map[string]any{
				"redirect_uris": []string{"http://127.0.0.1/cb"},
				"client_name":   tt.in,
			}))

			if resp.StatusCode != http.StatusCreated {
				t.Fatalf("status = %d; body: %s", resp.StatusCode, body)
			}
			if got := decodeJSON(t, body)["client_name"]; got != tt.want {
				t.Fatalf("client_name = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRegister_Rejects(t *testing.T) {
	elevenURIs := make([]string, 11)
	for i := range elevenURIs {
		elevenURIs[i] = fmt.Sprintf("http://127.0.0.1/cb%d", i)
	}
	tests := []struct {
		name string
		body string
		code string
	}{
		{
			name: "confidential auth method",
			body: `{"redirect_uris":["http://127.0.0.1/cb"],"token_endpoint_auth_method":"client_secret_basic"}`,
			code: "invalid_client_metadata",
		},
		{
			name: "non-loopback http redirect",
			body: `{"redirect_uris":["http://evil.com/cb"]}`,
			code: "invalid_redirect_uri",
		},
		{
			name: "no redirect URIs",
			body: `{"redirect_uris":[]}`,
			code: "invalid_redirect_uri",
		},
		{
			name: "eleven redirect URIs",
			body: `{"redirect_uris":["` + strings.Join(elevenURIs, `","`) + `"]}`,
			code: "invalid_redirect_uri",
		},
		{
			name: "over-long redirect URI",
			body: registrationJSON(t, map[string]any{"redirect_uris": []string{uriOfLength("https://app.example/", maxRedirectURIBytes+1)}}),
			code: "invalid_redirect_uri",
		},
		{
			name: "17 KiB body",
			body: `{"redirect_uris":["http://127.0.0.1/cb"],"client_name":"` + strings.Repeat("a", 17<<10) + `"}`,
			code: "invalid_client_metadata",
		},
		{
			name: "client_credentials grant",
			body: `{"redirect_uris":["http://127.0.0.1/cb"],"grant_types":["client_credentials"]}`,
			code: "invalid_client_metadata",
		},
		{
			name: "malformed JSON",
			body: `{"redirect_uris":`,
			code: "invalid_client_metadata",
		},
	}
	env := newTestServer(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := env.register(t, tt.body)

			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
			}
			doc := decodeJSON(t, body)
			if doc["error"] != tt.code {
				t.Fatalf("error = %v, want %s", doc["error"], tt.code)
			}
			if desc, _ := doc["error_description"].(string); desc == "" {
				t.Fatal("error_description is empty")
			}
		})
	}
}

func TestRegister_StoreFailure(t *testing.T) {
	env := newTestServer(t)
	_ = env.store.Close()

	resp, body := env.register(t, `{"redirect_uris":["http://127.0.0.1/cb"]}`)

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", resp.StatusCode)
	}
	if doc := decodeJSON(t, body); doc["error"] != "server_error" || strings.Contains(body, "sql") {
		t.Fatalf("body = %s", body)
	}
}
