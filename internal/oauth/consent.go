package oauth

import (
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/audit"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/token"
)

const (
	authCodeLifetime    = 60 * time.Second
	maxConsentFormBytes = 4 << 10
	unnamedClient       = "Unnamed app"
)

type consentView struct {
	ClientName   string
	Verified     bool
	RedirectHost string
	Username     string
	PendingID    string
	CSRF         string
	Action       string
}

func (s *Server) showConsent(w http.ResponseWriter, r *http.Request, pending store.PendingAuth, binding string) {
	client, err := s.store.GetClient(r.Context(), pending.ClientID)
	if err != nil {
		s.internalError(w, r, "getting client for consent", err)
		return
	}
	s.renderHTML(w, r, s.consent, http.StatusOK, consentPolicy(pending.RedirectURI), consentView{
		ClientName:   clientDisplayName(client),
		Verified:     client.Source == sourceCIMD,
		RedirectHost: displayHost(pending.RedirectURI),
		Username:     pending.MealieUsername,
		PendingID:    pending.ID,
		CSRF:         csrfToken(binding),
		Action:       s.cfg.Issuer + "/consent",
	})
}

func (s *Server) decideConsent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	r.Body = http.MaxBytesReader(w, r.Body, maxConsentFormBytes)
	pendingID, binding, ok := s.readBinding(r)
	if !ok || r.PostFormValue("pending_id") != pendingID || !validCSRF(r.PostFormValue("csrf"), binding) {
		s.renderError(w, r, http.StatusBadRequest, msgInvalidConsent)
		return
	}
	pending, err := s.store.GetPendingAuth(ctx, pendingID)
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, r, http.StatusBadRequest, msgExpired)
		return
	}
	if err != nil {
		s.internalError(w, r, "getting pending auth", err)
		return
	}
	if !s.now().Before(pending.ExpiresAt) {
		s.renderError(w, r, http.StatusBadRequest, msgExpired)
		return
	}
	if !bindingMatches(binding, pending) || pending.MealieUserID == "" {
		s.renderError(w, r, http.StatusBadRequest, msgInvalidConsent)
		return
	}
	switch r.PostFormValue("decision") {
	case "allow":
		s.allow(w, r, pending)
	case "deny":
		s.deny(w, r, pending)
	default:
		s.renderError(w, r, http.StatusBadRequest, msgInvalidConsent)
	}
}

func (s *Server) allow(w http.ResponseWriter, r *http.Request, pending store.PendingAuth) {
	if err := s.store.GrantConsent(r.Context(), pending.MealieUserID, pending.ClientID, s.now()); err != nil {
		s.internalError(w, r, "granting consent", err)
		return
	}
	s.audit.Event(r.Context(), audit.ConsentGranted, consentAttrs(pending)...)
	s.finish(w, r, pending)
}

func (s *Server) deny(w http.ResponseWriter, r *http.Request, pending store.PendingAuth) {
	if err := s.store.DeletePendingAuth(r.Context(), pending.ID); err != nil {
		s.internalError(w, r, "deleting pending auth", err)
		return
	}
	s.audit.Event(r.Context(), audit.ConsentDenied, consentAttrs(pending)...)
	http.SetCookie(w, s.bindingCookie("", -1))
	s.redirectError(w, r, pending.RedirectURI, pending.ClientState, "access_denied", "The user denied access.")
}

func (s *Server) finish(w http.ResponseWriter, r *http.Request, pending store.PendingAuth) {
	ctx := r.Context()
	now := s.now()
	sessionID := token.RandomID()
	sealed, err := s.resealForSession(pending, sessionID)
	if err != nil {
		s.internalError(w, r, "resealing mealie token", err)
		return
	}
	code, codeHash := token.New(token.Code)
	session := store.Session{
		ID:                   sessionID,
		ClientID:             pending.ClientID,
		MealieUserID:         pending.MealieUserID,
		MealieUsername:       pending.MealieUsername,
		MealieTokenCT:        sealed,
		MealieTokenExpiresAt: pending.MealieTokenExpiresAt,
		MealieTokenLifetime:  pending.MealieTokenLifetime,
		Resource:             pending.Resource,
		CreatedAt:            now,
		ExpiresAt:            now.Add(s.cfg.SessionMaxAge),
		LastUsedAt:           now,
	}
	authCode := store.AuthCode{
		Hash:          codeHash,
		SessionID:     sessionID,
		ClientID:      pending.ClientID,
		RedirectURI:   pending.RedirectURI,
		CodeChallenge: pending.CodeChallenge,
		Resource:      pending.Resource,
		ExpiresAt:     now.Add(authCodeLifetime),
	}
	if err := s.store.CreateSessionWithCode(ctx, session, authCode); err != nil {
		s.internalError(w, r, "creating session", err)
		return
	}
	if err := s.store.DeletePendingAuth(ctx, pending.ID); err != nil {
		s.logger.WarnContext(ctx, "deleting pending auth", slog.String("error", err.Error()))
	}
	http.SetCookie(w, s.bindingCookie("", -1))
	s.audit.Event(ctx, audit.LoginSucceeded, append(consentAttrs(pending), slog.String("session_id", sessionID))...)
	s.redirectToClient(w, r, pending.RedirectURI, pending.ClientState, url.Values{"code": {code.Reveal()}})
}

func (s *Server) resealForSession(pending store.PendingAuth, sessionID string) ([]byte, error) {
	plain, err := s.keyring.Open(pending.MealieTokenCT, pendingAAD(pending.ID))
	if err != nil {
		return nil, err
	}
	return s.keyring.Seal(plain, []byte("session:"+sessionID)), nil
}

func consentAttrs(pending store.PendingAuth) []slog.Attr {
	return []slog.Attr{slog.String("client_id", pending.ClientID), slog.String("mealie_user_id", pending.MealieUserID)}
}

func clientDisplayName(client store.Client) string {
	if client.Source == sourceCIMD {
		return displayHost(client.ID)
	}
	if client.Name == "" {
		return unnamedClient
	}
	return client.Name
}

func displayHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if host := u.Hostname(); host != "" {
		return host
	}
	return u.Scheme
}

func csrfToken(binding string) string {
	return hex.EncodeToString(token.Hash(binding))
}

func validCSRF(submitted, binding string) bool {
	return subtle.ConstantTimeCompare([]byte(submitted), []byte(csrfToken(binding))) == 1
}

func consentPolicy(redirectURI string) string {
	if source, ok := formActionSource(redirectURI); ok {
		return contentSecurityPolicy(source)
	}
	return contentSecurityPolicy()
}

func formActionSource(redirectURI string) (string, bool) {
	u, err := url.Parse(redirectURI)
	if err != nil || !onlyRunes(u.Scheme, isSchemeRune) {
		return "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return u.Scheme + ":", true
	}
	if !onlyRunes(u.Host, isHostRune) {
		return "", false
	}
	return u.Scheme + "://" + u.Host, true
}

func onlyRunes(s string, allowed func(rune) bool) bool {
	return s != "" && !strings.ContainsFunc(s, func(r rune) bool { return !allowed(r) })
}

func isSchemeRune(r rune) bool {
	return isASCIIAlphanumeric(r) || r == '+' || r == '-' || r == '.'
}

func isHostRune(r rune) bool {
	return isASCIIAlphanumeric(r) || r == '-' || r == '.' || r == ':' || r == '[' || r == ']'
}

func isASCIIAlphanumeric(r rune) bool {
	return 'a' <= r && r <= 'z' || 'A' <= r && r <= 'Z' || '0' <= r && r <= '9'
}
