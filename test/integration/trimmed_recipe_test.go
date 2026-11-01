//go:build integration

package integration

import (
	"crypto/rand"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	parsedOriginalText   = "2 of it"
	unparsedOriginalText = "a pinch of salt"
	changedOriginalText  = "changed by the agent"
)

type stepReference struct {
	ReferenceID string `json:"referenceId"`
}

type linkedStep struct {
	IngredientReferences []stepReference `json:"ingredientReferences"`
}

type storedRecipe struct {
	Ingredients []map[string]any `json:"recipeIngredient"`
	Steps       []linkedStep     `json:"recipeInstructions"`
}

func TestTrimmedRecipeUpdateLosesNothing(t *testing.T) {
	session := loggedInSession(t)
	slug, food, unit := recipeWithLinkedIngredients(t, session, "Trim Round Trip "+uniqueSuffix())
	before := storedRecipeOf(t, session, slug)
	parsed := before.Ingredients[0]
	if parsed["food"].(map[string]any)["id"] != food["id"] || parsed["unit"].(map[string]any)["id"] != unit["id"] || parsed["originalText"] != parsedOriginalText {
		t.Fatalf("setup: parsed ingredient %v is not linked to food %v and unit %v", parsed, food["id"], unit["id"])
	}

	ingredients := trimmedIngredients(t, session, slug)
	if _, present := ingredients[0]["originalText"]; present {
		t.Fatalf("trimmed parsed ingredient %v still has originalText", ingredients[0])
	}
	callOK(t, session, "recipes_write", map[string]any{"action": "update", "slug": slug, "fields": map[string]any{"recipeIngredient": ingredients}}, nil)

	if after := storedRecipeOf(t, session, slug); !reflect.DeepEqual(after, before) {
		t.Fatalf("recipe after update = %+v, want %+v", after, before)
	}
}

func TestTrimmedRecipeUpdateKeepsAgentChanges(t *testing.T) {
	session := loggedInSession(t)
	slug, _, _ := recipeWithLinkedIngredients(t, session, "Trim Agent Changes "+uniqueSuffix())

	ingredients := trimmedIngredients(t, session, slug)
	ingredients[1]["originalText"] = changedOriginalText
	ingredients = append(ingredients, map[string]any{"note": "pepper"})
	callOK(t, session, "recipes_write", map[string]any{"action": "update", "slug": slug, "fields": map[string]any{"recipeIngredient": ingredients}}, nil)

	after := storedRecipeOf(t, session, slug)
	got := make([]any, 0, len(after.Ingredients))
	for _, ingredient := range after.Ingredients {
		got = append(got, ingredient["originalText"])
	}
	if want := []any{parsedOriginalText, changedOriginalText, nil}; !reflect.DeepEqual(got, want) {
		t.Fatalf("originalText after update = %v, want %v", got, want)
	}
}

func fullIngredient(t *testing.T, session *mcp.ClientSession, kind, name string) map[string]any {
	t.Helper()
	var created namedItem
	callOK(t, session, "ingredients_write", map[string]any{"action": "create", "kind": kind, "name": name}, &created)
	var full map[string]any
	callOK(t, session, "ingredients_read", map[string]any{"action": "get", "kind": kind, "id": created.ID, "full": true}, &full)
	return full
}

func recipeWithLinkedIngredients(t *testing.T, session *mcp.ClientSession, name string) (string, map[string]any, map[string]any) {
	t.Helper()
	food := fullIngredient(t, session, "foods", name+" food")
	unit := fullIngredient(t, session, "units", name+" unit")
	var created namedItem
	callOK(t, session, "recipes_write", map[string]any{"action": "create", "name": name}, &created)
	parsedID, unparsedID := newUUID(), newUUID()
	callOK(t, session, "mealie_api", map[string]any{
		"method": "PATCH",
		"path":   "/api/recipes/" + created.Slug,
		"body": map[string]any{
			"recipeIngredient": []any{
				map[string]any{"referenceId": parsedID, "quantity": 2, "food": food, "unit": unit, "note": "chopped", "originalText": parsedOriginalText},
				map[string]any{"referenceId": unparsedID, "note": "salt to taste", "originalText": unparsedOriginalText},
			},
			"recipeInstructions": []any{
				map[string]any{"id": newUUID(), "text": "Mix.", "ingredientReferences": []any{map[string]any{"referenceId": parsedID}}},
				map[string]any{"id": newUUID(), "text": "Season.", "ingredientReferences": []any{map[string]any{"referenceId": unparsedID}}},
			},
		},
	}, nil)
	return created.Slug, food, unit
}

func trimmedIngredients(t *testing.T, session *mcp.ClientSession, slug string) []map[string]any {
	t.Helper()
	var recipe struct {
		Ingredients []map[string]any `json:"recipeIngredient"`
	}
	callOK(t, session, "recipes_read", map[string]any{"action": "get", "slug": slug}, &recipe)
	for _, field := range []string{"food", "unit"} {
		reference, _ := recipe.Ingredients[0][field].(map[string]any)
		if keys := slices.Sorted(maps.Keys(reference)); !slices.Equal(keys, []string{"id", "name"}) {
			t.Fatalf("trimmed %s has keys %v, want [id name]", field, keys)
		}
	}
	return recipe.Ingredients
}

func storedRecipeOf(t *testing.T, session *mcp.ClientSession, slug string) storedRecipe {
	t.Helper()
	var recipe storedRecipe
	callOK(t, session, "recipes_read", map[string]any{"action": "get", "slug": slug, "full": true}, &recipe)
	if len(recipe.Ingredients) < 2 {
		t.Fatalf("recipe %s has %d ingredients, want at least 2", slug, len(recipe.Ingredients))
	}
	return recipe
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
