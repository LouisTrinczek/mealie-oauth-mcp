package view

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTrimRecipe_Reductions(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "FoodAndUnitBecomeReferences",
			in:   `{"recipeIngredient":[{"referenceId":"r","quantity":1,"food":{"id":"f","name":"olive oil","pluralName":"olive oils","label":{"id":"l","name":"Oils","color":"#fff"},"aliases":[{"name":"evoo"}]},"unit":{"id":"u","name":"tablespoon","abbreviation":"tbsp","fraction":true,"useAbbreviation":false,"standardUnit":"ml","standardQuantity":15}}]}`,
			want: `{"recipeIngredient":[{"referenceId":"r","quantity":1,"food":{"id":"f","name":"olive oil"},"unit":{"id":"u","name":"tablespoon"}}]}`,
		},
		{
			name: "IngredientWithoutFoodOrUnit",
			in:   `{"recipeIngredient":[{"referenceId":"r","note":"salt to taste","food":null,"unit":null}]}`,
			want: `{"recipeIngredient":[{"referenceId":"r","note":"salt to taste"}]}`,
		},
		{
			name: "SubstituteFoodBecomesReference",
			in:   `{"recipeIngredient":[{"referenceId":"r","substitutions":[{"note":"or","substituteFood":{"id":"f2","name":"butter","pluralName":"butters","onHand":true}}]}]}`,
			want: `{"recipeIngredient":[{"referenceId":"r","substitutions":[{"note":"or","substituteFood":{"id":"f2","name":"butter"}}]}]}`,
		},
		{
			name: "OrganizersBecomeReferences",
			in:   `{"tags":[{"id":"t","name":"Quick","slug":"quick","recipeCount":12,"groupId":"g"}],"recipeCategory":[{"id":"c","name":"Soup","slug":"soup","recipeCount":3}],"tools":[{"id":"o","name":"Pot","slug":"pot","householdsWithTool":["h"]}]}`,
			want: `{"tags":[{"id":"t","name":"Quick","slug":"quick"}],"recipeCategory":[{"id":"c","name":"Soup","slug":"soup"}],"tools":[{"id":"o","name":"Pot","slug":"pot"}]}`,
		},
		{
			name: "UnknownFieldsSurvive",
			in:   `{"futureField":{"deep":{"x":1}},"recipeIngredient":[{"referenceId":"r","futureIngredientField":"kept","food":{"id":"f","name":"egg","futureFoodField":"dropped"}}],"recipeInstructions":[{"id":"s","text":"Mix.","ingredientReferences":[{"referenceId":"r"}]}]}`,
			want: `{"futureField":{"deep":{"x":1}},"recipeIngredient":[{"referenceId":"r","futureIngredientField":"kept","food":{"id":"f","name":"egg"}}],"recipeInstructions":[{"id":"s","text":"Mix.","ingredientReferences":[{"referenceId":"r"}]}]}`,
		},
		{
			name: "ParsedIngredientDropsOriginalText",
			in:   `{"recipeIngredient":[{"referenceId":"r","display":"1 tablespoon olive oil","originalText":"1 tbsp olive oil"}]}`,
			want: `{"recipeIngredient":[{"referenceId":"r","display":"1 tablespoon olive oil"}]}`,
		},
		{
			name: "UnparsedIngredientKeepsOriginalText",
			in:   `{"recipeIngredient":[{"referenceId":"r","note":"salt","originalText":"a pinch of salt"}]}`,
			want: `{"recipeIngredient":[{"referenceId":"r","note":"salt","originalText":"a pinch of salt"}]}`,
		},
		{
			name: "EmptyDisplayKeepsOriginalText",
			in:   `{"recipeIngredient":[{"referenceId":"r","display":"","originalText":"a pinch of salt"}]}`,
			want: `{"recipeIngredient":[{"referenceId":"r","originalText":"a pinch of salt"}]}`,
		},
		{
			name: "NonObjectEntriesAreLeftAlone",
			in:   `{"tags":["quick"],"recipeIngredient":["2 eggs"]}`,
			want: `{"tags":["quick"],"recipeIngredient":["2 eggs"]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := TrimRecipe(json.RawMessage(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decode(t, got), decode(t, []byte(tt.want))) {
				t.Errorf("TrimRecipe() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestTrimRecipe_StillTrims(t *testing.T) {
	got, err := TrimRecipe(json.RawMessage(`{"name":"Soup","description":"","groupId":"g","tags":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"name":"Soup"}` {
		t.Errorf("TrimRecipe() = %s", got)
	}
}

func TestTrimRecipe_NonObjectRecipe(t *testing.T) {
	got, err := TrimRecipe(json.RawMessage(`["x"]`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `["x"]` {
		t.Errorf("TrimRecipe() = %s", got)
	}
}

func TestTrimRecipe_InvalidJSON(t *testing.T) {
	if _, err := TrimRecipe(json.RawMessage(`{`)); err == nil {
		t.Error("expected error")
	}
}
