package mcpserver

import (
	"strings"
	"testing"
)

func TestExistingActionVersionGates(t *testing.T) {
	for _, tc := range []struct{ tool, action, kind, method, path, minimum, previous string }{
		{"recipes_write", "add_asset_url", "", "POST", "/api/recipes/example/assets/url", "v3.28.0", "v3.27.0"},
		{"organizers_read", "empty", "tools", "GET", "/api/organizers/tools/empty", "v3.29.0", "v3.28.0"},
		{"organizers_write", "merge", "tools", "POST", "/api/organizers/tools/merge", "v3.29.0", "v3.28.0"},
		{"ingredients_read", "empty", "foods", "GET", "/api/foods/empty", "v3.29.0", "v3.28.0"},
		{"ingredients_read", "empty", "labels", "GET", "/api/groups/labels/empty", "v3.29.0", "v3.28.0"},
		{"ingredients_write", "merge", "labels", "POST", "/api/groups/labels/merge", "v3.29.0", "v3.28.0"},
	} {
		for _, version := range []string{tc.previous, tc.minimum, "unknown"} {
			t.Run(tc.tool+"/"+tc.action+"/"+tc.kind+"/"+version, func(t *testing.T) {
				api := newFakeMealie(t)
				body := "{}"
				if tc.method == "GET" {
					body = "[]"
				}
				api.on(tc.method, tc.path, 200, body)
				d := toolDeps(t, api, &fakeTokens{})
				d.MealieVersion = version
				args := map[string]any{"action": tc.action}
				if tc.kind != "" {
					args["kind"] = tc.kind
				}
				if tc.action == "merge" {
					args["fromId"] = "from"
					args["toId"] = "to"
				}
				if tc.action == "add_asset_url" {
					args["slug"] = "example"
					args["url"] = "https://example.com/image.png"
				}
				res := callTool(t, connectInMemory(t, d), tc.tool, args)
				requests := api.requests()
				if version == tc.minimum {
					if res.IsError || len(requests) != 1 {
						t.Fatalf("result %s, requests %v", resultText(t, res), requests)
					}
				} else if !res.IsError || !strings.Contains(resultText(t, res), tc.minimum) || len(requests) != 0 {
					t.Fatalf("want version error and no requests, got %s, requests %v", resultText(t, res), requests)
				}
			})
		}
	}
}

func TestExistingActionsAtBaseline(t *testing.T) {
	for _, tc := range []struct{ tool, action, kind, method, path string }{
		{"organizers_read", "empty", "categories", "GET", "/api/organizers/categories/empty"},
		{"organizers_read", "empty", "tags", "GET", "/api/organizers/tags/empty"},
		{"organizers_write", "merge", "categories", "POST", "/api/organizers/categories/merge"},
		{"organizers_write", "merge", "tags", "POST", "/api/organizers/tags/merge"},
		{"ingredients_write", "merge", "foods", "PUT", "/api/foods/merge"},
		{"ingredients_write", "merge", "units", "PUT", "/api/units/merge"},
		{"recipes_write", "set_image_url", "", "POST", "/api/recipes/example/image"},
		{"mealie_api", "", "", "POST", "/api/recipes/example/assets/url"},
	} {
		t.Run(tc.tool+"/"+tc.action+"/"+tc.kind, func(t *testing.T) {
			api := newFakeMealie(t)
			body := "{}"
			if tc.method == "GET" {
				body = "[]"
			}
			api.on(tc.method, tc.path, 200, body)
			d := toolDeps(t, api, &fakeTokens{})
			d.MealieVersion = "v3.25.0"
			args := map[string]any{"action": tc.action}
			switch tc.action {
			case "empty":
				args["kind"] = tc.kind
			case "merge":
				args["kind"] = tc.kind
				args["fromId"] = "from"
				args["toId"] = "to"
			case "set_image_url":
				args["slug"] = "example"
				args["url"] = "https://example.com/image.png"
			default:
				args = map[string]any{"method": tc.method, "path": tc.path, "body": map[string]any{"url": "https://example.com/image.png"}}
			}
			res := callTool(t, connectInMemory(t, d), tc.tool, args)
			requests := api.requests()
			if res.IsError || len(requests) != 1 {
				t.Fatalf("result %s, requests %v", resultText(t, res), requests)
			}
			if tc.tool == "mealie_api" && requests[0].Body != `{"url":"https://example.com/image.png"}` {
				t.Fatalf("raw body changed: %s", requests[0].Body)
			}
		})
	}
}
