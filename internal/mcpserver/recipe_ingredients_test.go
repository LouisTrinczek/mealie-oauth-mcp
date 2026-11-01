package mcpserver

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

const (
	storedIngredientsBody   = `{"id":"r-1","slug":"tomato-soup","recipeIngredient":[{"referenceId":"a","display":"1 tablespoon oil","originalText":"1 tbsp oil"},{"referenceId":"b","originalText":"salt"},{"referenceId":"c","originalText":null},{"referenceId":"d","originalText":""},{"referenceId":"e"}]}`
	repeatedReferenceBody   = `{"recipeIngredient":[{"referenceId":"a","originalText":"first"},{"referenceId":"a","originalText":"second"}]}`
	tomatoSoupPath          = "/api/recipes/tomato-soup"
	storedIngredientsFailed = `{"detail":"boom"}`
)

func decodeJSON(t *testing.T, text string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		t.Fatalf("decoding %s: %v", text, err)
	}
	return value
}

func TestRecipesWrite_UpdateRestoresOriginalText(t *testing.T) {
	tests := []struct {
		name     string
		stored   string
		fields   string
		wantSent string
		wantRead bool
	}{
		{"RestoresMissingText", storedIngredientsBody, `{"recipeIngredient":[{"referenceId":"a","quantity":1}]}`, `{"recipeIngredient":[{"referenceId":"a","quantity":1,"originalText":"1 tbsp oil"}]}`, true},
		{"KeepsSentText", storedIngredientsBody, `{"recipeIngredient":[{"referenceId":"a","originalText":"changed"}]}`, `{"recipeIngredient":[{"referenceId":"a","originalText":"changed"}]}`, false},
		{"KeepsSentNull", storedIngredientsBody, `{"recipeIngredient":[{"referenceId":"a","originalText":null}]}`, `{"recipeIngredient":[{"referenceId":"a","originalText":null}]}`, false},
		{"KeepsSentEmpty", storedIngredientsBody, `{"recipeIngredient":[{"referenceId":"a","originalText":""}]}`, `{"recipeIngredient":[{"referenceId":"a","originalText":""}]}`, false},
		{"NewIngredientWithoutReference", storedIngredientsBody, `{"recipeIngredient":[{"note":"pepper"}]}`, `{"recipeIngredient":[{"note":"pepper"}]}`, false},
		{"EmptyReference", storedIngredientsBody, `{"recipeIngredient":[{"referenceId":"","note":"pepper"}]}`, `{"recipeIngredient":[{"referenceId":"","note":"pepper"}]}`, false},
		{"NonStringReference", storedIngredientsBody, `{"recipeIngredient":[{"referenceId":5}]}`, `{"recipeIngredient":[{"referenceId":5}]}`, false},
		{"UnknownReference", storedIngredientsBody, `{"recipeIngredient":[{"referenceId":"z"}]}`, `{"recipeIngredient":[{"referenceId":"z"}]}`, true},
		{"StoredNullAddsNothing", storedIngredientsBody, `{"recipeIngredient":[{"referenceId":"c"}]}`, `{"recipeIngredient":[{"referenceId":"c"}]}`, true},
		{"StoredMissingAddsNothing", storedIngredientsBody, `{"recipeIngredient":[{"referenceId":"e"}]}`, `{"recipeIngredient":[{"referenceId":"e"}]}`, true},
		{"StoredEmptyIsRestored", storedIngredientsBody, `{"recipeIngredient":[{"referenceId":"d"}]}`, `{"recipeIngredient":[{"referenceId":"d","originalText":""}]}`, true},
		{"RepeatedInSent", storedIngredientsBody, `{"recipeIngredient":[{"referenceId":"a"},{"referenceId":"a","note":"copy"}]}`, `{"recipeIngredient":[{"referenceId":"a"},{"referenceId":"a","note":"copy"}]}`, true},
		{"RepeatedInSentWithTextOnOne", storedIngredientsBody, `{"recipeIngredient":[{"referenceId":"a","originalText":"mine"},{"referenceId":"a"}]}`, `{"recipeIngredient":[{"referenceId":"a","originalText":"mine"},{"referenceId":"a"}]}`, true},
		{"RepeatedInStored", repeatedReferenceBody, `{"recipeIngredient":[{"referenceId":"a"}]}`, `{"recipeIngredient":[{"referenceId":"a"}]}`, true},
		{"StoredWithoutIngredients", `{"id":"r-1"}`, `{"recipeIngredient":[{"referenceId":"a"}]}`, `{"recipeIngredient":[{"referenceId":"a"}]}`, true},
		{
			"MixedListKeepsOrderAndOtherFields",
			storedIngredientsBody,
			`{"description":"New","recipeIngredient":[{"note":"new"},{"referenceId":"b","food":{"id":"f","name":"salt"}},"plain",{"referenceId":"a","originalText":"mine"}]}`,
			`{"description":"New","recipeIngredient":[{"note":"new"},{"referenceId":"b","food":{"id":"f","name":"salt"},"originalText":"salt"},"plain",{"referenceId":"a","originalText":"mine"}]}`,
			true,
		},
		{"IngredientsNotAList", storedIngredientsBody, `{"recipeIngredient":"oops"}`, `{"recipeIngredient":"oops"}`, false},
		{"EmptyList", storedIngredientsBody, `{"recipeIngredient":[]}`, `{"recipeIngredient":[]}`, false},
		{"NoIngredients", storedIngredientsBody, `{"description":"New"}`, `{"description":"New"}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, tomatoSoupPath, http.StatusOK, tt.stored)
			api.on(http.MethodPatch, tomatoSoupPath, http.StatusOK, recipeBody)
			cs := connectTools(t, api)

			res := callTool(t, cs, "recipes_write", map[string]any{"action": "update", "slug": "tomato-soup", "fields": decodeJSON(t, tt.fields)})
			if res.IsError {
				t.Fatalf("tool error: %s", resultText(t, res))
			}
			wantMethods := []string{http.MethodPatch}
			if tt.wantRead {
				wantMethods = []string{http.MethodGet, http.MethodPatch}
			}
			seen := api.requests()
			methods := make([]string, 0, len(seen))
			for _, request := range seen {
				methods = append(methods, request.Method)
			}
			if !reflect.DeepEqual(methods, wantMethods) {
				t.Fatalf("methods = %v, want %v", methods, wantMethods)
			}
			if got := decodeJSON(t, seen[len(seen)-1].Body); !reflect.DeepEqual(got, decodeJSON(t, tt.wantSent)) {
				t.Errorf("sent %s, want %s", seen[len(seen)-1].Body, tt.wantSent)
			}
		})
	}
}

func TestRecipesWrite_UpdateDoesNotWriteWhenStoredRecipeFails(t *testing.T) {
	tests := []struct {
		name   string
		status int
		stored string
	}{
		{"ReadFails", http.StatusInternalServerError, storedIngredientsFailed},
		{"IngredientsNotAList", http.StatusOK, `{"recipeIngredient":"oops"}`},
		{"IngredientNotAnObject", http.StatusOK, `{"recipeIngredient":["2 eggs"]}`},
		{"OriginalTextNotAString", http.StatusOK, `{"recipeIngredient":[{"referenceId":"a","originalText":5}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, tomatoSoupPath, tt.status, tt.stored)
			cs := connectTools(t, api)

			res := callTool(t, cs, "recipes_write", map[string]any{"action": "update", "slug": "tomato-soup", "fields": map[string]any{"recipeIngredient": []any{map[string]any{"referenceId": "a"}}}})
			if !res.IsError {
				t.Fatalf("result = %s, want a tool error", resultText(t, res))
			}
			for _, request := range api.requests() {
				if request.Method != http.MethodGet {
					t.Errorf("sent %s %s, want no write", request.Method, request.Path)
				}
			}
		})
	}
}

func TestRestoreOriginalTexts_LeavesInputUnchanged(t *testing.T) {
	ingredient := map[string]any{"referenceId": "a"}
	sent := []any{ingredient}

	restored := restoreOriginalTexts(sent, map[string]string{"a": "1 tbsp oil"})

	if !reflect.DeepEqual(ingredient, map[string]any{"referenceId": "a"}) {
		t.Errorf("input ingredient = %v, want it unchanged", ingredient)
	}
	if want := []any{map[string]any{"referenceId": "a", "originalText": "1 tbsp oil"}}; !reflect.DeepEqual(restored, want) {
		t.Errorf("restored = %v, want %v", restored, want)
	}
}
