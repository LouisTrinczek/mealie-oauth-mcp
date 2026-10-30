package oauth

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/audit"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/token"
)

const (
	maxTokenFormBytes = 4 << 10
	tokenTypeBearer   = "Bearer"
	reuseKindCode     = "code"
	reuseKindRefresh  = "refresh"
)

const (
	descCodeInvalid    = "The authorization code is invalid, expired, or already used."
	descCodeMismatch   = "The client_id or redirect_uri does not match the authorization request."
	descPKCEMismatch   = "The code_verifier does not match the code_challenge."
	descRefreshInvalid = "The refresh token is invalid, expired, or already used."
	descClientMismatch = "The refresh token was issued to another client."
	descSessionEnded   = "This login has ended. Start a new login."
)

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
}

type tokenPair struct {
	access     secret.Secret
	refresh    secret.Secret
	accessRow  store.AccessToken
	refreshRow store.RefreshToken
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if !parseTokenForm(w, r) {
		return
	}
	if r.PostForm.Get("client_id") == "" {
		writeJSONError(w, http.StatusUnauthorized, "invalid_client", "Send the client_id this app registered with.")
		return
	}
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.exchangeCode(w, r)
	case "refresh_token":
		s.refresh(w, r)
	default:
		writeJSONError(w, http.StatusBadRequest, "unsupported_grant_type", "Use authorization_code or refresh_token.")
	}
}

func parseTokenForm(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Pragma", "no-cache")
	r.Body = http.MaxBytesReader(w, r.Body, maxTokenFormBytes)
	if err := r.ParseForm(); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "The body must be a form of at most 4 KiB.")
		return false
	}
	return true
}

func (s *Server) exchangeCode(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	form := r.PostForm
	code, err := s.store.ConsumeAuthCode(ctx, token.Hash(form.Get("code")), s.now())
	if errors.Is(err, store.ErrAlreadyUsed) {
		s.rejectReuse(w, r, code.SessionID, code.ClientID, reuseKindCode, descCodeInvalid)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusBadRequest, "invalid_grant", descCodeInvalid)
		return
	}
	if err != nil {
		s.tokenServerError(w, r, "consuming authorization code", err)
		return
	}
	if form.Get("client_id") != code.ClientID || form.Get("redirect_uri") != code.RedirectURI {
		writeJSONError(w, http.StatusBadRequest, "invalid_grant", descCodeMismatch)
		return
	}
	if !VerifyPKCE(code.CodeChallenge, form.Get("code_verifier")) {
		writeJSONError(w, http.StatusBadRequest, "invalid_grant", descPKCEMismatch)
		return
	}
	if resource := form.Get("resource"); resource != "" && !resourceMatches(resource, s.cfg.Resource, code.Resource) {
		writeJSONError(w, http.StatusBadRequest, "invalid_target", "The resource does not identify this server.")
		return
	}
	session, ok := s.liveSession(w, r, code.SessionID)
	if !ok {
		return
	}
	pair := s.newTokenPair(session)
	if err := s.store.IssueTokens(ctx, pair.accessRow, pair.refreshRow); err != nil {
		s.tokenServerError(w, r, "issuing tokens", err)
		return
	}
	s.audit.Event(ctx, audit.TokenIssued, sessionAttrs(session)...)
	s.writeTokens(w, pair)
}

func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	form := r.PostForm
	oldHash := token.Hash(form.Get("refresh_token"))
	current, err := s.store.GetRefreshToken(ctx, oldHash)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.tokenServerError(w, r, "getting refresh token", err)
		return
	}
	if err != nil || !s.now().Before(current.ExpiresAt) {
		writeJSONError(w, http.StatusBadRequest, "invalid_grant", descRefreshInvalid)
		return
	}
	session, ok := s.liveSession(w, r, current.SessionID)
	if !ok {
		return
	}
	if form.Get("client_id") != session.ClientID {
		writeJSONError(w, http.StatusBadRequest, "invalid_grant", descClientMismatch)
		return
	}
	pair := s.newTokenPair(session)
	sessionID, err := s.store.RotateRefreshToken(ctx, oldHash, s.now(), pair.accessRow, pair.refreshRow)
	if errors.Is(err, store.ErrAlreadyUsed) {
		s.rejectReuse(w, r, sessionID, session.ClientID, reuseKindRefresh, descRefreshInvalid)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusBadRequest, "invalid_grant", descRefreshInvalid)
		return
	}
	if err != nil {
		s.tokenServerError(w, r, "rotating refresh token", err)
		return
	}
	s.audit.Event(ctx, audit.TokenRefreshed, sessionAttrs(session)...)
	s.writeTokens(w, pair)
}

func (s *Server) liveSession(w http.ResponseWriter, r *http.Request, id string) (store.Session, bool) {
	session, err := s.store.GetSession(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeJSONError(w, http.StatusBadRequest, "invalid_grant", descSessionEnded)
		return store.Session{}, false
	}
	if err != nil {
		s.tokenServerError(w, r, "getting session", err)
		return store.Session{}, false
	}
	if !sessionLive(session, s.now()) {
		writeJSONError(w, http.StatusBadRequest, "invalid_grant", descSessionEnded)
		return store.Session{}, false
	}
	return session, true
}

func sessionLive(session store.Session, now time.Time) bool {
	return session.RevokedAt.IsZero() && now.Before(session.ExpiresAt) && now.Before(session.MealieTokenExpiresAt)
}

func resourceMatches(requested, ours, bound string) bool {
	resolved, err := ResolveResource(requested, ours)
	return err == nil && resolved == bound
}

func (s *Server) rejectReuse(w http.ResponseWriter, r *http.Request, sessionID, clientID, kind, description string) {
	ctx := r.Context()
	if err := s.store.RevokeSession(ctx, sessionID, s.now()); err != nil {
		s.tokenServerError(w, r, "revoking session after reuse", err)
		return
	}
	s.audit.Event(ctx, audit.RefreshReuseDetected,
		slog.String("kind", kind), slog.String("client_id", clientID), slog.String("session_id", sessionID))
	writeJSONError(w, http.StatusBadRequest, "invalid_grant", description)
}

func (s *Server) newTokenPair(session store.Session) tokenPair {
	access, accessHash := token.New(token.Access)
	refresh, refreshHash := token.New(token.Refresh)
	return tokenPair{
		access:     access,
		refresh:    refresh,
		accessRow:  store.AccessToken{Hash: accessHash, SessionID: session.ID, ExpiresAt: s.now().Add(s.cfg.AccessTokenTTL)},
		refreshRow: store.RefreshToken{Hash: refreshHash, SessionID: session.ID, ExpiresAt: session.ExpiresAt},
	}
}

func (s *Server) writeTokens(w http.ResponseWriter, pair tokenPair) {
	writeJSON(w, http.StatusOK, tokenResponse{
		AccessToken:  pair.access.Reveal(),
		TokenType:    tokenTypeBearer,
		ExpiresIn:    int64(s.cfg.AccessTokenTTL / time.Second),
		RefreshToken: pair.refresh.Reveal(),
		Scope:        scopeMealie,
	})
}

func (s *Server) tokenServerError(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.logger.ErrorContext(r.Context(), what, slog.String("error", err.Error()))
	writeJSONError(w, http.StatusInternalServerError, "server_error", "The server could not complete the request. Try again.")
}

func sessionAttrs(session store.Session) []slog.Attr {
	return []slog.Attr{
		slog.String("client_id", session.ClientID),
		slog.String("mealie_user_id", session.MealieUserID),
		slog.String("session_id", session.ID),
	}
}
