package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/trace"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/metrics"
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
	Logger  *slog.Logger
	Metrics *metrics.Registry
	Tracing trace.TracerProvider
}

func New(d Deps) (*mcp.Server, error) {
	srv := mcp.NewServer(&mcp.Implementation{Name: serverName, Version: d.Version}, nil)
	closedWorld := false
	whoamiTool := &mcp.Tool{
		Name:        "whoami",
		Description: "Show which Mealie user this connection acts as, including household and group.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld},
	}
	mcp.AddTool(srv, whoamiTool, observed(d.toolkit(), whoamiTool.Name, whoami{deps: d}.handle))
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
		addShoppingRead(srv, d),
		addShoppingWrite(srv, d),
		addIngredientsRead(srv, d),
		addIngredientsWrite(srv, d),
		addFeedbackRead(srv, d),
		addFeedbackWrite(srv, d),
		addMealieAPI(srv, d),
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
	deps Deps
}

func (w whoami) handle(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, whoamiOutput, error) {
	s, err := openSession(ctx, w.deps, req)
	if err != nil {
		return nil, whoamiOutput{}, err
	}
	user, err := s.self(ctx)
	if err != nil {
		return nil, whoamiOutput{}, err
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
