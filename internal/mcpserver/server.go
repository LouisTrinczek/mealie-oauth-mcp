package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
)

const serverName = "mealie-oauth-mcp"

const (
	errSessionEnded      toolFailure = "Your Mealie session ended. Reconnect this app to log in again."
	errMealieUnavailable toolFailure = "Mealie is unavailable right now. Try again shortly."
	errProfileUnreadable toolFailure = "Could not read your Mealie profile. Try again."
)

type toolFailure string

func (f toolFailure) Error() string {
	return string(f)
}

type TokenSource interface {
	MealieToken(ctx context.Context, info *auth.TokenInfo) (secret.Secret, error)
	ReportUnauthorized(ctx context.Context, info *auth.TokenInfo)
}

type MealieClient interface {
	Self(ctx context.Context, tok secret.Secret) (mealie.User, error)
	Call(ctx context.Context, tok secret.Secret, method, path string, query url.Values, body any) (json.RawMessage, error)
}

type Deps struct {
	Tokens  TokenSource
	Mealie  MealieClient
	Clock   func() time.Time
	Version string
}

func New(d Deps) (*mcp.Server, error) {
	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: d.Version}, nil)
	closedWorld := false
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "whoami",
		Description: "Show which Mealie user this connection acts as, including household and group.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld},
	}, whoami{tokens: d.Tokens, mealie: d.Mealie}.handle)
	return srv, errors.Join(
		addAccountRead(srv, d),
		addRecipesRead(srv, d),
		addRecipesWrite(srv, d),
		addHistoryRead(srv, d),
		addHistoryWrite(srv, d),
		addMealplansRead(srv, d),
		addMealplansWrite(srv, d),
		addOrganizersRead(srv, d),
		addOrganizersWrite(srv, d),
	)
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
	mealie MealieClient
}

func (w whoami) handle(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, whoamiOutput, error) {
	info := tokenInfo(req)
	tok, err := w.tokens.MealieToken(ctx, info)
	if err != nil {
		return nil, whoamiOutput{}, tokenFailure(err)
	}
	user, err := w.mealie.Self(ctx, tok)
	if err != nil {
		return nil, whoamiOutput{}, w.profileFailure(ctx, info, err)
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

func (w whoami) profileFailure(ctx context.Context, info *auth.TokenInfo, err error) error {
	switch {
	case mealie.IsUnauthorized(err):
		w.tokens.ReportUnauthorized(ctx, info)
		return errSessionEnded
	case mealie.IsUnavailable(err):
		return errMealieUnavailable
	default:
		return errProfileUnreadable
	}
}

func tokenFailure(err error) error {
	if mealie.IsUnavailable(err) {
		return errMealieUnavailable
	}
	return errSessionEnded
}

func tokenInfo(req *mcp.CallToolRequest) *auth.TokenInfo {
	if req.Extra == nil {
		return nil
	}
	return req.Extra.TokenInfo
}
