//go:build integration

package integration

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	plannedDate    = "2026-11-02"
	plannedNote    = "round trip"
	madeItSubject  = "Made it"
	givenRating    = 4
	deniedPathText = "is not allowed"
)

type toolGroups struct {
	session    *mcp.ClientSession
	recipeName string
	tagName    string
	listName   string
	slug       string
	recipeID   string
	listID     string
}

type namedItem struct {
	ID      string `json:"id"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	Checked bool   `json:"checked"`
	Subject string `json:"subject"`
	Note    string `json:"note"`
	Display string `json:"display"`
}

type itemsResult struct {
	Items []namedItem `json:"items"`
}

type plannedEntry struct {
	ID     any       `json:"id"`
	Text   string    `json:"text"`
	Recipe namedItem `json:"recipe"`
}

type plannedEntries struct {
	Items []plannedEntry `json:"items"`
}

type userRating struct {
	RecipeID string  `json:"recipeId"`
	Rating   float64 `json:"rating"`
}

func TestToolGroups(t *testing.T) {
	suffix := uniqueSuffix()
	g := &toolGroups{
		session:    loggedInSession(t),
		recipeName: "Plan 2 Soup " + suffix,
		tagName:    "plan2-" + suffix,
		listName:   "Plan 2 " + suffix,
	}
	steps := []struct {
		name string
		run  func(*testing.T)
	}{
		{"recipes_write_create", g.createRecipe},
		{"recipes_read_search", g.searchRecipe},
		{"recipes_read_get", g.getRecipe},
		{"organizers_and_bulk_tag", g.tagRecipe},
		{"mealplans", g.planRecipe},
		{"mealplans_id_round_trip", g.updateAndDeletePlannedMeal},
		{"shopping", g.shopForRecipe},
		{"history", g.madeRecipe},
		{"ingredients_parse", g.parseIngredient},
		{"feedback_rating", g.rateRecipe},
		{"account_household", g.readHousehold},
		{"mealie_api", g.callMealieAPI},
	}
	for _, step := range steps {
		if !t.Run(step.name, step.run) {
			t.FailNow()
		}
	}
}

func TestToolGroups_RecipeUpdatePreservesUnknownFields(t *testing.T) {
	session := loggedInSession(t)
	var created struct {
		Slug string `json:"slug"`
	}
	callOK(t, session, "recipes_write", map[string]any{"action": "create", "name": "Plan 2 Unknown Fields " + uniqueSuffix()}, &created)
	callOK(t, session, "mealie_api", map[string]any{
		"method": "PATCH",
		"path":   "/api/recipes/" + created.Slug,
		"body":   map[string]any{"nutrition": map[string]any{"calories": "321"}, "orgURL": "https://example.com/x"},
	}, nil)
	callOK(t, session, "recipes_write", map[string]any{"action": "update", "slug": created.Slug, "fields": map[string]any{"description": "changed"}}, nil)
	var recipe struct {
		Description string `json:"description"`
		OrgURL      string `json:"orgURL"`
		Nutrition   struct {
			Calories string `json:"calories"`
		} `json:"nutrition"`
	}
	callOK(t, session, "recipes_read", map[string]any{"action": "get", "slug": created.Slug, "full": true}, &recipe)
	if recipe.Description != "changed" || recipe.Nutrition.Calories != "321" || recipe.OrgURL != "https://example.com/x" {
		t.Fatalf("after update: description %q, nutrition.calories %q, orgURL %q", recipe.Description, recipe.Nutrition.Calories, recipe.OrgURL)
	}
}

func (g *toolGroups) createRecipe(t *testing.T) {
	var created struct {
		Slug string `json:"slug"`
	}
	callOK(t, g.session, "recipes_write", map[string]any{"action": "create", "name": g.recipeName}, &created)
	if created.Slug == "" {
		t.Fatal("recipes_write create returned no slug")
	}
	g.slug = created.Slug
}

func (g *toolGroups) searchRecipe(t *testing.T) {
	var found itemsResult
	callOK(t, g.session, "recipes_read", map[string]any{"action": "search", "query": g.recipeName}, &found)
	if !slices.ContainsFunc(found.Items, func(item namedItem) bool { return item.Slug == g.slug }) {
		t.Fatalf("search for %q has no item with slug %q: %+v", g.recipeName, g.slug, found.Items)
	}
}

func (g *toolGroups) getRecipe(t *testing.T) {
	var recipe namedItem
	callOK(t, g.session, "recipes_read", map[string]any{"action": "get", "slug": g.slug}, &recipe)
	if recipe.Name != g.recipeName || recipe.ID == "" {
		t.Fatalf("get %q gave name %q, id %q", g.slug, recipe.Name, recipe.ID)
	}
	g.recipeID = recipe.ID
}

func (g *toolGroups) tagRecipe(t *testing.T) {
	var tag namedItem
	callOK(t, g.session, "organizers_write", map[string]any{"action": "create", "kind": "tags", "name": g.tagName}, &tag)
	callOK(t, g.session, "recipes_write", map[string]any{"action": "bulk_tag", "slugs": []string{g.slug}, "tags": []string{tag.Slug}}, nil)
	var recipe struct {
		Tags []namedItem `json:"tags"`
	}
	callOK(t, g.session, "recipes_read", map[string]any{"action": "get", "slug": g.slug}, &recipe)
	if !slices.ContainsFunc(recipe.Tags, func(item namedItem) bool { return item.Name == g.tagName }) {
		t.Fatalf("recipe %q has tags %+v, want %q", g.slug, recipe.Tags, g.tagName)
	}
}

func (g *toolGroups) planRecipe(t *testing.T) {
	callOK(t, g.session, "mealplans_write", map[string]any{"action": "create", "date": plannedDate, "slug": g.slug}, nil)
	planned := g.plannedMeals(t)
	if !slices.ContainsFunc(planned.Items, func(entry plannedEntry) bool { return entry.Recipe.Slug == g.slug }) {
		t.Fatalf("meal plan for %s has no recipe %q: %+v", plannedDate, g.slug, planned.Items)
	}
}

func (g *toolGroups) updateAndDeletePlannedMeal(t *testing.T) {
	planned := g.plannedMeals(t)
	if len(planned.Items) == 0 {
		t.Fatalf("meal plan for %s is empty", plannedDate)
	}
	id := planned.Items[0].ID
	var updated plannedEntry
	callOK(t, g.session, "mealplans_write", map[string]any{"action": "update", "id": id, "fields": map[string]any{"text": plannedNote}}, &updated)
	if updated.ID != id || updated.Text != plannedNote {
		t.Fatalf("update of meal plan entry %v returned id %v, text %q", id, updated.ID, updated.Text)
	}
	callOK(t, g.session, "mealplans_write", map[string]any{"action": "delete", "id": id}, nil)
	if slices.ContainsFunc(g.plannedMeals(t).Items, func(entry plannedEntry) bool { return entry.ID == id }) {
		t.Fatalf("meal plan entry %v is still listed after delete", id)
	}
}

func (g *toolGroups) plannedMeals(t *testing.T) plannedEntries {
	t.Helper()
	var planned plannedEntries
	callOK(t, g.session, "mealplans_read", map[string]any{"action": "list", "start": plannedDate, "end": plannedDate}, &planned)
	return planned
}

func (g *toolGroups) shopForRecipe(t *testing.T) {
	var list namedItem
	callOK(t, g.session, "shopping_write", map[string]any{"action": "create_list", "name": g.listName}, &list)
	g.listID = list.ID
	var added itemsResult
	callOK(t, g.session, "shopping_write", map[string]any{"action": "add_items", "listId": g.listID, "items": []map[string]any{{"note": "salt"}}}, &added)
	if len(added.Items) != 1 {
		t.Fatalf("add_items left %d items on the list, want 1", len(added.Items))
	}
	callOK(t, g.session, "shopping_write", map[string]any{"action": "update_items", "listId": g.listID, "items": []map[string]any{{"id": added.Items[0].ID, "checked": true}}}, nil)
	var read itemsResult
	callOK(t, g.session, "shopping_read", map[string]any{"action": "list", "id": g.listID}, &read)
	if len(read.Items) != 1 || !read.Items[0].Checked || (read.Items[0].Note != "salt" && read.Items[0].Display != "salt") {
		t.Fatalf("shopping list %s items %+v, want one checked item reading salt", g.listID, read.Items)
	}
}

func (g *toolGroups) madeRecipe(t *testing.T) {
	callOK(t, g.session, "history_write", map[string]any{"action": "made_it", "slug": g.slug}, nil)
	var timeline itemsResult
	callOK(t, g.session, "history_read", map[string]any{"action": "timeline", "slug": g.slug}, &timeline)
	if !slices.ContainsFunc(timeline.Items, func(event namedItem) bool { return event.Subject == madeItSubject }) {
		t.Fatalf("timeline of %q has no %q event: %+v", g.slug, madeItSubject, timeline.Items)
	}
}

func (g *toolGroups) parseIngredient(t *testing.T) {
	var parsed []any
	callOK(t, g.session, "ingredients_read", map[string]any{"action": "parse", "kind": "foods", "ingredients": []string{"2 cups flour"}}, &parsed)
	if len(parsed) == 0 {
		t.Fatal("parse returned no ingredients")
	}
}

func (g *toolGroups) rateRecipe(t *testing.T) {
	callOK(t, g.session, "feedback_write", map[string]any{"action": "rate", "slug": g.slug, "rating": givenRating}, nil)
	var ratings struct {
		Items []userRating `json:"items"`
	}
	callOK(t, g.session, "feedback_read", map[string]any{"action": "ratings"}, &ratings)
	if !slices.ContainsFunc(ratings.Items, func(r userRating) bool { return r.RecipeID == g.recipeID && r.Rating == givenRating }) {
		t.Fatalf("ratings %+v have no rating %d for recipe %s", ratings.Items, givenRating, g.recipeID)
	}
}

func (g *toolGroups) readHousehold(t *testing.T) {
	var household namedItem
	callOK(t, g.session, "account_read", map[string]any{"action": "household"}, &household)
	if household.Name == "" {
		t.Fatal("account_read household returned no name")
	}
}

func (g *toolGroups) callMealieAPI(t *testing.T) {
	denied, isError := callTool(t, g.session, "mealie_api", map[string]any{"method": "GET", "path": "/api/admin/about"})
	if !isError || !strings.Contains(denied, deniedPathText) {
		t.Fatalf("mealie_api GET /api/admin/about: error %t, text %s", isError, denied)
	}
	var recipe namedItem
	callOK(t, g.session, "mealie_api", map[string]any{"method": "GET", "path": "/api/recipes/" + g.slug}, &recipe)
	if recipe.Slug != g.slug {
		t.Fatalf("mealie_api GET /api/recipes/%s returned slug %q", g.slug, recipe.Slug)
	}
}

func uniqueSuffix() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func callOK(t *testing.T, session *mcp.ClientSession, name string, args map[string]any, out any) {
	t.Helper()
	text, isError := callTool(t, session, name, args)
	if isError {
		t.Fatalf("%s %v returned a tool error: %s", name, args["action"], text)
	}
	if out != nil {
		decode(t, []byte(text), out)
	}
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("calling %s: %v", name, err)
	}
	if len(result.Content) != 1 {
		t.Fatalf("%s returned %d content blocks, want 1", name, len(result.Content))
	}
	text, isText := result.Content[0].(*mcp.TextContent)
	if !isText {
		t.Fatalf("%s returned %T, want text", name, result.Content[0])
	}
	return text.Text, result.IsError
}
