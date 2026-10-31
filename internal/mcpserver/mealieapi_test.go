package mcpserver

import (
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const queryRejected = "query values must be strings, numbers, booleans, or arrays of those."

func TestMealieAPI_DeniedMakesNoRequest(t *testing.T) {
	api := newFakeMealie(t)
	tokens := &fakeTokens{}
	cs := connectInMemory(t, toolDeps(t, api, tokens))

	res := callTool(t, cs, "mealie_api", map[string]any{"method": "GET", "path": "/api/admin/users"})

	requireToolError(t, res, "GET /api/admin/users is not allowed. mealie_api covers recipes, timeline, meal plans, organizers, shopping, foods, units, labels, comments, ratings and favorites, the ingredient parser, and your own user, household and group.")
	if seen := api.requests(); len(seen) != 0 {
		t.Errorf("requests = %+v, want none", seen)
	}
	if len(tokens.seen) != 0 {
		t.Errorf("token lookups = %v, want none", tokens.seen)
	}
}

func TestMealieAPI_ForwardsCanonicalRequest(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPatch, "/api/recipes/soup", http.StatusOK, `{"slug":"soup","extra":null}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealie_api", map[string]any{
		"method": "PATCH",
		"path":   "/api/recipes/soup",
		"body":   map[string]any{"nutrition": map[string]any{"calories": "100"}},
	})

	if got := resultText(t, res); res.IsError || got != `{"slug":"soup"}` {
		t.Errorf("result = %s (error %v), want {\"slug\":\"soup\"}", got, res.IsError)
	}
	seen := api.requests()
	if len(seen) != 1 || seen[0].Method != http.MethodPatch || seen[0].Path != "/api/recipes/soup" || seen[0].Body != `{"nutrition":{"calories":"100"}}` {
		t.Errorf("requests = %+v, want one PATCH /api/recipes/soup with the nutrition body", seen)
	}
}

func TestMealieAPI_NormalizesPathBeforeRequest(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/a%20b", http.StatusOK, `{}`)
	cs := connectTools(t, api)

	callTool(t, cs, "mealie_api", map[string]any{"method": "GET", "path": "/api/recipes/a b"})

	if seen := api.requests(); len(seen) != 1 || seen[0].Path != "/api/recipes/a%20b" {
		t.Errorf("requests = %+v, want one GET /api/recipes/a%%20b", seen)
	}
}

func TestMealieAPI_FullKeepsNullsAndEmptyBodyIsOK(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, `{"slug":"soup","extra":null}`)
	api.on(http.MethodDelete, "/api/recipes/soup", http.StatusOK, ``)
	cs := connectTools(t, api)

	full := callTool(t, cs, "mealie_api", map[string]any{"method": "GET", "path": "/api/recipes/soup", "full": true})
	if got := resultText(t, full); got != `{"slug":"soup","extra":null}` {
		t.Errorf("full result = %s, want the raw body", got)
	}
	deleted := callTool(t, cs, "mealie_api", map[string]any{"method": "DELETE", "path": "/api/recipes/soup"})
	if got := resultText(t, deleted); got != `{"ok":true}` {
		t.Errorf("empty-body result = %s, want {\"ok\":true}", got)
	}
}

func TestMealieAPI_QueryEncoding(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes", http.StatusOK, `{}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealie_api", map[string]any{
		"method": "GET",
		"path":   "/api/recipes",
		"query":  map[string]any{"perPage": 5, "tags": []any{"a", "b"}, "requireAllTags": true, "ratio": 1.5, "off": []any{false, 2}},
	})

	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	want := url.Values{"perPage": {"5"}, "tags": {"a", "b"}, "requireAllTags": {"true"}, "ratio": {"1.5"}, "off": {"false", "2"}}
	seen := api.requests()
	if len(seen) != 1 || !reflect.DeepEqual(seen[0].Query, want) {
		t.Errorf("requests = %+v, want query %v", seen, want)
	}
}

func TestMealieAPI_EncodesQueryValuesSorted(t *testing.T) {
	values, err := queryValues(map[string]any{"perPage": float64(5), "tags": []any{"a", "b"}, "requireAllTags": true})
	if err != nil {
		t.Fatalf("queryValues: %v", err)
	}
	if got, want := values.Encode(), "perPage=5&requireAllTags=true&tags=a&tags=b"; got != want {
		t.Errorf("encoded = %q, want %q", got, want)
	}
}

func TestMealieAPI_RejectsNestedQuery(t *testing.T) {
	tests := []struct {
		name  string
		query map[string]any
	}{
		{"object", map[string]any{"a": map[string]any{"b": 1}}},
		{"nested array", map[string]any{"a": []any{[]any{"x"}}}},
		{"null", map[string]any{"a": nil}},
		{"null in array", map[string]any{"a": []any{"x", nil}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			tokens := &fakeTokens{}
			cs := connectInMemory(t, toolDeps(t, api, tokens))

			res := callTool(t, cs, "mealie_api", map[string]any{"method": "GET", "path": "/api/recipes", "query": tt.query})

			requireToolError(t, res, queryRejected)
			if seen := api.requests(); len(seen) != 0 {
				t.Errorf("requests = %+v, want none", seen)
			}
			if len(tokens.seen) != 0 {
				t.Errorf("token lookups = %v, want none", tokens.seen)
			}
		})
	}
}

func TestMealieAPI_Annotations(t *testing.T) {
	cs := connectTools(t, newFakeMealie(t))
	tool := findTool(t, cs, "mealie_api")

	a := tool.Annotations
	if a == nil || a.DestructiveHint == nil || !*a.DestructiveHint || a.OpenWorldHint == nil || *a.OpenWorldHint || a.ReadOnlyHint || a.IdempotentHint {
		t.Errorf("annotations = %+v, want destructive, closed world, not read-only, not idempotent", a)
	}
	schema := tool.InputSchema.(map[string]any)
	method := schema["properties"].(map[string]any)["method"].(map[string]any)
	if want := []any{"GET", "POST", "PUT", "PATCH", "DELETE"}; !reflect.DeepEqual(method["enum"], want) {
		t.Errorf("method enum = %v, want %v", method["enum"], want)
	}
	if want := []any{"method", "path"}; !reflect.DeepEqual(schema["required"], want) {
		t.Errorf("required = %v, want %v", schema["required"], want)
	}
}

func TestMealieAPI_RejectsMethodOutsideEnum(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "mealie_api", Arguments: map[string]any{"method": "TRACE", "path": "/api/recipes"}})
	if err == nil && !res.IsError {
		t.Error("TRACE accepted, want the schema to reject it")
	}
	if seen := api.requests(); len(seen) != 0 {
		t.Errorf("requests = %+v, want none", seen)
	}
}

func TestMealieAPI_RedactsTokens(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, "/api/recipes", http.StatusBadRequest, `{"detail":"bad Bearer abc.def"}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealie_api", map[string]any{"method": "POST", "path": "/api/recipes", "body": "x"})

	got := resultText(t, res)
	want := "Mealie rejected the request (400): bad [REDACTED]. Fix the arguments and try again."
	if !res.IsError || got != want {
		t.Errorf("result = %q (error %v), want %q", got, res.IsError, want)
	}
}

func TestMealieAPI_TokenFailureMakesNoRequest(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectInMemory(t, toolDeps(t, api, &fakeTokens{err: errors.New("revoked")}))

	res := callTool(t, cs, "mealie_api", map[string]any{"method": "GET", "path": "/api/recipes"})

	requireToolError(t, res, string(errSessionEnded))
	if seen := api.requests(); len(seen) != 0 {
		t.Errorf("requests = %+v, want none", seen)
	}
}

func TestAddSingleTool_SchemaError(t *testing.T) {
	srv := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	err := addSingleTool[channelInput](srv, toolkit{}, &mcp.Tool{Name: "broken"}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "inferring") {
		t.Errorf("addSingleTool error = %v, want an inferring error", err)
	}
}

func TestMealieAPI_FullEmptyBodyIsOK(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodDelete, "/api/comments/abc", http.StatusNoContent, "")
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealie_api", map[string]any{"method": "DELETE", "path": "/api/comments/abc", "full": true})

	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %q (error %v), want {\"ok\":true}", got, res.IsError)
	}
}

func TestMealieAPI_FullNullBodyIsOK(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodDelete, "/api/comments/abc", http.StatusOK, `null`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealie_api", map[string]any{"method": "DELETE", "path": "/api/comments/abc", "full": true})

	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %q (error %v), want {\"ok\":true}", got, res.IsError)
	}
}

func TestMealieAPI_HostileQueryIsEscaped(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes", http.StatusOK, `{}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealie_api", map[string]any{
		"method": "GET",
		"path":   "/api/recipes",
		"query":  map[string]any{"a&b": "c=d", "x": "1#frag"},
	})

	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	seen := api.requests()
	if len(seen) != 1 || seen[0].Path != "/api/recipes" {
		t.Fatalf("requests = %+v, want one GET /api/recipes", seen)
	}
	if want := "a%26b=c%3Dd&x=1%23frag"; seen[0].RawQuery != want {
		t.Errorf("RawQuery = %q, want %q", seen[0].RawQuery, want)
	}
}
