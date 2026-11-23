package oauth

import (
	"errors"
	"net/http"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
)

const (
	passwordPostPath       = "/login/password"
	maxPasswordFormBytes   = 16 << 10
	msgInvalidPasswordForm = "This sign-in form is invalid. Start again from your AI app."
	msgPasswordRefused     = "Mealie refused the sign-in. Start again from your AI app and check your username and password."
	msgPasswordFailed      = "Mealie could not complete the sign-in. Start again from your AI app."
)

type passwordView struct {
	PendingID string
	CSRF      string
	Action    string
	Upstream  string
}

func (s *Server) requireLoginTLS(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.TLS == nil {
		s.renderError(w, r, http.StatusBadRequest, "Sign-in requires HTTPS.")
		return false
	}
	return true
}

func (s *Server) showPasswordForm(w http.ResponseWriter, r *http.Request, leg upstreamLeg, upstream string) {
	s.renderHTML(w, r, passwordTemplate, http.StatusOK, contentSecurityPolicy(), passwordView{
		PendingID: leg.pendingID,
		CSRF:      csrfToken(leg.binding),
		Action:    passwordPostPath,
		Upstream:  upstream,
	})
}

func (s *Server) passwordLogin(w http.ResponseWriter, r *http.Request) {
	if !s.requireLoginTLS(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPasswordFormBytes)
	if err := r.ParseForm(); err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			s.renderError(w, r, http.StatusRequestEntityTooLarge, msgInvalidPasswordForm)
			return
		}
		s.renderError(w, r, http.StatusBadRequest, msgInvalidPasswordForm)
		return
	}
	pendingID, binding, ok := s.readBinding(r)
	username, password := r.PostForm.Get("username"), r.PostForm.Get("password")
	if !ok || r.PostForm.Get("pending_id") != pendingID || !validCSRF(r.PostForm.Get("csrf"), binding) || username == "" || password == "" {
		s.renderError(w, r, http.StatusBadRequest, msgInvalidPasswordForm)
		return
	}
	ctx := r.Context()
	pending, err := s.store.GetPendingAuth(ctx, pendingID)
	if errors.Is(err, store.ErrNotFound) {
		s.renderError(w, r, http.StatusBadRequest, msgExpired)
		return
	}
	if err != nil {
		s.internalError(w, r, "getting pending auth", err)
		return
	}
	if !bindingMatches(binding, pending) {
		s.renderError(w, r, http.StatusBadRequest, msgWrongBrowser)
		return
	}
	if !s.claimPending(w, r, pendingID) {
		return
	}
	tok, err := s.mealie.PasswordToken(ctx, username, secret.FromString(password))
	user, err := s.resolveMealieLogin(ctx, tok, err)
	if err != nil {
		s.failMealieLogin(w, r, pending, err, msgPasswordRefused, msgPasswordFailed)
		return
	}
	s.completeLogin(w, r, pending, binding, tok, user)
}
