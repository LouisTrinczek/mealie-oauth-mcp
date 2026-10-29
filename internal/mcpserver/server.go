package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
)

const (
	serverName = "mealie-oauth-mcp"

	sessionEndedMessage   = "Your Mealie session ended. Reconnect this app to log in again."
	unavailableMessage    = "Mealie is unavailable right now. Try again shortly."
	profileFailureMessage = "Could not read your Mealie profile. Try again."
)

type TokenSource interface {
	MealieToken(ctx context.Context, info *auth.TokenInfo) (secret.Secret, error)
	ReportUnauthorized(ctx context.Context, info *auth.TokenInfo)
}

type ProfileReader interface {
	Self(ctx context.Context, tok secret.Secret) (mealie.User, error)
}

type Deps struct {
	Tokens  TokenSource
	Mealie  ProfileReader
	Version string
}

func New(d Deps) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: d.Version}, nil)
	closedWorld := false
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "whoami",
		Description: "Show which Mealie user this connection acts as, including household and group.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld},
	}, whoami{tokens: d.Tokens, mealie: d.Mealie}.handle)
	return srv
}

type whoamiOutput struct {
	Username  string `json:"username"`
	FullName  string `json:"fullName,omitempty"`
	Email     string `json:"email,omitempty"`
	Household string `json:"household,omitempty"`
	Group     string `json:"group,omitempty"`
	Admin     bool   `json:"admin"`
}

type whoami struct {
	tokens TokenSource
	mealie ProfileReader
}

func (w whoami) handle(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, whoamiOutput, error) {
	info := tokenInfo(req)
	tok, err := w.tokens.MealieToken(ctx, info)
	if err != nil {
		return tokenFailure(err), whoamiOutput{}, nil
	}
	user, err := w.mealie.Self(ctx, tok)
	if err != nil {
		return w.profileFailure(ctx, info, err), whoamiOutput{}, nil
	}
	return nil, whoamiOutput{
		Username:  user.Username,
		FullName:  user.FullName,
		Email:     user.Email,
		Household: user.HouseholdSlug,
		Group:     user.GroupSlug,
		Admin:     user.Admin,
	}, nil
}

func (w whoami) profileFailure(ctx context.Context, info *auth.TokenInfo, err error) *mcp.CallToolResult {
	switch {
	case mealie.IsUnauthorized(err):
		w.tokens.ReportUnauthorized(ctx, info)
		return toolError(sessionEndedMessage)
	case mealie.IsUnavailable(err):
		return toolError(unavailableMessage)
	default:
		return toolError(profileFailureMessage)
	}
}

func tokenFailure(err error) *mcp.CallToolResult {
	if mealie.IsUnavailable(err) {
		return toolError(unavailableMessage)
	}
	return toolError(sessionEndedMessage)
}

func tokenInfo(req *mcp.CallToolRequest) *auth.TokenInfo {
	if req.Extra == nil {
		return nil
	}
	return req.Extra.TokenInfo
}

func toolError(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: msg}},
	}
}
