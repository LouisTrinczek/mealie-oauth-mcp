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
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("recipeID = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
