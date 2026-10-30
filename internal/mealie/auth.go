package mealie

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
)

const (
	aboutPath          = "/api/app/about"
	nativeConfigPath   = "/api/auth/oauth/native/config"
	nativeExchangePath = "/api/auth/oauth/native/token"
	refreshPath        = "/api/auth/refresh"
	selfPath           = "/api/users/self"
)

type About struct {
	Version    string
	EnableOIDC bool
}

type NativeConfig struct {
	AuthorizationEndpoint string
	ClientID              string
	Scope                 string
}

type NativeTokenRequest struct {
	Code         secret.Secret
	CodeVerifier string
	RedirectURI  string
	Nonce        string
}

type Token struct {
	AccessToken secret.Secret
	ExpiresIn   time.Duration
}

type User struct {
	ID            string
	Username      string
	FullName      string
	Email         string
	GroupSlug     string
	HouseholdSlug string
	Admin         bool
}

type aboutWire struct {
	Version    string `json:"version"`
	EnableOIDC bool   `json:"enableOidc"`
}

type nativeConfigWire struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	ClientID              string `json:"client_id"`
	Scope                 string `json:"scope"`
}

type nativeTokenRequestWire struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	RedirectURI  string `json:"redirect_uri"`
	Nonce        string `json:"nonce"`
}

type tokenWire struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

type userWire struct {
	ID            string `json:"id"`
	Username      string `json:"username"`
	FullName      string `json:"fullName"`
	Email         string `json:"email"`
	GroupSlug     string `json:"groupSlug"`
	HouseholdSlug string `json:"householdSlug"`
	Admin         bool   `json:"admin"`
}

func (c *Client) About(ctx context.Context) (About, error) {
	var wire aboutWire
	if err := c.do(ctx, secret.Secret{}, http.MethodGet, aboutPath, nil, nil, &wire); err != nil {
		return About{}, fmt.Errorf("fetching mealie about: %w", err)
	}
	return About(wire), nil
}

func (c *Client) NativeConfig(ctx context.Context) (NativeConfig, error) {
	var wire nativeConfigWire
	if err := c.do(ctx, secret.Secret{}, http.MethodGet, nativeConfigPath, nil, nil, &wire); err != nil {
		return NativeConfig{}, fmt.Errorf("fetching mealie native config: %w", err)
	}
	return NativeConfig(wire), nil
}

func (c *Client) NativeToken(ctx context.Context, req NativeTokenRequest) (Token, error) {
	body := nativeTokenRequestWire{
		Code:         req.Code.Reveal(),
		CodeVerifier: req.CodeVerifier,
		RedirectURI:  req.RedirectURI,
		Nonce:        req.Nonce,
	}
	var wire tokenWire
	if err := c.do(ctx, secret.Secret{}, http.MethodPost, nativeExchangePath, nil, body, &wire); err != nil {
		return Token{}, fmt.Errorf("exchanging mealie native code: %w", err)
	}
	return wire.token(), nil
}

func (c *Client) RefreshToken(ctx context.Context, current secret.Secret) (Token, error) {
	var wire tokenWire
	if err := c.do(ctx, current, http.MethodPost, refreshPath, nil, nil, &wire); err != nil {
		return Token{}, fmt.Errorf("refreshing mealie token: %w", err)
	}
	return wire.token(), nil
}

func (c *Client) Self(ctx context.Context, tok secret.Secret) (User, error) {
	var wire userWire
	if err := c.do(ctx, tok, http.MethodGet, selfPath, nil, nil, &wire); err != nil {
		return User{}, fmt.Errorf("fetching mealie user: %w", err)
	}
	return User(wire), nil
}

func (w tokenWire) token() Token {
	return Token{
		AccessToken: secret.FromString(w.AccessToken),
		ExpiresIn:   time.Duration(w.ExpiresIn) * time.Second,
	}
}
