package mealie

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
)

const maxPasswordResponseBytes = 64 << 10

func newPasswordHTTPClient(client *http.Client) *http.Client {
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if transport, ok := client.Transport.(*http.Transport); ok && transport.TLSClientConfig != nil {
		tlsConfig.RootCAs = transport.TLSClientConfig.RootCAs
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy:             http.ProxyFromEnvironment,
			TLSClientConfig:   tlsConfig,
			ForceAttemptHTTP2: true,
		},
		Timeout:       defaultTimeout,
		CheckRedirect: refuseRedirect,
	}
}

func (c *Client) PasswordToken(ctx context.Context, username string, password secret.Secret) (Token, error) {
	target := c.baseURL.JoinPath("/api/auth/token")
	if target.Scheme != "https" || target.Host == "" || target.User != nil {
		return Token{}, errors.New("mealie password sign-in requires verified HTTPS")
	}
	target.RawQuery, target.Fragment, target.RawFragment, target.ForceQuery = "", "", "", false
	form := url.Values{"username": {username}, "password": {password.Reveal()}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, errors.New("building mealie password sign-in request")
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.passwordHTTP.Do(req)
	if err != nil {
		return Token{}, ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Token{}, &APIError{Status: resp.StatusCode, Message: "mealie password sign-in failed"}
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPasswordResponseBytes+1))
	var wire tokenWire
	if err != nil || len(raw) > maxPasswordResponseBytes || json.Unmarshal(raw, &wire) != nil {
		return Token{}, errors.New("invalid mealie password sign-in response")
	}
	return wire.token(), nil
}
