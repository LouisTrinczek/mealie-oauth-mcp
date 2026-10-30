package mcpserver

import (
	"context"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

const (
	recipesPageBody = `{"page":1,"per_page":100,"total":1,"total_pages":1,"items":[{"id":"r-1","slug":"tomato-soup","name":"Tomato Soup","description":"Quick and easy","totalTime":"30 min","rating":4,"recipeCategory":[{"id":"c-1","slug":"soup","name":"Soup"}],"tags":[{"id":"t-1","slug":"quick","name":"Quick"}],"recipeIngredient":[{"note":"tomatoes"}],"userId":"u-1","image":"abc"}],"next":null,"previous":null}`
	recipeBody      = `{"id":"r-1","slug":"tomato-soup","name":"Tomato Soup","description":"","rating":4,"recipeCategory":[{"id":"c-1","slug":"soup","name":"Soup"}],"recipeIngredient":[{"note":"2 tomatoes","referenceId":"x"}],"recipeInstructions":[{"id":"i-1","text":"Boil."}],"userId":"u-1","groupId":"g-1","householdId":"h-1","extras":{},"dateAdded":"2026-01-01"}`
	suggestionsBody = `{"items":[{"recipe":{"id":"r-1","slug":"tomato-soup","name":"Tomato Soup","recipeCategory":[],"userId":"u-1"},"missingFoods":[{"id":"f-1","name":"Basil","pluralName":null}],"missingTools":[]},{"recipe":{"id":"r-2","slug":"salad","name":"Salad"},"missingFoods":[],"missingTools":[{"id":"t-1","name":"Whisk","slug":"whisk"}]}]}`
)

func TestRecipesRead_SearchQuery(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes", http.StatusOK, recipesPageBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "recipes_read", map[string]any{
		"action": "search", "query": "soup", "tags": []string{"quick", "vegan"},
		"requireAll": true, "perPage": 500, "orderBy": "name", "orderDirection": "desc",
	})
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	assertGolden(t, "recipes_search", resultText(t, res))
	want := url.Values{
		"search": {"soup"}, "tags": {"quick", "vegan"}, "orderBy": {"name"}, "orderDirection": {"desc"},
		"page": {"1"}, "perPage": {"100"},
		"requireAllCategories": {"true"}, "requireAllTags": {"true"}, "requireAllTools": {"true"}, "requireAllFoods": {"true"},
	}
	if seen := api.requests(); len(seen) != 1 || !reflect.DeepEqual(seen[0].Query, want) {
		t.Errorf("requests = %+v, want one GET with query %v", seen, want)
	}
}

func TestRecipesRead_SearchSendsOnlyGivenFilters(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes", http.StatusOK, recipesPageBody)
	cs := connectTools(t, api)

	callTool(t, cs, "recipes_read", map[string]any{"action": "search", "categories": []string{"soup"}})
	want := url.Values{"categories": {"soup"}, "page": {"1"}, "perPage": {"20"}}
	if seen := api.requests(); len(seen) != 1 || !reflect.DeepEqual(seen[0].Query, want) {
		t.Errorf("requests = %+v, want query %v", seen, want)
	}
}

func TestRecipesRead_RejectsUnknownOrderDirection(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res := callTool(t, cs, "recipes_read", map[string]any{"action": "search", "orderDirection": "sideways"})
	if !res.IsError {
		t.Error("IsError = false, want a validation error")
	}
	if seen := api.requests(); len(seen) != 0 {
		t.Errorf("requests = %+v, want none", seen)
	}
}

func TestRecipesRead_GetTrimmedAndFull(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/tomato-soup", http.StatusOK, recipeBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "recipes_read", map[string]any{"action": "get", "slug": "tomato-soup"})
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	assertGolden(t, "recipes_get", resultText(t, res))

	full := callTool(t, cs, "recipes_read", map[string]any{"action": "get", "slug": "tomato-soup", "full": true})
	if full.IsError {
		t.Fatalf("tool error: %s", resultText(t, full))
	}
	want := recipeBody
	if got := resultText(t, full); got != want {
		t.Errorf("full = %s, want %s", got, want)
	}
}

func TestRecipesRead_GetNeedsSlug(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	requireToolError(t, callTool(t, cs, "recipes_read", map[string]any{"action": "get"}), `Action "get" needs slug.`)
}

func TestRecipesRead_BadSlugMakesNoRequest(t *testing.T) {
	for _, slug := range []string{"../admin", "a/b", "%2e%2e"} {
		t.Run(slug, func(t *testing.T) {
			api := newFakeMealie(t)
			cs := connectTools(t, api)

			requireToolError(t, callTool(t, cs, "recipes_read", map[string]any{"action": "get", "slug": slug}),
				`"`+slug+`" is not a valid slug or id.`)
			if seen := api.requests(); len(seen) != 0 {
				t.Errorf("requests = %+v, want none", seen)
			}
		})
	}
}

func TestRecipesRead_Suggest(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		want  string
	}{
		{"DefaultsToTen", 0, "10"},
		{"CapsAtFifty", 99, "50"},
		{"KeepsValidLimit", 5, "5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, "/api/recipes/suggestions", http.StatusOK, suggestionsBody)
			cs := connectTools(t, api)

			res := callTool(t, cs, "recipes_read", map[string]any{
				"action": "suggest", "foods": []string{"tomato", "onion"}, "tools": []string{"pot"}, "limit": tt.limit,
			})
			if res.IsError {
				t.Fatalf("tool error: %s", resultText(t, res))
			}
			assertGolden(t, "recipes_suggest", resultText(t, res))
			want := url.Values{"foods": {"tomato", "onion"}, "tools": {"pot"}, "limit": {tt.want}}
			if seen := api.requests(); len(seen) != 1 || !reflect.DeepEqual(seen[0].Query, want) {
				t.Errorf("requests = %+v, want query %v", seen, want)
			}
		})
	}
}

func TestRecipesRead_ReportsMealieAndDecodeFailures(t *testing.T) {
	tests := []struct {
		name   string
		args   map[string]any
		path   string
		status int
		body   string
		want   string
	}{
		{"SearchForbidden", map[string]any{"action": "search"}, "/api/recipes", http.StatusForbidden, `{"detail":"no"}`, "Your Mealie user is not allowed to do this (403): no."},
		{"GetNotFound", map[string]any{"action": "get", "slug": "nope"}, "/api/recipes/nope", http.StatusNotFound, `{"detail":"gone"}`, "Mealie could not find that (404): gone. Check the slug or id; search or list to find it."},
		{"SuggestForbidden", map[string]any{"action": "suggest"}, "/api/recipes/suggestions", http.StatusForbidden, `{"detail":"no"}`, "Your Mealie user is not allowed to do this (403): no."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, tt.path, tt.status, tt.body)
			cs := connectTools(t, api)

			requireToolError(t, callTool(t, cs, "recipes_read", tt.args), tt.want)
		})
	}
}

func TestRecipesRead_SuggestRejectsUnexpectedShape(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/suggestions", http.StatusOK, `[]`)
	cs := connectTools(t, api)

	if res := callTool(t, cs, "recipes_read", map[string]any{"action": "suggest"}); !res.IsError {
		t.Error("IsError = false, want a tool error")
	}
}

func TestRecipeID(t *testing.T) {
	tests := []struct {
		name    string
		slug    string
		status  int
		body    string
		want    string
		wantErr string
	}{
		{"ReturnsID", "tomato-soup", http.StatusOK, recipeBody, "r-1", ""},
		{"BadSlug", "a/b", http.StatusOK, "", "", `"a/b" is not a valid slug or id.`},
		{"MealieError", "nope", http.StatusNotFound, `{"detail":"gone"}`, "", "Mealie could not find that (404): gone. Check the slug or id; search or list to find it."},
		{"EmptyObject", "empty", http.StatusOK, `{}`, "", `Mealie returned recipe "empty" without an id.`},
		{"MissingID", "x", http.StatusOK, `{"slug":"x"}`, "", `Mealie returned recipe "x" without an id.`},
		{"UnexpectedShape", "odd", http.StatusOK, `[]`, "", `decoding recipe "odd"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, "/api/recipes/"+tt.slug, tt.status, tt.body)
			s := &session{tokens: &fakeTokens{}, mealie: api.client(t)}

			got, err := recipeID(context.Background(), s, tt.slug)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %v, want containing %q", err, tt.wantErr)
				}
				if got != "" {
					t.Errorf("id = %q, want empty on error", got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("recipeID = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestRecipesWrite_UpdateSendsOnlyGivenFields(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPatch, "/api/recipes/tomato-soup", http.StatusOK, recipeBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "recipes_write", map[string]any{"action": "update", "slug": "tomato-soup", "fields": map[string]any{"description": "New"}})
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	want := []seenRequest{{Method: http.MethodPatch, Path: "/api/recipes/tomato-soup", Query: url.Values{}, Body: `{"description":"New"}`}}
	if seen := api.requests(); !reflect.DeepEqual(seen, want) {
		t.Errorf("requests = %+v, want %+v", seen, want)
	}
	if strings.Contains(resultText(t, res), "userId") {
		t.Errorf("result %s is not trimmed", resultText(t, res))
	}
}

func TestRecipesWrite_DeleteReportsSuccess(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodDelete, "/api/recipes/tomato-soup", http.StatusOK, "")
	cs := connectTools(t, api)

	res := callTool(t, cs, "recipes_write", map[string]any{"action": "delete", "slug": "tomato-soup"})
	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
	}
}

func TestRecipesWrite_CreateAndImportReturnSlug(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		path string
		body string
	}{
		{"Create", map[string]any{"action": "create", "name": "Tomato Soup"}, "/api/recipes", `{"name":"Tomato Soup"}`},
		{"Import", map[string]any{"action": "import_url", "url": "https://example.com/r", "includeTags": true}, "/api/recipes/create/url", `{"includeTags":true,"url":"https://example.com/r"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodPost, tt.path, http.StatusCreated, `"tomato-soup"`)
			cs := connectTools(t, api)

			res := callTool(t, cs, "recipes_write", tt.args)
			if got := resultText(t, res); res.IsError || got != `{"slug":"tomato-soup"}` {
				t.Errorf("result = %s (error %v)", got, res.IsError)
			}
			want := []seenRequest{{Method: http.MethodPost, Path: tt.path, Query: url.Values{}, Body: tt.body}}
			if seen := api.requests(); !reflect.DeepEqual(seen, want) {
				t.Errorf("requests = %+v, want %+v", seen, want)
			}
		})
	}
}

func TestRecipesWrite_CreateRejectsNonStringBody(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, "/api/recipes", http.StatusCreated, `{"slug":"x"}`)
	cs := connectTools(t, api)

	if res := callTool(t, cs, "recipes_write", map[string]any{"action": "create", "name": "X"}); !res.IsError {
		t.Error("IsError = false, want a tool error")
	}
}

func TestRecipesWrite_DuplicateReturnsCompactRecipe(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, "/api/recipes/tomato-soup/duplicate", http.StatusCreated, recipeBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "recipes_write", map[string]any{"action": "duplicate", "slug": "tomato-soup", "name": "Copy"})
	want := `{"id":"r-1","name":"Tomato Soup","rating":4,"recipeCategory":["Soup"],"slug":"tomato-soup"}`
	if got := resultText(t, res); res.IsError || got != want {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, want)
	}
	if seen := api.requests(); len(seen) != 1 || seen[0].Body != `{"name":"Copy"}` {
		t.Errorf("requests = %+v, want one POST with the name", seen)
	}
}

func TestRecipesWrite_DuplicateWithoutNameSendsEmptyObject(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, "/api/recipes/tomato-soup/duplicate", http.StatusCreated, recipeBody)
	cs := connectTools(t, api)

	callTool(t, cs, "recipes_write", map[string]any{"action": "duplicate", "slug": "tomato-soup"})
	if seen := api.requests(); len(seen) != 1 || seen[0].Body != `{}` {
		t.Errorf("requests = %+v, want one POST with {}", seen)
	}
}

func TestRecipesWrite_DuplicateRejectsUnexpectedShape(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, "/api/recipes/tomato-soup/duplicate", http.StatusCreated, `[]`)
	cs := connectTools(t, api)

	if res := callTool(t, cs, "recipes_write", map[string]any{"action": "duplicate", "slug": "tomato-soup"}); !res.IsError {
		t.Error("IsError = false, want a tool error")
	}
}

func TestRecipesWrite_MarkMadeDefaultsToClock(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPatch, "/api/recipes/tomato-soup/last-made", http.StatusOK, "")
	cs := connectTools(t, api)

	res := callTool(t, cs, "recipes_write", map[string]any{"action": "mark_made", "slug": "tomato-soup"})
	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %s (error %v)", got, res.IsError)
	}
	if seen := api.requests(); len(seen) != 1 || seen[0].Body != `{"timestamp":"2026-10-30T12:00:00Z"}` {
		t.Errorf("requests = %+v, want the clock timestamp", seen)
	}
}

func TestRecipesWrite_MarkMadeUsesGivenTimestamp(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPatch, "/api/recipes/tomato-soup/last-made", http.StatusOK, "")
	cs := connectTools(t, api)

	callTool(t, cs, "recipes_write", map[string]any{"action": "mark_made", "slug": "tomato-soup", "timestamp": "2026-01-02T03:04:05Z"})
	if seen := api.requests(); len(seen) != 1 || seen[0].Body != `{"timestamp":"2026-01-02T03:04:05Z"}` {
		t.Errorf("requests = %+v, want the given timestamp", seen)
	}
}

func TestRecipesWrite_ImageAndAssetUrls(t *testing.T) {
	tests := []struct {
		name   string
		action string
		path   string
		body   string
		want   string
	}{
		{"Image", "set_image_url", "/api/recipes/tomato-soup/image", "", `{"ok":true}`},
		{"Asset", "add_asset_url", "/api/recipes/tomato-soup/assets/url", `{"name":"a","userId":"u"}`, `{"name":"a"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodPost, tt.path, http.StatusOK, tt.body)
			cs := connectTools(t, api)

			res := callTool(t, cs, "recipes_write", map[string]any{"action": tt.action, "slug": "tomato-soup", "url": "https://example.com/p.jpg"})
			if got := resultText(t, res); res.IsError || got != tt.want {
				t.Errorf("result = %s (error %v), want %s", got, res.IsError, tt.want)
			}
			if seen := api.requests(); len(seen) != 1 || seen[0].Body != `{"url":"https://example.com/p.jpg"}` {
				t.Errorf("requests = %+v, want the url body", seen)
			}
		})
	}
}

func TestRecipesWrite_ShareResolvesRecipeID(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/tomato-soup", http.StatusOK, recipeBody)
	api.on(http.MethodPost, "/api/shared/recipes", http.StatusCreated, `{"recipeId":"r-1","expiresAt":"2026-11-01T00:00:00Z","groupId":"g-1","id":"s-1","createdAt":"x","recipe":{"id":"r-1"}}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "recipes_write", map[string]any{"action": "share", "slug": "tomato-soup", "expiresAt": "2026-11-01T00:00:00Z"})
	if got, want := resultText(t, res), `{"expiresAt":"2026-11-01T00:00:00Z","id":"s-1"}`; res.IsError || got != want {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, want)
	}
	seen := api.requests()
	if len(seen) != 2 || seen[1].Body != `{"expiresAt":"2026-11-01T00:00:00Z","recipeId":"r-1"}` {
		t.Errorf("requests = %+v, want a GET then a POST with recipeId and expiresAt", seen)
	}
}

func TestRecipesWrite_ShareWithoutExpiry(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/tomato-soup", http.StatusOK, recipeBody)
	api.on(http.MethodPost, "/api/shared/recipes", http.StatusCreated, `{"id":"s-1"}`)
	cs := connectTools(t, api)

	callTool(t, cs, "recipes_write", map[string]any{"action": "share", "slug": "tomato-soup"})
	if seen := api.requests(); len(seen) != 2 || seen[1].Body != `{"recipeId":"r-1"}` {
		t.Errorf("requests = %+v, want a POST with only recipeId", seen)
	}
}

func TestRecipesWrite_ShareFailures(t *testing.T) {
	tests := []struct {
		name  string
		setup func(api *fakeAPI)
	}{
		{"RecipeLookupFails", func(api *fakeAPI) {
			api.on(http.MethodGet, "/api/recipes/tomato-soup", http.StatusNotFound, `{"detail":"gone"}`)
		}},
		{"ShareFails", func(api *fakeAPI) {
			api.on(http.MethodGet, "/api/recipes/tomato-soup", http.StatusOK, recipeBody)
			api.on(http.MethodPost, "/api/shared/recipes", http.StatusForbidden, `{"detail":"no"}`)
		}},
		{"ShareBodyUnexpected", func(api *fakeAPI) {
			api.on(http.MethodGet, "/api/recipes/tomato-soup", http.StatusOK, recipeBody)
			api.on(http.MethodPost, "/api/shared/recipes", http.StatusCreated, `[]`)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			tt.setup(api)
			cs := connectTools(t, api)

			if res := callTool(t, cs, "recipes_write", map[string]any{"action": "share", "slug": "tomato-soup"}); !res.IsError {
				t.Error("IsError = false, want a tool error")
			}
		})
	}
}

func TestRecipesWrite_BulkTagResolvesSlugs(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/organizers/tags/slug/quick", http.StatusOK, `{"id":"t-1","name":"Quick","slug":"quick","groupId":"g-1"}`)
	api.on(http.MethodGet, "/api/organizers/tags/slug/vegan", http.StatusOK, `{"id":"t-2","name":"Vegan","slug":"vegan","groupId":"g-1"}`)
	api.on(http.MethodPost, "/api/recipes/bulk-actions/tag", http.StatusOK, "")
	cs := connectTools(t, api)

	res := callTool(t, cs, "recipes_write", map[string]any{"action": "bulk_tag", "slugs": []string{"a", "b"}, "tags": []string{"quick", "vegan"}})
	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %s (error %v)", got, res.IsError)
	}
	seen := api.requests()
	if len(seen) != 3 || seen[0].Path != "/api/organizers/tags/slug/quick" || seen[1].Path != "/api/organizers/tags/slug/vegan" {
		t.Fatalf("requests = %+v, want two lookups then the bulk POST", seen)
	}
	want := `{"recipes":["a","b"],"tags":[{"id":"t-1","name":"Quick","slug":"quick"},{"id":"t-2","name":"Vegan","slug":"vegan"}]}`
	if seen[2].Method != http.MethodPost || seen[2].Path != "/api/recipes/bulk-actions/tag" || seen[2].Body != want {
		t.Errorf("bulk request = %+v, want body %s", seen[2], want)
	}
}

func TestRecipesWrite_BulkCategorizeResolvesSlugs(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/organizers/categories/slug/soup", http.StatusOK, `{"id":"c-1","name":"Soup","slug":"soup","groupId":"g-1"}`)
	api.on(http.MethodPost, "/api/recipes/bulk-actions/categorize", http.StatusOK, "")
	cs := connectTools(t, api)

	callTool(t, cs, "recipes_write", map[string]any{"action": "bulk_categorize", "slugs": []string{"a"}, "categories": []string{"soup"}})
	seen := api.requests()
	want := `{"categories":[{"id":"c-1","name":"Soup","slug":"soup"}],"recipes":["a"]}`
	if len(seen) != 2 || seen[1].Path != "/api/recipes/bulk-actions/categorize" || seen[1].Body != want {
		t.Errorf("requests = %+v, want a lookup then a bulk POST with %s", seen, want)
	}
}

func TestRecipesWrite_BulkTagUnknownTagStops(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/organizers/tags/slug/quick", http.StatusOK, `{"id":"t-1","name":"Quick","slug":"quick"}`)
	api.on(http.MethodGet, "/api/organizers/tags/slug/nope", http.StatusNotFound, `{"detail":"Tag nope not found"}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "recipes_write", map[string]any{"action": "bulk_tag", "slugs": []string{"a"}, "tags": []string{"quick", "nope"}})
	requireToolError(t, res, "Mealie could not find that (404): Tag nope not found. Check the slug or id; search or list to find it.")
	for _, seen := range api.requests() {
		if seen.Method == http.MethodPost {
			t.Errorf("unexpected bulk request %+v", seen)
		}
	}
}

func TestRecipesWrite_BulkDelete(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, "/api/recipes/bulk-actions/delete", http.StatusOK, "")
	cs := connectTools(t, api)

	res := callTool(t, cs, "recipes_write", map[string]any{"action": "bulk_delete", "slugs": []string{"a", "b"}})
	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %s (error %v)", got, res.IsError)
	}
	if seen := api.requests(); len(seen) != 1 || seen[0].Body != `{"recipes":["a","b"]}` {
		t.Errorf("requests = %+v, want one bulk delete", seen)
	}
}

func TestRecipesWrite_BadSlugsMakeNoRequest(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
	}{
		{"BulkDelete", map[string]any{"action": "bulk_delete", "slugs": []string{"a", "../admin"}}},
		{"BulkTag", map[string]any{"action": "bulk_tag", "slugs": []string{"a/b"}, "tags": []string{"quick"}}},
		{"BulkTagTag", map[string]any{"action": "bulk_tag", "slugs": []string{"a"}, "tags": []string{"%2e%2e"}}},
		{"Update", map[string]any{"action": "update", "slug": "a/b", "fields": map[string]any{"x": 1}}},
		{"Delete", map[string]any{"action": "delete", "slug": ".."}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			cs := connectTools(t, api)

			res := callTool(t, cs, "recipes_write", tt.args)
			if !res.IsError || !strings.HasSuffix(resultText(t, res), `is not a valid slug or id.`) {
				t.Errorf("result = %s (error %v), want a bad-slug error", resultText(t, res), res.IsError)
			}
			if seen := api.requests(); len(seen) != 0 {
				t.Errorf("requests = %+v, want none", seen)
			}
		})
	}
}

func TestRecipesWrite_MissingArguments(t *testing.T) {
	tests := []struct {
		action string
		args   map[string]any
		want   string
	}{
		{"create", nil, "name"},
		{"import_url", nil, "url"},
		{"update", nil, "slug"},
		{"update", map[string]any{"slug": "a"}, "fields"},
		{"update", map[string]any{"slug": "a", "fields": map[string]any{}}, "fields"},
		{"duplicate", nil, "slug"},
		{"delete", nil, "slug"},
		{"mark_made", nil, "slug"},
		{"set_image_url", nil, "slug"},
		{"set_image_url", map[string]any{"slug": "a"}, "url"},
		{"add_asset_url", nil, "slug"},
		{"add_asset_url", map[string]any{"slug": "a"}, "url"},
		{"share", nil, "slug"},
		{"bulk_tag", nil, "slugs"},
		{"bulk_tag", map[string]any{"slugs": []string{"a"}}, "tags"},
		{"bulk_categorize", nil, "slugs"},
		{"bulk_categorize", map[string]any{"slugs": []string{"a"}}, "categories"},
		{"bulk_delete", nil, "slugs"},
	}
	for _, tt := range tests {
		t.Run(tt.action+"_"+tt.want, func(t *testing.T) {
			api := newFakeMealie(t)
			cs := connectTools(t, api)
			args := map[string]any{"action": tt.action}
			for key, value := range tt.args {
				args[key] = value
			}

			requireToolError(t, callTool(t, cs, "recipes_write", args), `Action "`+tt.action+`" needs `+tt.want+`.`)
			if seen := api.requests(); len(seen) != 0 {
				t.Errorf("requests = %+v, want none", seen)
			}
		})
	}
}

func TestRecipesWrite_Annotations(t *testing.T) {
	cs := connectTools(t, newFakeMealie(t))

	a := findTool(t, cs, "recipes_write").Annotations
	if a == nil || a.DestructiveHint == nil || !*a.DestructiveHint || a.OpenWorldHint == nil || !*a.OpenWorldHint {
		t.Errorf("annotations = %+v, want destructive and open world", a)
	}
}

func TestRecipesWrite_ReportsMealieFailures(t *testing.T) {
	forbidden := "Your Mealie user is not allowed to do this (403): no."
	tests := []struct {
		name   string
		args   map[string]any
		method string
		path   string
		status int
		body   string
		want   string
	}{
		{"Create", map[string]any{"action": "create", "name": "X"}, http.MethodPost, "/api/recipes", http.StatusForbidden, `{"detail":"no"}`, forbidden},
		{"Import", map[string]any{"action": "import_url", "url": "https://e.com"}, http.MethodPost, "/api/recipes/create/url", http.StatusForbidden, `{"detail":"no"}`, forbidden},
		{"Update", map[string]any{"action": "update", "slug": "a", "fields": map[string]any{"x": 1}}, http.MethodPatch, "/api/recipes/a", http.StatusForbidden, `{"detail":"no"}`, forbidden},
		{"Duplicate", map[string]any{"action": "duplicate", "slug": "a"}, http.MethodPost, "/api/recipes/a/duplicate", http.StatusForbidden, `{"detail":"no"}`, forbidden},
		{"Delete", map[string]any{"action": "delete", "slug": "a"}, http.MethodDelete, "/api/recipes/a", http.StatusForbidden, `{"detail":"no"}`, forbidden},
		{"MarkMade", map[string]any{"action": "mark_made", "slug": "a"}, http.MethodPatch, "/api/recipes/a/last-made", http.StatusForbidden, `{"detail":"no"}`, forbidden},
		{"SetImage", map[string]any{"action": "set_image_url", "slug": "a", "url": "https://e.com"}, http.MethodPost, "/api/recipes/a/image", http.StatusForbidden, `{"detail":"no"}`, forbidden},
		{"BulkTagPost", map[string]any{"action": "bulk_tag", "slugs": []string{"a"}, "tags": []string{"q"}}, http.MethodPost, "/api/recipes/bulk-actions/tag", http.StatusForbidden, `{"detail":"no"}`, forbidden},
		{"BulkDelete", map[string]any{"action": "bulk_delete", "slugs": []string{"a"}}, http.MethodPost, "/api/recipes/bulk-actions/delete", http.StatusForbidden, `{"detail":"no"}`, forbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, "/api/organizers/tags/slug/q", http.StatusOK, `{"id":"t-1","name":"Q","slug":"q"}`)
			api.on(tt.method, tt.path, tt.status, tt.body)
			cs := connectTools(t, api)

			requireToolError(t, callTool(t, cs, "recipes_write", tt.args), tt.want)
		})
	}
}

func TestRecipesWrite_BulkTagRejectsUnexpectedOrganizer(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/organizers/tags/slug/q", http.StatusOK, `[]`)
	cs := connectTools(t, api)

	if res := callTool(t, cs, "recipes_write", map[string]any{"action": "bulk_tag", "slugs": []string{"a"}, "tags": []string{"q"}}); !res.IsError {
		t.Error("IsError = false, want a tool error")
	}
	if seen := api.requests(); len(seen) != 1 {
		t.Errorf("requests = %+v, want only the lookup", seen)
	}
}
