package mealie

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
)

func TestPasswordTokenSendsForm(t *testing.T) {
	const username = "name+alias@example.org"
	credential := rand.Text() + "päss &=+ word"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/auth/token" ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "" ||
			r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Error("unexpected password token request")
		}
		if err := r.ParseForm(); err != nil || r.PostForm.Get("username") != username || r.PostForm.Get("password") != credential {
			t.Error("invalid credentials form encoding")
		}
		respond(w, `{"access_token":"session-token","token_type":"bearer","expires_in":172800,"extra":true}`)
	}))
	t.Cleanup(server.Close)
	token, err := passwordTestClient(t, server.URL+"?ignored=true", server.Client()).
		PasswordToken(t.Context(), username, secret.FromString(credential))
	if err != nil || token.AccessToken.Reveal() != "session-token" || token.ExpiresIn != 48*time.Hour {
		t.Fatalf("unexpected password token result: %v", err)
	}
}

func TestPasswordTokenDiscardsErrorsAndRedirects(t *testing.T) {
	const password = "test-secret-payload"
	for _, status := range []int{401, 422, 500, 307, 308} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/auth/token" {
					t.Error("password request followed redirect")
					return
				}
				w.Header().Set("Location", "/unexpected")
				w.WriteHeader(status)
				respond(w, `{"detail":"`+password+`"}`)
			}))
			t.Cleanup(server.Close)
			_, err := passwordTestClient(t, server.URL, server.Client()).
				PasswordToken(t.Context(), "name", secret.FromString(password))
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Status != status || strings.Contains(err.Error(), password) {
				t.Fatal("password error included unexpected diagnostics")
			}
		})
	}
}

func TestPasswordTokenRequiresVerifiedTLS(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("password request reached server without verified TLS")
	})
	plain := httptest.NewServer(handler)
	t.Cleanup(plain.Close)
	tlsServer := httptest.NewTLSServer(handler)
	t.Cleanup(tlsServer.Close)
	client := tlsServer.Client()
	wrongHost := strings.Replace(tlsServer.URL, "127.0.0.1", "localhost", 1)
	for _, tc := range []struct {
		name, target string
		client       *http.Client
	}{
		{"plaintext", plain.URL, nil},
		{"untrusted", tlsServer.URL, nil},
		{"wrong hostname", wrongHost, client},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := passwordTestClient(t, tc.target, tc.client).
				PasswordToken(t.Context(), "name", secret.FromString("test-password")); err == nil {
				t.Fatal("insecure password request succeeded")
			}
		})
	}
}

func TestPasswordTokenRejectsInvalidResponse(t *testing.T) {
	for name, body := range map[string]string{
		"malformed":   `{"access_token":`,
		"oversized":   `{"access_token":"token","expires_in":3600}` + strings.Repeat(" ", 1<<17),
		"interrupted": "",
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if name == "interrupted" {
					w.Header().Set("Content-Length", "10")
				}
				respond(w, body)
			}))
			t.Cleanup(server.Close)
			if _, err := passwordTestClient(t, server.URL, server.Client()).
				PasswordToken(context.Background(), "name", secret.FromString("test-password")); err == nil {
				t.Fatal("invalid password response succeeded")
			}
		})
	}
}

func passwordTestClient(t *testing.T, target string, client *http.Client) *Client {
	t.Helper()
	base, err := url.Parse(target)
	if err != nil {
		t.Fatal(err)
	}
	return New(base, Options{HTTPClient: client})
}

func TestPasswordTokenRejectsInvalidEndpoint(t *testing.T) {
	client := New(&url.URL{Scheme: "https", Host: "example.org:invalid"}, Options{})
	if _, err := client.PasswordToken(t.Context(), "name", secret.FromString("test-password")); err == nil {
		t.Fatal("invalid password endpoint succeeded")
	}
}
