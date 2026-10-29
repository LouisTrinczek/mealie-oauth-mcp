package oauth

import (
	"bytes"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

const (
	msgExpired           = "This login link expired or was already used. Start again from your AI app."
	msgMealieRefused     = "Mealie refused this login. Ask the Mealie admin to check the OIDC settings (signup, user claim)."
	msgMealieUnreachable = "Mealie is unreachable. Try again in a moment."
	msgMealieFailed      = "Mealie could not complete the login. Try again in a moment."
	msgInvalidClient     = "This app could not be identified (invalid_client). Start again from your AI app."
	msgInvalidRedirect   = "This app asked to return to an address it did not register (invalid_request)."
	msgWrongBrowser      = "This login was started in a different browser. Start again from your AI app in this browser."
	msgInvalidConsent    = "This consent form is not valid any more. Start again from your AI app."
	msgInternal          = "Something went wrong on this server. Start again from your AI app."
)

type errorView struct {
	Message string
}

type oauthError struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

func contentSecurityPolicy(formActionSources ...string) string {
	formAction := strings.Join(append([]string{"'self'"}, formActionSources...), " ")
	return "default-src 'none'; style-src 'unsafe-inline'; form-action " + formAction + "; frame-ancestors 'none'"
}

func (s *Server) renderHTML(w http.ResponseWriter, r *http.Request, page *template.Template, status int, policy string, data any) {
	var body bytes.Buffer
	if err := page.Execute(&body, data); err != nil {
		s.logger.ErrorContext(r.Context(), "rendering page", slog.String("error", err.Error()))
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Content-Security-Policy", policy)
	header.Set("X-Frame-Options", "DENY")
	w.WriteHeader(status)
	_, _ = w.Write(body.Bytes())
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, message string) {
	s.renderHTML(w, r, s.errorPage, status, contentSecurityPolicy(), errorView{Message: message})
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.logger.ErrorContext(r.Context(), what, slog.String("error", err.Error()))
	s.renderError(w, r, http.StatusInternalServerError, msgInternal)
}

func (s *Server) redirectToClient(w http.ResponseWriter, r *http.Request, redirectURI, state string, params url.Values) {
	if state != "" {
		params.Set("state", state)
	}
	params.Set("iss", s.cfg.Issuer)
	separator := "?"
	if strings.Contains(redirectURI, "?") {
		separator = "&"
	}
	http.Redirect(w, r, redirectURI+separator+params.Encode(), http.StatusFound)
}

func (s *Server) redirectError(w http.ResponseWriter, r *http.Request, redirectURI, state, code, description string) {
	s.redirectToClient(w, r, redirectURI, state, url.Values{"error": {code}, "error_description": {description}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, oauthError{Error: code, Description: description})
}
