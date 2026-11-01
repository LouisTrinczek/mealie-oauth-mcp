package view

import (
	"encoding/json"
	"slices"
)

var (
	organizerFields      = []string{"tags", "recipeCategory", "tools"}
	organizerKeys        = []string{"id", "name", "slug"}
	ingredientObjectKeys = []string{"id", "name"}
)

func TrimRecipe(raw json.RawMessage) (json.RawMessage, error) {
	value, err := decodeValue(raw)
	if err != nil {
		return nil, err
	}
	recipe := trimmed(value)
	if object, ok := recipe.(map[string]any); ok {
		reduceRecipe(object)
	}
	return marshal(recipe)
}

func reduceRecipe(recipe map[string]any) {
	for _, field := range organizerFields {
		for _, organizer := range objects(recipe[field]) {
			keepOnly(organizer, organizerKeys)
		}
	}
	for _, ingredient := range objects(recipe["recipeIngredient"]) {
		reduceIngredient(ingredient)
	}
}

func reduceIngredient(ingredient map[string]any) {
	keepOnlyIn(ingredient, "food", ingredientObjectKeys)
	keepOnlyIn(ingredient, "unit", ingredientObjectKeys)
	for _, substitution := range objects(ingredient["substitutions"]) {
		keepOnlyIn(substitution, "substituteFood", ingredientObjectKeys)
	}
}

func keepOnlyIn(parent map[string]any, field string, keys []string) {
	if object, ok := parent[field].(map[string]any); ok {
		keepOnly(object, keys)
	}
}

func keepOnly(object map[string]any, keys []string) {
	for key := range object {
		if !slices.Contains(keys, key) {
			delete(object, key)
		}
	}
}

func objects(value any) []map[string]any {
	list, _ := value.([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if object, ok := item.(map[string]any); ok {
			out = append(out, object)
		}
	}
	return out
}
