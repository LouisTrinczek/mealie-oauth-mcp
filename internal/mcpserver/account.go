package mcpserver

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/view"
)

type accountInput struct {
	actionField
	Page    int  `json:"page,omitempty" jsonschema:"households: page number, starting at 1."`
	PerPage int  `json:"perPage,omitempty" jsonschema:"households: results per page (default 20, max 100)."`
	Full    bool `json:"full,omitempty" jsonschema:"household, group: return Mealie's full object instead of the trimmed one."`
}

func addAccountRead(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:    "account_read",
		summary: "Read the current user's household, group, and the households in the group.",
		read:    true,
	}, []action[accountInput]{
		{name: "household", help: "Your household and its preferences. Optional: full.", run: readTrimmed("/api/households/self")},
		{name: "group", help: "Your group and its preferences. Optional: full.", run: readTrimmed("/api/groups/self")},
		{name: "households", help: "List the households in your group. Optional: page, perPage.", run: listHouseholds},
	}, nil)
}

func readTrimmed(path string) func(context.Context, *session, accountInput) (any, error) {
	return func(ctx context.Context, s *session, in accountInput) (any, error) {
		raw, err := s.call(ctx, http.MethodGet, path, nil, nil)
		if err != nil {
			return nil, err
		}
		return trimmed(raw, in.Full)
	}
}

func listHouseholds(ctx context.Context, s *session, in accountInput) (any, error) {
	raw, err := s.call(ctx, http.MethodGet, "/api/groups/households", pageQuery(in.Page, in.PerPage), nil)
	if err != nil {
		return nil, err
	}
	return view.Page(raw, []string{"id", "slug", "name"})
}
