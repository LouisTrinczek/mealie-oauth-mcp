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

type stepReference struct {
	ReferenceID string `json:"referenceId"`
}

type linkedIngredient struct {
	ReferenceID string    `json:"referenceId"`
	Food        namedItem `json:"food"`
	Unit        namedItem `json:"unit"`
}

type linkedStep struct {
	IngredientReferences []stepReference `json:"ingredientReferences"`
}

type recipeLinks struct {
	Ingredients []linkedIngredient `json:"recipeIngredient"`
	Steps       []linkedStep       `json:"recipeInstructions"`
}

func TestTrimmedRecipeUpdateKeepsLinks(t *testing.T) {
	session := loggedInSession(t)
	suffix := uniqueSuffix()
	food := fullIngredient(t, session, "foods", "Trim Food "+suffix)
	unit := fullIngredient(t, session, "units", "Trim Unit "+suffix)
	slug := recipeWithLinkedIngredient(t, session, "Trim Round Trip "+suffix, food, unit)
	before := linksOf(t, session, slug)
	if len(before.Ingredients) != 1 || before.Ingredients[0].Food.ID != food["id"] || before.Ingredients[0].Unit.ID != unit["id"] {
		t.Fatalf("setup: ingredients %+v are not linked to food %v and unit %v", before.Ingredients, food["id"], unit["id"])
	}

	ingredients := trimmedIngredients(t, session, slug)
	callOK(t, session, "recipes_write", map[string]any{"action": "update", "slug": slug, "fields": map[string]any{"recipeIngredient": ingredients}}, nil)

	if after := linksOf(t, session, slug); !reflect.DeepEqual(after, before) {
		t.Fatalf("links after update = %+v, want %+v", after, before)
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

func recipeWithLinkedIngredient(t *testing.T, session *mcp.ClientSession, name string, food, unit map[string]any) string {
	t.Helper()
	var created namedItem
	callOK(t, session, "recipes_write", map[string]any{"action": "create", "name": name}, &created)
	referenceID := newUUID()
	callOK(t, session, "mealie_api", map[string]any{
		"method": "PATCH",
		"path":   "/api/recipes/" + created.Slug,
		"body": map[string]any{
			"recipeIngredient":   []any{map[string]any{"referenceId": referenceID, "quantity": 2, "food": food, "unit": unit, "originalText": "2 of it"}},
			"recipeInstructions": []any{map[string]any{"id": newUUID(), "text": "Mix.", "ingredientReferences": []any{map[string]any{"referenceId": referenceID}}}},
		},
	}, nil)
	return created.Slug
}

func trimmedIngredients(t *testing.T, session *mcp.ClientSession, slug string) []map[string]any {
	t.Helper()
	var recipe struct {
		Ingredients []map[string]any `json:"recipeIngredient"`
	}
	callOK(t, session, "recipes_read", map[string]any{"action": "get", "slug": slug}, &recipe)
	for _, ingredient := range recipe.Ingredients {
		for _, field := range []string{"food", "unit"} {
			reference, _ := ingredient[field].(map[string]any)
			if keys := slices.Sorted(maps.Keys(reference)); !slices.Equal(keys, []string{"id", "name"}) {
				t.Fatalf("trimmed %s has keys %v, want [id name]", field, keys)
			}
		}
	}
	return recipe.Ingredients
}

func linksOf(t *testing.T, session *mcp.ClientSession, slug string) recipeLinks {
	t.Helper()
	var links recipeLinks
	callOK(t, session, "recipes_read", map[string]any{"action": "get", "slug": slug, "full": true}, &links)
	return links
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
