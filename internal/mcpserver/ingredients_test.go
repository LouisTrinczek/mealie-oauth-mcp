package mcpserver

import (
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	foodBody  = `{"id":"f-1","name":"Flour","pluralName":"Flours","description":"","label":{"id":"l-1","name":"Baking","color":"#fff"},"aliases":[],"createdAt":"2026-01-01"}`
	foodsPage = `{"page":1,"per_page":20,"total":1,"total_pages":1,"items":[` + foodBody + `],"next":null,"previous":null}`
	unitBody  = `{"id":"u-1","name":"gram","pluralName":"grams","abbreviation":"g","description":""}`
	labelBody = `{"id":"l-1","name":"Baking","color":"#fff","groupId":"g-1"}`
)

func TestIngredientsRead_ListGolden(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/foods", http.StatusOK, foodsPage)
	cs := connectTools(t, api)

	res := callTool(t, cs, "ingredients_read", map[string]any{"action": "list", "kind": "foods", "search": "flo"})
	assertGolden(t, "ingredients_foods_list", resultText(t, res))
	want := url.Values{"search": {"flo"}, "page": {"1"}, "perPage": {"20"}}
	if seen := api.requests(); len(seen) != 1 || !reflect.DeepEqual(seen[0].Query, want) {
		t.Errorf("requests = %+v, want one GET with query %v", seen, want)
	}
}

func TestIngredientsRead_ListPaths(t *testing.T) {
	tests := []struct{ kind, path string }{
		{"units", "/api/units"},
		{"labels", "/api/groups/labels"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, tt.path, http.StatusOK, `{"page":2,"per_page":5,"total":0,"total_pages":0,"items":[]}`)
			cs := connectTools(t, api)

			res := callTool(t, cs, "ingredients_read", map[string]any{"action": "list", "kind": tt.kind, "page": 2, "perPage": 5})
			want := url.Values{"page": {"2"}, "perPage": {"5"}}
			if seen := api.requests(); res.IsError || len(seen) != 1 || !reflect.DeepEqual(seen[0].Query, want) {
				t.Errorf("result = %s, requests = %+v, want one GET %s with query %v", resultText(t, res), seen, tt.path, want)
			}
		})
	}
}

func TestIngredientsRead_SearchOnlyForFoods(t *testing.T) {
	for _, kind := range []string{"units", "labels"} {
		api := newFakeMealie(t)
		cs := connectTools(t, api)

		res := callTool(t, cs, "ingredients_read", map[string]any{"action": "list", "kind": kind, "search": "x"})
		if got, want := resultText(t, res), "search does not apply to "+kind+"."; !res.IsError || got != want || len(api.requests()) != 0 {
			t.Errorf("result = %q (error %v), want %q and no request", got, res.IsError, want)
		}
	}
}

func TestIngredientsRead_Get(t *testing.T) {
	tests := []struct{ kind, path, body, want string }{
		{"foods", "/api/foods/f-1", foodBody, `{"id":"f-1","label":{"color":"#fff","id":"l-1","name":"Baking"},"name":"Flour","pluralName":"Flours"}`},
		{"units", "/api/units/u-1", unitBody, `{"abbreviation":"g","id":"u-1","name":"gram","pluralName":"grams"}`},
		{"labels", "/api/groups/labels/l-1", labelBody, `{"color":"#fff","id":"l-1","name":"Baking"}`},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, tt.path, http.StatusOK, tt.body)
			cs := connectTools(t, api)

			id := tt.path[strings.LastIndex(tt.path, "/")+1:]
			res := callTool(t, cs, "ingredients_read", map[string]any{"action": "get", "kind": tt.kind, "id": id})
			if got := resultText(t, res); res.IsError || got != tt.want {
				t.Errorf("result = %s (error %v), want %s", got, res.IsError, tt.want)
			}
		})
	}
}

func TestIngredientsRead_GetFailures(t *testing.T) {
	tests := []struct {
		id, want string
	}{
		{"", `Action "get" needs id.`},
		{"a/b", `"a/b" is not a valid slug or id.`},
	}
	for _, tt := range tests {
		api := newFakeMealie(t)
		cs := connectTools(t, api)

		res := callTool(t, cs, "ingredients_read", map[string]any{"action": "get", "kind": "foods", "id": tt.id})
		requireToolError(t, res, tt.want)
		if len(api.requests()) != 0 {
			t.Errorf("requests = %+v, want none", api.requests())
		}
	}
}

func TestIngredientsRead_Empty(t *testing.T) {
	tests := []struct{ kind, path, body, want string }{
		{"foods", "/api/foods/empty", `[` + foodBody + `]`, `{"items":[{"id":"f-1","label":{"id":"l-1","name":"Baking"},"name":"Flour","pluralName":"Flours"}],"total":1}`},
		{"labels", "/api/groups/labels/empty", `[` + labelBody + `]`, `{"items":[{"color":"#fff","id":"l-1","name":"Baking"}],"total":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, tt.path, http.StatusOK, tt.body)
			cs := connectTools(t, api)

			res := callTool(t, cs, "ingredients_read", map[string]any{"action": "empty", "kind": tt.kind})
			if got := resultText(t, res); res.IsError || got != tt.want {
				t.Errorf("result = %s (error %v), want %s", got, res.IsError, tt.want)
			}
		})
	}
}

func TestIngredientsRead_EmptyNotForUnits(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res := callTool(t, cs, "ingredients_read", map[string]any{"action": "empty", "kind": "units"})
	if got, want := resultText(t, res), `Action "empty" does not apply to units.`; !res.IsError || got != want || len(api.requests()) != 0 {
		t.Errorf("result = %q (error %v), want %q and no request", got, res.IsError, want)
	}
}

func TestIngredientsRead_Parse(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, "/api/parser/ingredients", http.StatusOK,
		`[{"input":"2 cups flour","confidence":{"average":0.9},"ingredient":{"quantity":2,"unit":{"id":"u-1","name":"cup"},"food":{"id":"f-1","name":"flour"},"note":"","originalText":"2 cups flour"}}]`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "ingredients_read", map[string]any{"action": "parse", "ingredients": []string{"2 cups flour"}})
	assertGolden(t, "ingredients_parse", resultText(t, res))
	wantBody := `{"ingredients":["2 cups flour"],"parser":"nlp"}`
	if seen := api.requests(); len(seen) != 1 || seen[0].Method != http.MethodPost || seen[0].Body != wantBody {
		t.Errorf("requests = %+v, want one POST with body %s", seen, wantBody)
	}
}

func TestIngredientsRead_ParseWithoutKind(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, "/api/parser/ingredients", http.StatusOK, `[{"input":"1 egg","ingredient":{"quantity":1,"note":"egg"}}]`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "ingredients_read", map[string]any{"action": "parse", "ingredients": []string{"1 egg"}})
	if res.IsError || len(api.requests()) != 1 {
		t.Errorf("result = %q (error %v), requests = %d, want a success and one request", resultText(t, res), res.IsError, len(api.requests()))
	}
}

func TestIngredientsRead_ListNeedsKind(t *testing.T) {
	for _, action := range []string{"list", "get", "empty"} {
		api := newFakeMealie(t)
		cs := connectTools(t, api)

		res := callTool(t, cs, "ingredients_read", map[string]any{"action": action, "id": "f-1"})
		requireToolError(t, res, `Action "`+action+`" needs kind.`)
		if len(api.requests()) != 0 {
			t.Errorf("%s: requests = %+v, want none", action, api.requests())
		}
	}
}

func TestIngredientsRead_GetFull(t *testing.T) {
	api := newFakeMealie(t)
	raw := `{"id":"f-1","name":"Flour","description":null}`
	api.on(http.MethodGet, "/api/foods/f-1", http.StatusOK, raw)
	cs := connectTools(t, api)

	res := callTool(t, cs, "ingredients_read", map[string]any{"action": "get", "kind": "foods", "id": "f-1", "full": true})
	if got := resultText(t, res); res.IsError || got != raw {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, raw)
	}
}

func TestIngredientsRead_ParseRejectsBlank(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res := callTool(t, cs, "ingredients_read", map[string]any{"action": "parse", "ingredients": []string{"1 egg", "  "}})
	requireToolError(t, res, "Ingredient 2 is blank. Remove it and try again.")
	if len(api.requests()) != 0 {
		t.Errorf("requests = %+v, want none", api.requests())
	}
}

func TestIngredientsRead_ParseNeedsIngredients(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res := callTool(t, cs, "ingredients_read", map[string]any{"action": "parse", "kind": "foods"})
	if got, want := resultText(t, res), `Action "parse" needs ingredients.`; !res.IsError || got != want || len(api.requests()) != 0 {
		t.Errorf("result = %q (error %v), want %q and no request", got, res.IsError, want)
	}
}

func TestIngredientsRead_ParseLimit(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	lines := make([]string, 51)
	for i := range lines {
		lines[i] = strconv.Itoa(i) + " eggs"
	}
	res := callTool(t, cs, "ingredients_read", map[string]any{"action": "parse", "kind": "foods", "ingredients": lines})
	if got, want := resultText(t, res), "parse takes at most 50 ingredients."; !res.IsError || got != want || len(api.requests()) != 0 {
		t.Errorf("result = %q (error %v), want %q and no request", got, res.IsError, want)
	}
}

func TestIngredients_MealieFailures(t *testing.T) {
	tests := []struct {
		name, tool, method, path string
		args                     map[string]any
	}{
		{"list", "ingredients_read", http.MethodGet, "/api/foods", map[string]any{"action": "list", "kind": "foods"}},
		{"get", "ingredients_read", http.MethodGet, "/api/foods/f-1", map[string]any{"action": "get", "kind": "foods", "id": "f-1"}},
		{"empty", "ingredients_read", http.MethodGet, "/api/foods/empty", map[string]any{"action": "empty", "kind": "foods"}},
		{"parse", "ingredients_read", http.MethodPost, "/api/parser/ingredients", map[string]any{"action": "parse", "kind": "foods", "ingredients": []string{"x"}}},
		{"create", "ingredients_write", http.MethodPost, "/api/foods", map[string]any{"action": "create", "kind": "foods", "name": "X"}},
		{"merge", "ingredients_write", http.MethodPut, "/api/foods/merge", map[string]any{"action": "merge", "kind": "foods", "fromId": "a", "toId": "b"}},
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

func TestIngredientsWrite_Create(t *testing.T) {
	tests := []struct {
		name, path, response, want, wantBody string
		args                                 map[string]any
	}{
		{
			"food", "/api/foods", foodBody,
			`{"id":"f-1","label":{"id":"l-1","name":"Baking"},"name":"Flour","pluralName":"Flours"}`, `{"name":"Flour","pluralName":"Flours"}`,
			map[string]any{"kind": "foods", "name": "Flour", "pluralName": "Flours"},
		},
		{
			"unit", "/api/units", unitBody,
			`{"abbreviation":"g","id":"u-1","name":"gram"}`, `{"abbreviation":"g","name":"gram","pluralName":"grams"}`,
			map[string]any{"kind": "units", "name": "gram", "pluralName": "grams", "abbreviation": "g"},
		},
		{
			"label", "/api/groups/labels", labelBody,
			`{"color":"#fff","id":"l-1","name":"Baking"}`, `{"color":"#fff","name":"Baking"}`,
			map[string]any{"kind": "labels", "name": "Baking", "color": "#fff"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.args["action"] = "create"
			res, seen := createIngredientVia(t, tt.path, tt.response, tt.args)
			if got := resultText(t, res); res.IsError || got != tt.want {
				t.Errorf("result = %s (error %v), want %s", got, res.IsError, tt.want)
			}
			if len(seen) != 1 || seen[0].Body != tt.wantBody {
				t.Errorf("requests = %+v, want one POST with body %s", seen, tt.wantBody)
			}
		})
	}
}

func createIngredientVia(t *testing.T, path, response string, args map[string]any) (*mcp.CallToolResult, []seenRequest) {
	t.Helper()
	api := newFakeMealie(t)
	api.on(http.MethodPost, path, http.StatusCreated, response)
	cs := connectTools(t, api)
	return callTool(t, cs, "ingredients_write", args), api.requests()
}

func TestIngredientsWrite_CreateNeedsName(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res := callTool(t, cs, "ingredients_write", map[string]any{"action": "create", "kind": "foods"})
	if got, want := resultText(t, res), `Action "create" needs name.`; !res.IsError || got != want {
		t.Errorf("result = %q (error %v), want %q", got, res.IsError, want)
	}
}

func TestIngredientsWrite_CreateRejectsForeignField(t *testing.T) {
	tests := []struct {
		kind, field, want string
	}{
		{"foods", "color", "color does not apply to foods."},
		{"foods", "abbreviation", "abbreviation does not apply to foods."},
		{"units", "color", "color does not apply to units."},
		{"labels", "pluralName", "pluralName does not apply to labels."},
		{"labels", "abbreviation", "abbreviation does not apply to labels."},
	}
	for _, tt := range tests {
		t.Run(tt.kind+" "+tt.field, func(t *testing.T) {
			api := newFakeMealie(t)
			cs := connectTools(t, api)

			res := callTool(t, cs, "ingredients_write", map[string]any{"action": "create", "kind": tt.kind, "name": "X", tt.field: "v"})
			if got := resultText(t, res); !res.IsError || got != tt.want || len(api.requests()) != 0 {
				t.Errorf("result = %q (error %v), want %q and no request", got, res.IsError, tt.want)
			}
		})
	}
}

func TestIngredientsWrite_UpdateMergesFields(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/units/u-1", http.StatusOK, unitBody)
	api.on(http.MethodPut, "/api/units/u-1", http.StatusOK, `{"id":"u-1","name":"gram","abbreviation":"gr","extra":{"k":1}}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "ingredients_write", map[string]any{"action": "update", "kind": "units", "id": "u-1", "fields": map[string]any{"abbreviation": "gr"}})
	if got, want := resultText(t, res), `{"abbreviation":"gr","id":"u-1","name":"gram"}`; res.IsError || got != want {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, want)
	}
	seen := api.requests()
	wantBody := `{"abbreviation":"gr","description":"","id":"u-1","name":"gram","pluralName":"grams"}`
	if len(seen) != 2 || seen[1].Method != http.MethodPut || seen[1].Body != wantBody {
		t.Errorf("requests = %+v, want GET then PUT with body %s", seen, wantBody)
	}
}

func TestIngredientsWrite_UpdateEveryKind(t *testing.T) {
	tests := []struct{ kind, path, current, written, want string }{
		{"foods", "/api/foods/f-1", foodBody, `{"id":"f-1","name":"Plain Flour","pluralName":"Flours"}`, `{"id":"f-1","name":"Plain Flour","pluralName":"Flours"}`},
		{"units", "/api/units/u-1", unitBody, `{"id":"u-1","name":"gram","abbreviation":"gr"}`, `{"abbreviation":"gr","id":"u-1","name":"gram"}`},
		{"labels", "/api/groups/labels/l-1", labelBody, `{"id":"l-1","name":"Pantry","color":"#fff"}`, `{"color":"#fff","id":"l-1","name":"Pantry"}`},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, tt.path, http.StatusOK, tt.current)
			api.on(http.MethodPut, tt.path, http.StatusOK, tt.written)
			cs := connectTools(t, api)

			id := tt.path[strings.LastIndex(tt.path, "/")+1:]
			res := callTool(t, cs, "ingredients_write", map[string]any{"action": "update", "kind": tt.kind, "id": id, "fields": map[string]any{"name": "New"}})
			if got := resultText(t, res); res.IsError || got != tt.want {
				t.Errorf("result = %s (error %v), want %s", got, res.IsError, tt.want)
			}
			seen := api.requests()
			if len(seen) != 2 || seen[0].Method != http.MethodGet || seen[0].Path != tt.path || seen[1].Method != http.MethodPut || seen[1].Path != tt.path {
				t.Errorf("requests = %+v, want GET then PUT on %s", seen, tt.path)
			}
		})
	}
}

func TestIngredientsWrite_UpdateAndDeleteRejectBadIDs(t *testing.T) {
	for _, action := range []string{"update", "delete"} {
		api := newFakeMealie(t)
		cs := connectTools(t, api)

		res := callTool(t, cs, "ingredients_write", map[string]any{"action": action, "kind": "foods", "id": "a/b", "fields": map[string]any{"name": "x"}})
		requireToolError(t, res, `"a/b" is not a valid slug or id.`)
		if len(api.requests()) != 0 {
			t.Errorf("%s: requests = %+v, want none", action, api.requests())
		}
	}
}

func TestIngredientsWrite_MergePassesIDsInBody(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPut, "/api/foods/merge", http.StatusOK, `{"message":"merged"}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "ingredients_write", map[string]any{"action": "merge", "kind": "foods", "fromId": "a;b", "toId": ".."})
	if res.IsError {
		t.Fatalf("result = %q, want success", resultText(t, res))
	}
	wantBody := `{"fromFood":"a;b","toFood":".."}`
	if seen := api.requests(); len(seen) != 1 || seen[0].Path != "/api/foods/merge" || seen[0].Body != wantBody {
		t.Errorf("requests = %+v, want one PUT with body %s", seen, wantBody)
	}
}

func TestIngredientsWrite_Delete(t *testing.T) {
	tests := []struct{ kind, path string }{
		{"foods", "/api/foods/x-1"},
		{"units", "/api/units/x-1"},
		{"labels", "/api/groups/labels/x-1"},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodDelete, tt.path, http.StatusOK, `{"message":"deleted"}`)
			cs := connectTools(t, api)

			res := callTool(t, cs, "ingredients_write", map[string]any{"action": "delete", "kind": tt.kind, "id": "x-1"})
			if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
				t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
			}
			if seen := api.requests(); len(seen) != 1 || seen[0].Method != http.MethodDelete || seen[0].Path != tt.path {
				t.Errorf("requests = %+v, want one DELETE %s", seen, tt.path)
			}
		})
	}
}

func TestIngredients_MergeBodiesPerKind(t *testing.T) {
	tests := []struct{ kind, method, path, body string }{
		{"foods", http.MethodPut, "/api/foods/merge", `{"fromFood":"a","toFood":"b"}`},
		{"units", http.MethodPut, "/api/units/merge", `{"fromUnit":"a","toUnit":"b"}`},
		{"labels", http.MethodPost, "/api/groups/labels/merge", `{"fromId":"a","toId":"b"}`},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(tt.method, tt.path, http.StatusOK, `{"message":"merged"}`)
			cs := connectTools(t, api)

			res := callTool(t, cs, "ingredients_write", map[string]any{"action": "merge", "kind": tt.kind, "fromId": "a", "toId": "b"})
			if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
				t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
			}
			if seen := api.requests(); len(seen) != 1 || seen[0].Method != tt.method || seen[0].Path != tt.path || seen[0].Body != tt.body {
				t.Errorf("requests = %+v, want one %s %s with body %s", seen, tt.method, tt.path, tt.body)
			}
		})
	}
}

func TestIngredientsWrite_MergeFailures(t *testing.T) {
	tests := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"fromId": "", "toId": "b"}, `Action "merge" needs fromId.`},
		{map[string]any{"fromId": "a"}, `Action "merge" needs toId.`},
	}
	for _, tt := range tests {
		api := newFakeMealie(t)
		cs := connectTools(t, api)

		tt.args["action"], tt.args["kind"] = "merge", "foods"
		res := callTool(t, cs, "ingredients_write", tt.args)
		requireToolError(t, res, tt.want)
		if len(api.requests()) != 0 {
			t.Errorf("args %v: requests = %+v, want none", tt.args, api.requests())
		}
	}
}

func TestIngredients_KindEnum(t *testing.T) {
	cs := connectTools(t, newFakeMealie(t))

	for _, name := range []string{"ingredients_read", "ingredients_write"} {
		schema := schemaJSON(t, findTool(t, cs, name).InputSchema)
		kind := schema["properties"].(map[string]any)["kind"].(map[string]any)
		if want := []any{"foods", "units", "labels"}; !reflect.DeepEqual(kind["enum"], want) {
			t.Errorf("%s kind enum = %v, want %v", name, kind["enum"], want)
		}
		want := []any{"action", "kind"}
		if name == "ingredients_read" {
			want = []any{"action"}
		}
		if !reflect.DeepEqual(schema["required"], want) {
			t.Errorf("%s required = %v, want %v", name, schema["required"], want)
		}
	}
}
