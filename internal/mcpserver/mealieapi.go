package mcpserver

import (
	"context"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/allowlist"
)

const errQueryValues toolFailure = "query values must be strings, numbers, booleans, or arrays of those."

type mealieAPIInput struct {
	Method string         `json:"method" jsonschema:"HTTP method."`
	Path   string         `json:"path" jsonschema:"Endpoint path starting with /api/, without a query string, e.g. /api/recipes/tomato-soup."`
	Query  map[string]any `json:"query,omitempty" jsonschema:"Query parameters. Values may be a string, number, boolean, or an array of those."`
	Body   any            `json:"body,omitempty" jsonschema:"JSON request body."`
	Full   bool           `json:"full,omitempty" jsonschema:"Return the complete response instead of dropping empty values."`
}

func addMealieAPI(srv *mcp.Server, d Deps) error {
	return addSingleTool(srv, d.Logger, &mcp.Tool{
		Name:        "mealie_api",
		Description: "Call a Mealie API endpoint directly as the current user. Use it only when no other tool covers what you need. Only endpoints in the everyday areas (recipes, timeline, meal plans, organizers, shopping, foods, units, labels, comments, ratings, parser, your own user, household and group) are allowed; admin, auth, tokens, webhooks, notifications, and other users are blocked.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: new(true), OpenWorldHint: new(false)},
	}, map[string][]any{
		"method": {"GET", "POST", "PUT", "PATCH", "DELETE"},
	}, func(ctx context.Context, req *mcp.CallToolRequest, in mealieAPIInput) (*mcp.CallToolResult, any, error) {
		path, err := allowlist.Check(in.Method, in.Path)
		if err != nil {
			return nil, nil, err
		}
		query, err := queryValues(in.Query)
		if err != nil {
			return nil, nil, err
		}
		s, err := openSession(ctx, d, req)
		if err != nil {
			return nil, nil, err
		}
		raw, err := s.call(ctx, in.Method, path, query, in.Body)
		if err != nil {
			return nil, nil, err
		}
		if in.Full && len(raw) > 0 {
			return textResult(raw)
		}
		return textResult(ok(raw))
	})
}

func queryValues(query map[string]any) (url.Values, error) {
	values := make(url.Values, len(query))
	for key, value := range query {
		items, isList := value.([]any)
		if !isList {
			items = []any{value}
		}
		for _, item := range items {
			text, err := queryText(item)
			if err != nil {
				return nil, err
			}
			values.Add(key, text)
		}
	}
	return values, nil
}

func queryText(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(v), nil
	default:
		return "", errQueryValues
	}
}
