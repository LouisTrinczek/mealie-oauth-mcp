package mealie

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
)

func TestNativeToken_SendsExactBody(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/auth/oauth/native/token" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if _, ok := r.Header["Authorization"]; ok {
			t.Error("Authorization header sent")
		}
		raw, _ := io.ReadAll(r.Body)
		var got map[string]string
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("body not a JSON string map: %s", raw)
		}
		want := map[string]string{
			"code":          "the-code",
			"code_verifier": "the-verifier",
			"redirect_uri":  "https://mcp.example/cb",
			"nonce":         "the-nonce",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("body = %v, want %v", got, want)
		}
		respond(w, `{"access_token":"`+testJWT+`","token_type":"bearer","expires_in":172800}`)
	}))

	tok, err := c.NativeToken(context.Background(), NativeTokenRequest{
		Code:         secret.FromString("the-code"),
		CodeVerifier: "the-verifier",
		RedirectURI:  "https://mcp.example/cb",
		Nonce:        "the-nonce",
	})
	if err != nil {
		t.Fatalf("NativeToken: %v", err)
	}
	if tok.AccessToken.Reveal() != testJWT {
		t.Error("access token not decoded")
	}
	if tok.ExpiresIn != 48*time.Hour {
		t.Errorf("ExpiresIn = %v, want 48h", tok.ExpiresIn)
	}
}

func TestNativeToken_ErrorDoesNotLeakInputs(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		respond(w, `{"detail":"invalid grant"}`)
	}))
	_, err := c.NativeToken(context.Background(), NativeTokenRequest{
		Code:         secret.FromString("the-code"),
		CodeVerifier: "the-verifier",
	})
	if err == nil {
		t.Fatal("NativeToken returned no error")
	}
	for _, leaked := range []string{"the-code", "the-verifier"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("error leaks %q: %v", leaked, err)
		}
	}
}

func TestRefreshToken_SendsBearer(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/auth/refresh" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer current-token" {
			t.Errorf("Authorization = %q", got)
		}
		respond(w, `{"access_token":"fresh-token","token_type":"bearer","expires_in":3600}`)
	}))

	tok, err := c.RefreshToken(context.Background(), secret.FromString("current-token"))
	if err != nil {
		t.Fatalf("RefreshToken: %v", err)
	}
	if tok.AccessToken.Reveal() != "fresh-token" || tok.ExpiresIn != time.Hour {
		t.Errorf("token = %v / %v", tok.AccessToken.Reveal(), tok.ExpiresIn)
	}
}

func TestSelf_DecodesUserOutLeniently(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/users/self" || r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("request = %s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		respond(w, `{"id":"u1","username":"louis","fullName":"Louis T","email":"l@example.com","admin":true,
			"groupSlug":"home","householdSlug":"family","brandNewField":{"nested":[1,2,3]},"authMethod":"OIDC"}`)
	}))

	user, err := c.Self(context.Background(), secret.FromString("tok"))
	if err != nil {
		t.Fatalf("Self: %v", err)
	}
	want := User{ID: "u1", Username: "louis", FullName: "Louis T", Email: "l@example.com", GroupSlug: "home", HouseholdSlug: "family", Admin: true}
	if user != want {
		t.Errorf("user = %+v, want %+v", user, want)
	}
}

func TestSelf_Unauthorized(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		respond(w, `{"detail":"Unauthorized"}`)
	}))
	_, err := c.Self(context.Background(), secret.FromString("tok"))
	if !IsUnauthorized(err) {
		t.Errorf("err = %v, want unauthorized", err)
	}
}

func TestNativeConfig(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/oauth/native/config" {
			t.Errorf("path = %q", r.URL.Path)
		}
		respond(w, `{"authorization_endpoint":"https://idp/auth","client_id":"cid","scope":"openid email","extra":1}`)
	}))
	cfg, err := c.NativeConfig(context.Background())
	if err != nil {
		t.Fatalf("NativeConfig: %v", err)
	}
	want := NativeConfig{AuthorizationEndpoint: "https://idp/auth", ClientID: "cid", Scope: "openid email"}
	if cfg != want {
		t.Errorf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestAbout(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/app/about" {
			t.Errorf("path = %q", r.URL.Path)
		}
		respond(w, `{"version":"v3.23.0","enableOidc":true,"production":true}`)
	}))
	about, err := c.About(context.Background())
	if err != nil {
		t.Fatalf("About: %v", err)
	}
	if about != (About{Version: "v3.23.0", EnableOIDC: true}) {
		t.Errorf("about = %+v", about)
	}
}
