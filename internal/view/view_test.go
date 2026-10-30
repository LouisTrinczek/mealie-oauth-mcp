package view

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func decode(t *testing.T, raw []byte) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
	return v
}

func TestTrim_DropsEmptyAndBookkeeping(t *testing.T) {
	in := `{"name":"Soup","description":"","tags":[],"nutrition":{"calories":null},"groupId":"g","rating":0,"settings":{"public":false},"recipeIngredient":[{"note":"salt","unit":null}]}`
	want := `{"name":"Soup","rating":0,"settings":{"public":false},"recipeIngredient":[{"note":"salt"}]}`
	got, err := Trim(json.RawMessage(in))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decode(t, got), decode(t, []byte(want))) {
		t.Errorf("Trim() = %s, want %s", got, want)
	}
}

func TestTrim_DropsEveryBookkeepingKey(t *testing.T) {
	in := `{"groupId":"a","householdId":"b","userId":"c","createdAt":"d","updatedAt":"e","update_at":"f","dateUpdated":"g","cacheKey":"h","keep":"x"}`
	got, err := Trim(json.RawMessage(in))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"keep":"x"}` {
		t.Errorf("Trim() = %s", got)
	}
}

func TestTrim_InvalidJSON(t *testing.T) {
	if _, err := Trim(json.RawMessage(`{`)); err == nil {
		t.Error("expected error")
	}
}

func TestCompact_Rules(t *testing.T) {
	long := strings.Repeat("é", 200)
	item := map[string]any{
		"tags":        []any{map[string]any{"id": "1", "name": "Quick", "slug": "quick", "groupId": "g"}},
		"recipe":      map[string]any{"id": "r", "slug": "s", "name": "N", "description": "long"},
		"description": long,
		"secret":      "unlisted",
		"empty":       "",
		"nothing":     nil,
		"slugOnly":    []any{map[string]any{"slug": "only-slug"}},
		"idOnly":      []any{map[string]any{"id": "only-id"}},
		"nameless":    []any{map[string]any{"other": "x"}},
		"scalars":     []any{"a", "b"},
		"emptyObj":    map[string]any{"other": "x"},
		"rating":      float64(0),
		"public":      false,
	}
	fields := []string{"tags", "recipe", "description", "empty", "nothing", "missing", "slugOnly", "idOnly", "nameless", "scalars", "emptyObj", "rating", "public"}
	got := Compact(item, fields)
	want := map[string]any{
		"tags":        []any{"Quick"},
		"recipe":      map[string]any{"id": "r", "slug": "s", "name": "N"},
		"description": strings.Repeat("é", 160) + "…",
		"slugOnly":    []any{"only-slug"},
		"idOnly":      []any{"only-id"},
		"scalars":     []any{"a", "b"},
		"rating":      float64(0),
		"public":      false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Compact() = %v, want %v", got, want)
	}
}

func TestPage_Shape(t *testing.T) {
	tests := []struct {
		name    string
		page    int
		pages   int
		hasMore bool
	}{
		{"middle page", 2, 3, true},
		{"last page", 3, 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{
				"page": tt.page, "per_page": 20, "total": 45, "total_pages": tt.pages,
				"items": []any{map[string]any{"id": "1", "name": "A", "extra": "x"}},
			})
			got, err := Page(raw, []string{"id", "name"})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]any{
				"items":      []any{map[string]any{"id": "1", "name": "A"}},
				"page":       tt.page,
				"perPage":    20,
				"total":      45,
				"totalPages": tt.pages,
				"hasMore":    tt.hasMore,
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("Page() = %v, want %v", got, want)
			}
		})
	}
}

func TestPage_EmptyItemsIsEmptyList(t *testing.T) {
	got, err := Page(json.RawMessage(`{"page":1,"per_page":20,"total":0,"total_pages":0,"items":null}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if items, ok := got["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %#v", got["items"])
	}
}

func TestPage_Errors(t *testing.T) {
	for name, in := range map[string]string{
		"malformed":       `{`,
		"item not object": `{"items":["x"]}`,
		"null item":       `{"items":[null]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Page(json.RawMessage(in), nil); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestList_Shape(t *testing.T) {
	got, err := List(json.RawMessage(`[{"id":"1","name":"A","x":1},{"id":"2","name":"B"}]`), []string{"id", "name"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"items": []any{
			map[string]any{"id": "1", "name": "A"},
			map[string]any{"id": "2", "name": "B"},
		},
		"total": 2,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("List() = %v, want %v", got, want)
	}
}

func TestList_Errors(t *testing.T) {
	for name, in := range map[string]string{
		"malformed":       `[`,
		"item not object": `[1]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := List(json.RawMessage(in), nil); err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestEncode_Small(t *testing.T) {
	got, err := Encode(map[string]any{"a": 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"a":1}` {
		t.Errorf("Encode() = %s", got)
	}
}

func TestEncode_TooLarge(t *testing.T) {
	_, err := Encode(strings.Repeat("a", 70000))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	want := "The result is too large (69 KiB). Use a smaller perPage, a narrower filter, or leave out full."
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}

func TestEncode_MarshalError(t *testing.T) {
	_, err := Encode(make(chan int))
	if err == nil || errors.Is(err, ErrTooLarge) {
		t.Errorf("err = %v", err)
	}
}

func TestPerPageAndPageNumber(t *testing.T) {
	for in, want := range map[int]int{0: 20, -1: 20, 50: 50, 500: 100} {
		if got := PerPage(in); got != want {
			t.Errorf("PerPage(%d) = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[int]int{0: 1, 3: 3} {
		if got := PageNumber(in); got != want {
			t.Errorf("PageNumber(%d) = %d, want %d", in, got, want)
		}
	}
}
