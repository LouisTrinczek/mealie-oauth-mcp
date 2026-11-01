package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
)

const (
	recipeIngredientField = "recipeIngredient"
	referenceIDField      = "referenceId"
	originalTextField     = "originalText"
)

func withStoredOriginalText(ctx context.Context, s *session, slug string, fields map[string]any) (map[string]any, error) {
	sent, isList := fields[recipeIngredientField].([]any)
	if !isList || !slices.ContainsFunc(sent, lacksOriginalText) {
		return fields, nil
	}
	raw, err := getRecipeRaw(ctx, s, slug)
	if err != nil {
		return nil, err
	}
	stored, err := storedOriginalTexts(raw)
	if err != nil {
		return nil, err
	}
	restored := maps.Clone(fields)
	restored[recipeIngredientField] = restoreOriginalTexts(sent, stored)
	return restored, nil
}

func lacksOriginalText(entry any) bool {
	_, _, ok := ingredientWithoutOriginalText(entry)
	return ok
}

func ingredientWithoutOriginalText(entry any) (map[string]any, string, bool) {
	ingredient, isObject := entry.(map[string]any)
	if !isObject {
		return nil, "", false
	}
	if _, present := ingredient[originalTextField]; present {
		return nil, "", false
	}
	referenceID := referenceOf(ingredient)
	return ingredient, referenceID, referenceID != ""
}

func referenceOf(entry any) string {
	ingredient, _ := entry.(map[string]any)
	referenceID, _ := ingredient[referenceIDField].(string)
	return referenceID
}

func storedOriginalTexts(raw json.RawMessage) (map[string]string, error) {
	var recipe struct {
		RecipeIngredient []struct {
			ReferenceID  string  `json:"referenceId"`
			OriginalText *string `json:"originalText"`
		} `json:"recipeIngredient"`
	}
	if err := json.Unmarshal(raw, &recipe); err != nil {
		return nil, fmt.Errorf("decoding the stored recipe ingredients: %w", err)
	}
	seen := map[string]int{}
	texts := map[string]string{}
	for _, ingredient := range recipe.RecipeIngredient {
		seen[ingredient.ReferenceID]++
		if ingredient.OriginalText != nil {
			texts[ingredient.ReferenceID] = *ingredient.OriginalText
		}
	}
	return withoutRepeated(texts, seen), nil
}

func restoreOriginalTexts(sent []any, stored map[string]string) []any {
	seen := map[string]int{}
	for _, entry := range sent {
		seen[referenceOf(entry)]++
	}
	texts := withoutRepeated(stored, seen)
	out := make([]any, 0, len(sent))
	for _, entry := range sent {
		ingredient, referenceID, lacking := ingredientWithoutOriginalText(entry)
		if text, known := texts[referenceID]; lacking && known {
			ingredient = maps.Clone(ingredient)
			ingredient[originalTextField] = text
			entry = ingredient
		}
		out = append(out, entry)
	}
	return out
}

func withoutRepeated(texts map[string]string, seen map[string]int) map[string]string {
	out := make(map[string]string, len(texts))
	for referenceID, text := range texts {
		if referenceID != "" && seen[referenceID] <= 1 {
			out[referenceID] = text
		}
	}
	return out
}
