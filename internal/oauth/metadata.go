package oauth

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

const scopeMealie = "mealie"

type authorizationServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	RevocationEndpoint                string   `json:"revocation_endpoint"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
	ClientIDMetadataDocumentSupported bool     `json:"client_id_metadata_document_supported"`
	ScopesSupported                   []string `json:"scopes_supported"`
	IssParameterSupported             bool     `json:"authorization_response_iss_parameter_supported"`
}

func (s *Server) authorizationServerMetadataHandler() http.Handler {
	issuer := s.cfg.Issuer
	metadata := authorizationServerMetadata{
		Issuer:                            issuer,
		AuthorizationEndpoint:             issuer + "/authorize",
		TokenEndpoint:                     issuer + "/token",
		RegistrationEndpoint:              issuer + "/register",
		RevocationEndpoint:                issuer + "/revoke",
		ResponseTypesSupported:            []string{"code"},
		GrantTypesSupported:               supportedGrantTypes(),
		CodeChallengeMethodsSupported:     []string{pkceMethodS256},
		TokenEndpointAuthMethodsSupported: []string{authMethodNone},
		ClientIDMetadataDocumentSupported: true,
		ScopesSupported:                   []string{scopeMealie},
		IssParameterSupported:             true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, metadata)
	})
}

func (s *Server) protectedResourceMetadataHandler() http.Handler {
	return auth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               s.cfg.Resource,
		AuthorizationServers:   []string{s.cfg.Issuer},
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        []string{scopeMealie},
		ResourceName:           "Mealie",
	})
}

func supportedGrantTypes() []string {
	return []string{"authorization_code", "refresh_token"}
}
