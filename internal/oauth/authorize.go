package oauth

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/audit"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/token"
)

const (
	pendingAuthLifetime = 10 * time.Minute
	pkceMethodS256      = "S256"
	bindingCookieName   = "mmcp_auth"
	hostCookiePrefix    = "__Host-"
)

var errUnknownClient = errors.New("unknown client")

type upstreamLeg struct {
	pendingID string
	state     string
	verifier  string
	nonce     string
	binding   string
}

func newUpstreamLeg() upstreamLeg {
	return upstreamLeg{
		pendingID: token.RandomID(),
		state:     token.RandomID(),
		verifier:  token.RandomVerifier(),
		nonce:     token.RandomID(),
		binding:   token.RandomVerifier(),
	}
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	client, err := s.resolveClient(ctx, q.Get("client_id"))
	if errors.Is(err, errUnknownClient) {
		s.renderError(w, r, http.StatusBadRequest, msgInvalidClient)
		return
	}
	if err != nil {
		s.internalError(w, r, "resolving client", err)
		return
	}
	redirectURI := q.Get("redirect_uri")
	if !MatchRedirectURI(client.RedirectURIs, redirectURI) {
		s.renderError(w, r, http.StatusBadRequest, msgInvalidRedirect)
		return
	}
	state := q.Get("state")
	resource, code, description := s.checkAuthorizeParams(q)
	if code != "" {
		s.redirectError(w, r, redirectURI, state, code, description)
		return
	}
	leg := newUpstreamLeg()
	upstream, err := s.upstreamAuthorizeURL(ctx, leg)
	if err != nil {
		s.logger.LogAttrs(ctx, slog.LevelWarn, "fetching mealie native config failed", mealieStatus(err)...)
		s.renderError(w, r, http.StatusBadGateway, msgMealieUnreachable)
		return
	}
	now := s.now()
	pending := store.PendingAuth{
		ID:                 leg.pendingID,
		ClientID:           client.ID,
		RedirectURI:        redirectURI,
		ClientState:        state,
		CodeChallenge:      q.Get("code_challenge"),
		Resource:           resource,
		BrowserBindingHash: token.Hash(leg.binding),
		UpstreamStateHash:  token.Hash(leg.state),
		UpstreamVerifier:   leg.verifier,
		UpstreamNonce:      leg.nonce,
		CreatedAt:          now,
		ExpiresAt:          now.Add(pendingAuthLifetime),
	}
	if err := s.store.CreatePendingAuth(ctx, pending); err != nil {
		s.internalError(w, r, "creating pending auth", err)
		return
	}
	http.SetCookie(w, s.bindingCookie(leg.pendingID+"."+leg.binding, int(pendingAuthLifetime.Seconds())))
	s.audit.Event(ctx, audit.LoginStarted, slog.String("client_id", client.ID))
	http.Redirect(w, r, upstream, http.StatusFound)
}

func (s *Server) resolveClient(ctx context.Context, clientID string) (store.Client, error) {
	if !strings.HasPrefix(clientID, "https://") {
		client, err := s.store.GetClient(ctx, clientID)
		if errors.Is(err, store.ErrNotFound) {
			return store.Client{}, errUnknownClient
		}
		return client, err
	}
	metadata, err := s.cimd.Fetch(ctx, clientID)
	if err != nil {
		s.logger.WarnContext(ctx, "client metadata document rejected", slog.String("client_id", clientID), slog.String("error", err.Error()))
		return store.Client{}, errUnknownClient
	}
	now := s.now()
	client := store.Client{
		ID:                clientID,
		Source:            sourceCIMD,
		Name:              sanitizeClientName(metadata.ClientName),
		RedirectURIs:      metadata.RedirectURIs,
		CreatedAt:         now,
		MetadataFetchedAt: now,
	}
	if err := s.store.UpsertClient(ctx, client); err != nil {
		return store.Client{}, err
	}
	return client, nil
}

func (s *Server) checkAuthorizeParams(q url.Values) (resource, code, description string) {
	if q.Get("response_type") != "code" {
		return "", "unsupported_response_type", "Only response_type=code is supported."
	}
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != pkceMethodS256 {
		return "", "invalid_request", "PKCE is required: send code_challenge with code_challenge_method=S256."
	}
	resource, err := ResolveResource(q.Get("resource"), s.cfg.Resource)
	if err != nil {
		return "", "invalid_target", "The resource parameter must be " + s.cfg.Resource + " or left out."
	}
	return resource, "", ""
}

func (s *Server) upstreamAuthorizeURL(ctx context.Context, leg upstreamLeg) (string, error) {
	native, err := s.mealie.NativeConfig(ctx)
	if err != nil {
		return "", err
	}
	endpoint, err := url.Parse(native.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}
	q := endpoint.Query()
	q.Set("response_type", "code")
	q.Set("client_id", native.ClientID)
	q.Set("scope", native.Scope)
	q.Set("redirect_uri", s.cfg.CallbackURL)
	q.Set("state", leg.state)
	q.Set("nonce", leg.nonce)
	q.Set("code_challenge", S256(leg.verifier))
	q.Set("code_challenge_method", pkceMethodS256)
	endpoint.RawQuery = q.Encode()
	return endpoint.String(), nil
}

func (s *Server) bindingCookie(value string, maxAge int) *http.Cookie {
	name := bindingCookieName
	if s.cfg.SecureCookies {
		name = hostCookiePrefix + bindingCookieName
	}
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	}
}

func (s *Server) readBinding(r *http.Request) (pendingID, binding string, ok bool) {
	cookie, err := r.Cookie(s.bindingCookie("", 0).Name)
	if err != nil {
		return "", "", false
	}
	pendingID, binding, ok = strings.Cut(cookie.Value, ".")
	return pendingID, binding, ok && pendingID != "" && binding != ""
}

func bindingMatches(binding string, pending store.PendingAuth) bool {
	return subtle.ConstantTimeCompare(token.Hash(binding), pending.BrowserBindingHash) == 1
}

func mealieStatus(err error) []slog.Attr {
	var apiErr *mealie.APIError
	if errors.As(err, &apiErr) {
		return []slog.Attr{slog.Int("mealie_status", apiErr.Status)}
	}
	return nil
}
