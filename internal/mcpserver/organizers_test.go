package mcpserver

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

const (
	tagBody      = `{"id":"t-1","name":"Quick","slug":"quick","groupId":"g-1","recipes":[]}`
	tagsPageBody = `{"page":1,"per_page":20,"total":1,"total_pages":1,"items":[` + tagBody + `],"next":null,"previous":null}`
	cookbookBody = `{"id":"c-1","name":"Weeknights","slug":"weeknights","description":"Fast dinners","position":1,"public":false,"queryFilterString":"tags.name IN [\"quick\"]","groupId":"g-1","householdId":"h-1"}`
	cookbookPage = `{"page":1,"per_page":20,"total":1,"total_pages":1,"items":[` + cookbookBody + `],"next":null,"previous":null}`
)

func TestOrganizers_KindPaths(t *testing.T) {
	tests := []struct {
		kind, path string
		wantQuery  url.Values
	}{
		{"cookbooks", "/api/households/cookbooks", url.Values{"page": {"1"}, "perPage": {"20"}}},
		{"categories", "/api/organizers/categories", url.Values{"search": {"din"}, "page": {"1"}, "perPage": {"20"}}},
		{"tags", "/api/organizers/tags", url.Values{"search": {"din"}, "page": {"1"}, "perPage": {"20"}}},
		{"tools", "/api/organizers/tools", url.Values{"search": {"din"}, "page": {"1"}, "perPage": {"20"}}},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, tt.path, http.StatusOK, tagsPageBody)
			cs := connectTools(t, api)

			res := callTool(t, cs, "organizers_read", map[string]any{"action": "list", "kind": tt.kind, "search": "din"})
			if res.IsError {
				t.Fatalf("tool error: %s", resultText(t, res))
			}
			seen := api.requests()
			if len(seen) != 1 || seen[0].Method != http.MethodGet || seen[0].Path != tt.path || !reflect.DeepEqual(seen[0].Query, tt.wantQuery) {
				t.Errorf("requests = %+v, want one GET %s with query %v", seen, tt.path, tt.wantQuery)
			}
		})
	}
}

func TestOrganizersRead_ListGolden(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/organizers/tags", http.StatusOK, tagsPageBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "organizers_read", map[string]any{"action": "list", "kind": "tags"})
	assertGolden(t, "organizers_tags_list", resultText(t, res))
}

func TestOrganizersRead_GetBySlugUsesSlugRoute(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		path string
	}{
		{"tag by slug", map[string]any{"kind": "tags", "slug": "quick"}, "/api/organizers/tags/slug/quick"},
		{"tool by id", map[string]any{"kind": "tools", "id": "t-9"}, "/api/organizers/tools/t-9"},
		{"category slug wins over id", map[string]any{"kind": "categories", "slug": "dinner", "id": "c-9"}, "/api/organizers/categories/slug/dinner"},
		{"cookbook by slug", map[string]any{"kind": "cookbooks", "slug": "weeknights"}, "/api/households/cookbooks/weeknights"},
		{"cookbook by id", map[string]any{"kind": "cookbooks", "id": "c-1"}, "/api/households/cookbooks/c-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, tt.path, http.StatusOK, tagBody)
			cs := connectTools(t, api)

			tt.args["action"] = "get"
			res := callTool(t, cs, "organizers_read", tt.args)
			if res.IsError {
				t.Fatalf("tool error: %s", resultText(t, res))
			}
			if seen := api.requests(); len(seen) != 1 || seen[0].Method != http.MethodGet || seen[0].Path != tt.path {
				t.Errorf("requests = %+v, want one GET %s", seen, tt.path)
			}
		})
	}
}

func TestOrganizersRead_CookbookGetGolden(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/households/cookbooks/weeknights", http.StatusOK, cookbookBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "organizers_read", map[string]any{"action": "get", "kind": "cookbooks", "slug": "weeknights"})
	assertGolden(t, "organizers_cookbook_get", resultText(t, res))
}

func TestOrganizersRead_GetCompactsLinkedRecipes(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/organizers/categories/slug/dinner", http.StatusOK,
		`{"id":"c-1","name":"Dinner","slug":"dinner","groupId":"g-1","recipes":[{"id":"r-1","slug":"soup","name":"Soup","description":"long","recipeIngredient":[]},{"id":"r-2","slug":"stew","name":"Stew"}]}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "organizers_read", map[string]any{"action": "get", "kind": "categories", "slug": "dinner"})
	if got, want := resultText(t, res), `{"id":"c-1","name":"Dinner","recipes":["Soup","Stew"],"slug":"dinner"}`; res.IsError || got != want {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, want)
	}
}

func TestOrganizersRead_GetFullReturnsTrimmedObject(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/households/cookbooks/weeknights", http.StatusOK, cookbookBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "organizers_read", map[string]any{"action": "get", "kind": "cookbooks", "slug": "weeknights", "full": true})
	assertGolden(t, "organizers_cookbook_get_full", resultText(t, res))
}

func TestOrganizersRead_GetRejectsNonObject(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/organizers/tags/t-1", http.StatusOK, `[1]`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "organizers_read", map[string]any{"action": "get", "kind": "tags", "id": "t-1"})
	if !res.IsError {
		t.Errorf("result = %q, want an error", resultText(t, res))
	}
}

func TestOrganizersRead_GetNeedsSlugOrID(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res := callTool(t, cs, "organizers_read", map[string]any{"action": "get", "kind": "tags"})
	if got, want := resultText(t, res), `Action "get" needs slug or id.`; !res.IsError || got != want {
		t.Errorf("result = %q (error %v), want %q", got, res.IsError, want)
	}
}

func TestOrganizersRead_GetRejectsUnsafeSegment(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res := callTool(t, cs, "organizers_read", map[string]any{"action": "get", "kind": "tags", "slug": "a/b"})
	if !res.IsError || len(api.requests()) != 0 {
		t.Errorf("result = %q (error %v), requests = %+v, want a path error and no request", resultText(t, res), res.IsError, api.requests())
	}
}

func TestOrganizersRead_Empty(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/organizers/tags/empty", http.StatusOK, `[`+tagBody+`]`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "organizers_read", map[string]any{"action": "empty", "kind": "tags"})
	want := `{"items":[{"id":"t-1","name":"Quick","slug":"quick"}],"total":1}`
	if got := resultText(t, res); res.IsError || got != want {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, want)
	}
}

func TestOrganizers_CookbookRestrictions(t *testing.T) {
	tests := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"organizers_read", map[string]any{"action": "empty", "kind": "cookbooks"}, `Action "empty" does not apply to cookbooks.`},
		{"organizers_write", map[string]any{"action": "merge", "kind": "cookbooks", "fromId": "a", "toId": "b"}, `Action "merge" does not apply to cookbooks.`},
		{"organizers_write", map[string]any{"action": "create", "kind": "tags", "name": "X", "description": "d"}, `description does not apply to tags.`},
		{"organizers_write", map[string]any{"action": "create", "kind": "tools", "name": "X", "queryFilter": "q"}, `queryFilter does not apply to tools.`},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			api := newFakeMealie(t)
			cs := connectTools(t, api)

			res := callTool(t, cs, tt.tool, tt.args)
			if got := resultText(t, res); !res.IsError || got != tt.want {
				t.Errorf("result = %q (error %v), want %q", got, res.IsError, tt.want)
			}
			if got := api.requests(); len(got) != 0 {
				t.Errorf("Mealie got %d requests, want none", len(got))
			}
		})
	}
}

func TestOrganizersWrite_Create(t *testing.T) {
	tests := []struct {
		name     string
		args     map[string]any
		path     string
		response string
		want     string
		wantBody string
	}{
		{
			"tag", map[string]any{"kind": "tags", "name": "Quick"}, "/api/organizers/tags", tagBody,
			`{"id":"t-1","name":"Quick","slug":"quick"}`, `{"name":"Quick"}`,
		},
		{
			"cookbook", map[string]any{"kind": "cookbooks", "name": "Weeknights", "description": "Fast dinners", "queryFilter": `tags.name IN ["quick"]`},
			"/api/households/cookbooks", cookbookBody,
			`{"description":"Fast dinners","id":"c-1","name":"Weeknights","slug":"weeknights"}`,
			`{"description":"Fast dinners","name":"Weeknights","queryFilterString":"tags.name IN [\"quick\"]"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodPost, tt.path, http.StatusCreated, tt.response)
			cs := connectTools(t, api)

			tt.args["action"] = "create"
			res := callTool(t, cs, "organizers_write", tt.args)
			if got := resultText(t, res); res.IsError || got != tt.want {
				t.Errorf("result = %s (error %v), want %s", got, res.IsError, tt.want)
			}
			if seen := api.requests(); len(seen) != 1 || seen[0].Body != tt.wantBody {
				t.Errorf("requests = %+v, want one POST with body %s", seen, tt.wantBody)
			}
		})
	}
}

func TestOrganizers_MealieFailures(t *testing.T) {
	tests := []struct {
		name, tool, method, path string
		args                     map[string]any
	}{
		{"list", "organizers_read", http.MethodGet, "/api/organizers/tags", map[string]any{"action": "list", "kind": "tags"}},
		{"get", "organizers_read", http.MethodGet, "/api/organizers/tags/t-1", map[string]any{"action": "get", "kind": "tags", "id": "t-1"}},
		{"empty", "organizers_read", http.MethodGet, "/api/organizers/tags/empty", map[string]any{"action": "empty", "kind": "tags"}},
		{"create", "organizers_write", http.MethodPost, "/api/organizers/tags", map[string]any{"action": "create", "kind": "tags", "name": "X"}},
		{"merge", "organizers_write", http.MethodPost, "/api/organizers/tags/merge", map[string]any{"action": "merge", "kind": "tags", "fromId": "a", "toId": "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(tt.method, tt.path, http.StatusNotFound, `{"detail":"nope"}`)
			cs := connectTools(t, api)

			res := callTool(t, cs, tt.tool, tt.args)
			if got := resultText(t, res); !res.IsError || !strings.HasPrefix(got, "Mealie could not find that (404)") {
				t.Errorf("result = %q (error %v), want a 404 failure", got, res.IsError)
			}
		})
	}
}

func TestOrganizersWrite_CreateAcceptsUnreadableResponse(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, "/api/organizers/tags", http.StatusCreated, `[1]`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "organizers_write", map[string]any{"action": "create", "kind": "tags", "name": "X"})
	if res.IsError || resultText(t, res) != `{"ok":true}` {
		t.Errorf("result = %q (error %v), want {\"ok\":true}", resultText(t, res), res.IsError)
	}
}

func TestOrganizersWrite_CreateNeedsName(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res := callTool(t, cs, "organizers_write", map[string]any{"action": "create", "kind": "tags"})
	if got, want := resultText(t, res), `Action "create" needs name.`; !res.IsError || got != want {
		t.Errorf("result = %q (error %v), want %q", got, res.IsError, want)
	}
}

func TestOrganizersWrite_UpdateMergesFields(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/organizers/tags/t-1", http.StatusOK, tagBody)
	api.on(http.MethodPut, "/api/organizers/tags/t-1", http.StatusOK, `{"id":"t-1","name":"Fast","slug":"fast","groupId":"g-1","recipes":[{"id":"r-1","name":"Soup","description":"long"}]}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "organizers_write", map[string]any{"action": "update", "kind": "tags", "id": "t-1", "fields": map[string]any{"name": "Fast", "slug": "fast"}})
	want := `{"id":"t-1","name":"Fast","slug":"fast"}`
	if got := resultText(t, res); res.IsError || got != want {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, want)
	}
	seen := api.requests()
	wantBody := `{"groupId":"g-1","id":"t-1","name":"Fast","recipes":[],"slug":"fast"}`
	if len(seen) != 2 || seen[1].Method != http.MethodPut || seen[1].Body != wantBody {
		t.Errorf("requests = %+v, want GET then PUT with body %s", seen, wantBody)
	}
}

func TestOrganizersWrite_Delete(t *testing.T) {
	tests := []struct{ kind, path string }{
		{"cookbooks", "/api/households/cookbooks/c-1"},
		{"tags", "/api/organizers/tags/c-1"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodDelete, tt.path, http.StatusOK, tagBody)
			cs := connectTools(t, api)

			res := callTool(t, cs, "organizers_write", map[string]any{"action": "delete", "kind": tt.kind, "id": "c-1"})
			if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
				t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
			}
			if seen := api.requests(); len(seen) != 1 || seen[0].Method != http.MethodDelete || seen[0].Path != tt.path {
				t.Errorf("requests = %+v, want one DELETE %s", seen, tt.path)
			}
		})
	}
}

func TestOrganizersWrite_MergeBody(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, "/api/organizers/categories/merge", http.StatusOK, `{"message":"merged"}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "organizers_write", map[string]any{"action": "merge", "kind": "categories", "fromId": "a", "toId": "b"})
	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
	}
	if seen := api.requests(); len(seen) != 1 || seen[0].Method != http.MethodPost || seen[0].Body != `{"fromId":"a","toId":"b"}` {
		t.Errorf("requests = %+v, want one POST with body {\"fromId\":\"a\",\"toId\":\"b\"}", seen)
	}
}

func TestOrganizersWrite_MergeNeedsBothIDs(t *testing.T) {
	tests := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "merge", "kind": "tags", "toId": "b"}, `Action "merge" needs fromId.`},
		{map[string]any{"action": "merge", "kind": "tags", "fromId": "a"}, `Action "merge" needs toId.`},
	}
	for _, tt := range tests {
		api := newFakeMealie(t)
		cs := connectTools(t, api)

		res := callTool(t, cs, "organizers_write", tt.args)
		if got := resultText(t, res); !res.IsError || got != tt.want {
			t.Errorf("result = %q (error %v), want %q", got, res.IsError, tt.want)
		}
	}
}

func TestOrganizers_KindEnum(t *testing.T) {
	cs := connectTools(t, newFakeMealie(t))

	for _, name := range []string{"organizers_read", "organizers_write"} {
		schema := schemaJSON(t, findTool(t, cs, name).InputSchema)
		kind := schema["properties"].(map[string]any)["kind"].(map[string]any)
		if want := []any{"cookbooks", "categories", "tags", "tools"}; !reflect.DeepEqual(kind["enum"], want) {
			t.Errorf("%s kind enum = %v, want %v", name, kind["enum"], want)
		}
		if want := []any{"action", "kind"}; !reflect.DeepEqual(schema["required"], want) {
			t.Errorf("%s required = %v, want %v", name, schema["required"], want)
		}
	}
}
