package oauth

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

func decodeJSON(t *testing.T, body string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("decoding JSON %q: %v", body, err)
	}
	return doc
}

func wantFields(t *testing.T, doc map[string]any, want map[string]any) {
	t.Helper()
	for key, value := range want {
		if !reflect.DeepEqual(doc[key], value) {
			t.Errorf("%s = %#v, want %#v", key, doc[key], value)
		}
	}
}

func TestMetadata_AuthorizationServer(t *testing.T) {
	env := newTestServer(t)
	issuer := env.http.URL

	resp, body := get(t, env.client, issuer+"/.well-known/oauth-authorization-server")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	wantFields(t, decodeJSON(t, body), map[string]any{
		"issuer":                                         issuer,
		"authorization_endpoint":                         issuer + "/authorize",
		"token_endpoint":                                 issuer + "/token",
		"registration_endpoint":                          issuer + "/register",
		"revocation_endpoint":                            issuer + "/revoke",
		"response_types_supported":                       []any{"code"},
		"grant_types_supported":                          []any{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":               []any{"S256"},
		"token_endpoint_auth_methods_supported":          []any{"none"},
		"client_id_metadata_document_supported":          true,
		"scopes_supported":                               []any{"mealie"},
		"authorization_response_iss_parameter_supported": true,
	})
}

func TestMetadata_ProtectedResource_BothPaths(t *testing.T) {
	env := newTestServer(t)
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		t.Run(path, func(t *testing.T) {
			resp, body := get(t, env.client, env.http.URL+path)

			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			wantFields(t, decodeJSON(t, body), map[string]any{
				"resource":                 env.http.URL + "/mcp",
				"authorization_servers":    []any{env.http.URL},
				"bearer_methods_supported": []any{"header"},
				"scopes_supported":         []any{"mealie"},
				"resource_name":            "Mealie",
			})
		})
	}
}
