package oauth

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"unicode"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/audit"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/token"
)

const (
	maxRegistrationBytes = 16 << 10
	maxRedirectURIs      = 10
	maxClientNameRunes   = 100
	authMethodNone       = "none"
	sourceDCR            = "dcr"
	sourceCIMD           = "cimd"
)

type registrationRequest struct {
	RedirectURIs            []string `json:"redirect_uris"`
	ClientName              string   `json:"client_name"`
	GrantTypes              []string `json:"grant_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

type registrationResponse struct {
	ClientID                string   `json:"client_id"`
	ClientIDIssuedAt        int64    `json:"client_id_issued_at"`
	ClientName              string   `json:"client_name,omitempty"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var req registrationRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRegistrationBytes)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_client_metadata", "The body must be a JSON object of at most 16 KiB.")
		return
	}
	if code, description := req.problem(); code != "" {
		writeJSONError(w, http.StatusBadRequest, code, description)
		return
	}
	now := s.now()
	client := store.Client{
		ID:           token.RandomID(),
		Source:       sourceDCR,
		Name:         sanitizeClientName(req.ClientName),
		RedirectURIs: req.RedirectURIs,
		CreatedAt:    now,
	}
	if err := s.store.UpsertClient(r.Context(), client); err != nil {
		s.logger.ErrorContext(r.Context(), "registering client", slog.String("error", err.Error()))
		writeJSONError(w, http.StatusInternalServerError, "server_error", "The client could not be saved. Try again.")
		return
	}
	s.audit.Event(r.Context(), audit.ClientRegistered, slog.String("client_id", client.ID), slog.String("source", sourceDCR))
	writeJSON(w, http.StatusCreated, registrationResponse{
		ClientID:                client.ID,
		ClientIDIssuedAt:        now.Unix(),
		ClientName:              client.Name,
		RedirectURIs:            client.RedirectURIs,
		GrantTypes:              req.grantTypes(),
		TokenEndpointAuthMethod: authMethodNone,
	})
}

func (req registrationRequest) problem() (code, description string) {
	if len(req.RedirectURIs) == 0 || len(req.RedirectURIs) > maxRedirectURIs {
		return "invalid_redirect_uri", "Register between 1 and 10 redirect_uris."
	}
	for _, uri := range req.RedirectURIs {
		if err := ValidateRedirectURIForRegistration(uri); err != nil {
			return "invalid_redirect_uri", err.Error()
		}
	}
	if req.TokenEndpointAuthMethod != "" && req.TokenEndpointAuthMethod != authMethodNone {
		return "invalid_client_metadata", "Only public clients are supported; set token_endpoint_auth_method to none or leave it out."
	}
	for _, grant := range req.GrantTypes {
		if !slices.Contains(supportedGrantTypes(), grant) {
			return "invalid_client_metadata", "grant_types may only contain authorization_code and refresh_token."
		}
	}
	return "", ""
}

func (req registrationRequest) grantTypes() []string {
	if len(req.GrantTypes) == 0 {
		return supportedGrantTypes()
	}
	return req.GrantTypes
}

func sanitizeClientName(name string) string {
	kept := make([]rune, 0, maxClientNameRunes)
	for _, r := range name {
		if len(kept) == maxClientNameRunes {
			break
		}
		if !unicode.In(r, unicode.Cc, unicode.Cf) {
			kept = append(kept, r)
		}
	}
	return string(kept)
}
