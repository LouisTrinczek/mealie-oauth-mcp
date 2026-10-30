package mcpserver

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

const (
	listsBase = "/api/households/shopping/lists"
	itemsBase = "/api/households/shopping/items"

	shoppingListBody = `{"id":"l-1","name":"Weekly","extras":{},"groupId":"g-1","householdId":"h-1","listItems":[` +
		`{"id":"i-1","display":"2 lemons","checked":false,"quantity":2,"note":"lemons","position":0,"shoppingListId":"l-1","label":{"id":"lb-1","name":"Produce","color":"#fff"},"extras":{}},` +
		`{"id":"i-2","display":"milk","checked":true,"quantity":0,"note":"milk","position":1,"shoppingListId":"l-1","label":null}],` +
		`"recipeReferences":[{"recipeId":"r-1","recipeQuantity":1,"recipe":{"id":"r-1","name":"Soup","slug":"soup"}},{"recipeId":"r-2","recipeQuantity":1,"recipe":null}],"labelSettings":[]}`
	shoppingListView  = `{"id":"l-1","items":[{"checked":false,"display":"2 lemons","id":"i-1","label":{"id":"lb-1","name":"Produce"},"note":"lemons","quantity":2},{"checked":true,"display":"milk","id":"i-2","note":"milk"}],"name":"Weekly","recipes":["Soup"]}`
	shoppingListsPage = `{"page":1,"per_page":20,"total":1,"total_pages":1,"items":[{"id":"l-1","name":"Weekly","createdAt":"2026-10-01T00:00:00Z","listItems":[]}],"next":null,"previous":null}`
)

func TestShoppingRead_ListsGolden(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, listsBase, http.StatusOK, shoppingListsPage)
	cs := connectTools(t, api)

	res := callTool(t, cs, "shopping_read", map[string]any{"action": "lists", "page": 2})
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	assertGolden(t, "shopping_lists", resultText(t, res))
	want := url.Values{"page": {"2"}, "perPage": {"20"}}
	if seen := api.requests(); len(seen) != 1 || seen[0].Method != http.MethodGet || !reflect.DeepEqual(seen[0].Query, want) {
		t.Errorf("requests = %+v, want one GET %s with query %v", seen, listsBase, want)
	}
}

func TestShoppingRead_ListView(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, listsBase+"/l-1", http.StatusOK, shoppingListBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "shopping_read", map[string]any{"action": "list", "id": "l-1"})
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	assertGolden(t, "shopping_list", resultText(t, res))
}

func TestShoppingWrite_CreateList(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, listsBase, http.StatusCreated, shoppingListBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "shopping_write", map[string]any{"action": "create_list", "name": "Weekly"})
	if got := resultText(t, res); res.IsError || got != shoppingListView {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, shoppingListView)
	}
	if seen := api.requests(); len(seen) != 1 || seen[0].Method != http.MethodPost || seen[0].Body != `{"name":"Weekly"}` {
		t.Errorf("requests = %+v, want one POST with body {\"name\":\"Weekly\"}", seen)
	}
}

func TestShoppingWrite_UpdateListMergesAndShapesList(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, listsBase+"/l-1", http.StatusOK, `{"id":"l-1","name":"Old","extras":{"x":1}}`)
	api.on(http.MethodPut, listsBase+"/l-1", http.StatusOK, shoppingListBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "shopping_write", map[string]any{"action": "update_list", "id": "l-1", "fields": map[string]any{"name": "Weekly"}})
	if got := resultText(t, res); res.IsError || got != shoppingListView {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, shoppingListView)
	}
	seen := api.requests()
	wantBody := `{"extras":{"x":1},"id":"l-1","name":"Weekly"}`
	if len(seen) != 2 || seen[1].Method != http.MethodPut || seen[1].Body != wantBody {
		t.Errorf("requests = %+v, want GET then PUT with body %s", seen, wantBody)
	}
}

func TestShoppingWrite_DeleteList(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodDelete, listsBase+"/l-1", http.StatusOK, shoppingListBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "shopping_write", map[string]any{"action": "delete_list", "id": "l-1"})
	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
	}
	if seen := api.requests(); len(seen) != 1 || seen[0].Method != http.MethodDelete || seen[0].Path != listsBase+"/l-1" {
		t.Errorf("requests = %+v, want one DELETE %s/l-1", seen, listsBase)
	}
}

func TestShoppingWrite_AddItems(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, itemsBase+"/create-bulk", http.StatusOK, `{"createdItems":[],"updatedItems":[],"deletedItems":[]}`)
	api.on(http.MethodGet, listsBase+"/l-1", http.StatusOK, shoppingListBody)
	cs := connectTools(t, api)

	items := []any{
		map[string]any{"note": "lemons", "display": "2 lemons", "quantity": 2, "foodId": "f-1", "unitId": "u-1", "labelId": "lb-1"},
		map[string]any{"display": "milk"},
	}
	res := callTool(t, cs, "shopping_write", map[string]any{"action": "add_items", "listId": "l-1", "items": items})
	if got := resultText(t, res); res.IsError || got != shoppingListView {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, shoppingListView)
	}
	seen := api.requests()
	wantBody := `[{"shoppingListId":"l-1","note":"lemons","quantity":2,"foodId":"f-1","unitId":"u-1","labelId":"lb-1"},{"shoppingListId":"l-1","note":"milk"}]`
	if len(seen) != 2 || seen[0].Method != http.MethodPost || seen[0].Body != wantBody || seen[1].Method != http.MethodGet || seen[1].Path != listsBase+"/l-1" {
		t.Errorf("requests = %+v, want POST with body %s then GET %s/l-1", seen, wantBody, listsBase)
	}
}

func updateItemsAPI(t *testing.T) *fakeAPI {
	t.Helper()
	api := newFakeMealie(t)
	api.on(http.MethodGet, itemsBase+"/i-1", http.StatusOK, `{"id":"i-1","shoppingListId":"l-1","note":"old","checked":false,"extras":{"x":1},"position":4}`)
	api.on(http.MethodGet, itemsBase+"/i-2", http.StatusOK, `{"id":"i-2","shoppingListId":"l-1","checked":true,"position":5}`)
	api.on(http.MethodPut, itemsBase, http.StatusOK, `{"createdItems":[],"updatedItems":[],"deletedItems":[]}`)
	api.on(http.MethodGet, listsBase+"/l-1", http.StatusOK, shoppingListBody)
	return api
}

func TestShoppingWrite_UpdateItemsMergesAndPutsOnce(t *testing.T) {
	api := updateItemsAPI(t)
	cs := connectTools(t, api)

	items := []any{
		map[string]any{"id": "i-1", "checked": true, "display": "d1"},
		map[string]any{"id": "i-2", "note": "n", "display": "d", "quantity": 3, "foodId": "f", "unitId": "u", "labelId": "lb"},
	}
	res := callTool(t, cs, "shopping_write", map[string]any{"action": "update_items", "listId": "l-1", "items": items})
	if got := resultText(t, res); res.IsError || got != shoppingListView {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, shoppingListView)
	}
	wantBody := `[{"checked":true,"extras":{"x":1},"id":"i-1","note":"old","position":4,"shoppingListId":"l-1"},` +
		`{"checked":true,"foodId":"f","id":"i-2","labelId":"lb","note":"n","position":5,"quantity":3,"shoppingListId":"l-1","unitId":"u"}]`
	var puts []seenRequest
	for _, req := range api.requests() {
		if req.Method == http.MethodPut {
			puts = append(puts, req)
		}
	}
	if len(puts) != 1 || puts[0].Path != itemsBase || puts[0].Body != wantBody {
		t.Errorf("PUTs = %+v, want exactly one to %s with body %s", puts, itemsBase, wantBody)
	}
}

func TestShoppingWrite_UpdateItemsIgnoresDisplay(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, itemsBase+"/i-3", http.StatusOK, `{"id":"i-3","shoppingListId":"l-1","note":"","display":"1 cup flour","foodId":"f-1","checked":false}`)
	api.on(http.MethodPut, itemsBase, http.StatusOK, `{}`)
	api.on(http.MethodGet, listsBase+"/l-1", http.StatusOK, shoppingListBody)
	cs := connectTools(t, api)

	callTool(t, cs, "shopping_write", map[string]any{"action": "update_items", "listId": "l-1", "items": []any{map[string]any{"id": "i-3", "checked": true, "display": "1 cup flour"}}})
	seen := api.requests()
	wantBody := `[{"checked":true,"display":"1 cup flour","foodId":"f-1","id":"i-3","note":"","shoppingListId":"l-1"}]`
	if len(seen) != 3 || seen[1].Method != http.MethodPut || seen[1].Body != wantBody {
		t.Errorf("requests = %+v, want PUT with body %s second", seen, wantBody)
	}
}

func TestShoppingWrite_UncheckWithFalse(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, itemsBase+"/i-2", http.StatusOK, `{"id":"i-2","shoppingListId":"l-1","checked":true}`)
	api.on(http.MethodPut, itemsBase, http.StatusOK, `{}`)
	api.on(http.MethodGet, listsBase+"/l-1", http.StatusOK, shoppingListBody)
	cs := connectTools(t, api)

	callTool(t, cs, "shopping_write", map[string]any{"action": "update_items", "listId": "l-1", "items": []any{map[string]any{"id": "i-2", "checked": false}}})
	seen := api.requests()
	wantBody := `[{"checked":false,"id":"i-2","shoppingListId":"l-1"}]`
	if len(seen) != 3 || seen[1].Method != http.MethodPut || seen[1].Body != wantBody {
		t.Errorf("requests = %+v, want PUT with body %s second", seen, wantBody)
	}
}

func TestShoppingWrite_DeleteItemsQuery(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodDelete, itemsBase, http.StatusOK, `{"message":"deleted","error":false}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "shopping_write", map[string]any{"action": "delete_items", "ids": []any{"a", "b"}})
	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
	}
	want := url.Values{"ids": {"a", "b"}}
	if seen := api.requests(); len(seen) != 1 || seen[0].Method != http.MethodDelete || !reflect.DeepEqual(seen[0].Query, want) {
		t.Errorf("requests = %+v, want one DELETE with query ids=a&ids=b", seen)
	}
}

func TestShoppingWrite_AddRecipeDefaultScale(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
	api.on(http.MethodPost, listsBase+"/l-1/recipe", http.StatusOK, shoppingListBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "shopping_write", map[string]any{"action": "add_recipe", "listId": "l-1", "slug": "soup"})
	if got := resultText(t, res); res.IsError || got != shoppingListView {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, shoppingListView)
	}
	seen := api.requests()
	wantBody := `[{"recipeId":"r-1","recipeIncrementQuantity":1}]`
	if len(seen) != 2 || seen[1].Method != http.MethodPost || seen[1].Body != wantBody {
		t.Errorf("requests = %+v, want POST with body %s second", seen, wantBody)
	}
}

func TestShoppingWrite_RemoveRecipeScale(t *testing.T) {
	tests := []struct {
		name  string
		scale any
		body  string
	}{
		{"default", nil, `{"recipeDecrementQuantity":1}`},
		{"explicit", 2.5, `{"recipeDecrementQuantity":2.5}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
			api.on(http.MethodPost, listsBase+"/l-1/recipe/r-1/delete", http.StatusOK, shoppingListBody)
			cs := connectTools(t, api)

			args := map[string]any{"action": "remove_recipe", "listId": "l-1", "slug": "soup"}
			if tt.scale != nil {
				args["scale"] = tt.scale
			}
			res := callTool(t, cs, "shopping_write", args)
			if got := resultText(t, res); res.IsError || got != shoppingListView {
				t.Errorf("result = %s (error %v), want %s", got, res.IsError, shoppingListView)
			}
			if seen := api.requests(); len(seen) != 2 || seen[1].Body != tt.body {
				t.Errorf("requests = %+v, want POST with body %s second", seen, tt.body)
			}
		})
	}
}

func TestShoppingTools_Failures(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"list needs id", "shopping_read", map[string]any{"action": "list"}, `Action "list" needs id.`},
		{"list bad id", "shopping_read", map[string]any{"action": "list", "id": "a/b"}, `"a/b" is not a valid slug or id.`},
		{"create needs name", "shopping_write", map[string]any{"action": "create_list"}, `Action "create_list" needs name.`},
		{"update needs fields", "shopping_write", map[string]any{"action": "update_list", "id": "l-1"}, `Action "update_list" needs fields.`},
		{"delete needs id", "shopping_write", map[string]any{"action": "delete_list"}, `Action "delete_list" needs id.`},
		{"add_items needs listId", "shopping_write", map[string]any{"action": "add_items"}, `Action "add_items" needs listId.`},
		{"add_items needs items", "shopping_write", map[string]any{"action": "add_items", "listId": "l-1"}, `Action "add_items" needs items.`},
		{"add_items needs text", "shopping_write", map[string]any{"action": "add_items", "listId": "l-1", "items": []any{map[string]any{"note": "ok"}, map[string]any{"quantity": 1}}}, `Action "add_items" needs note or display on every item.`},
		{"add_items bad listId", "shopping_write", map[string]any{"action": "add_items", "listId": "..", "items": []any{map[string]any{"note": "ok"}}}, `".." is not a valid slug or id.`},
		{"update_items needs listId", "shopping_write", map[string]any{"action": "update_items"}, `Action "update_items" needs listId.`},
		{"update_items needs items", "shopping_write", map[string]any{"action": "update_items", "listId": "l-1"}, `Action "update_items" needs items.`},
		{"update_items bad listId", "shopping_write", map[string]any{"action": "update_items", "listId": "a/b", "items": []any{map[string]any{"id": "i-1"}}}, `"a/b" is not a valid slug or id.`},
		{"update_items needs id", "shopping_write", map[string]any{"action": "update_items", "listId": "l-1", "items": []any{map[string]any{"checked": true}}}, `Action "update_items" needs id.`},
		{"update_items bad id", "shopping_write", map[string]any{"action": "update_items", "listId": "l-1", "items": []any{map[string]any{"id": "x?y"}}}, `"x?y" is not a valid slug or id.`},
		{"delete_items needs ids", "shopping_write", map[string]any{"action": "delete_items"}, `Action "delete_items" needs ids.`},
		{"delete_items bad id", "shopping_write", map[string]any{"action": "delete_items", "ids": []any{"a", "b/c"}}, `"b/c" is not a valid slug or id.`},
		{"add_recipe needs listId", "shopping_write", map[string]any{"action": "add_recipe", "slug": "soup"}, `Action "add_recipe" needs listId.`},
		{"add_recipe needs slug", "shopping_write", map[string]any{"action": "add_recipe", "listId": "l-1"}, `Action "add_recipe" needs slug.`},
		{"remove_recipe needs slug", "shopping_write", map[string]any{"action": "remove_recipe", "listId": "l-1"}, `Action "remove_recipe" needs slug.`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			cs := connectTools(t, api)

			res := callTool(t, cs, tt.tool, tt.args)
			if got := resultText(t, res); !res.IsError || got != tt.want {
				t.Errorf("result = %q (error %v), want %q", got, res.IsError, tt.want)
			}
			if seen := api.requests(); len(seen) != 0 {
				t.Errorf("requests = %+v, want none", seen)
			}
		})
	}
}

func TestShoppingTools_MealieFailures(t *testing.T) {
	const gone = `{"detail":"gone"}`
	const want = "Mealie could not find that (404): gone. Check the slug or id; search or list to find it."
	tests := []struct {
		name         string
		tool         string
		args         map[string]any
		method, path string
		prepare      func(*fakeAPI)
	}{
		{"lists", "shopping_read", map[string]any{"action": "lists"}, http.MethodGet, listsBase, nil},
		{"list", "shopping_read", map[string]any{"action": "list", "id": "l-1"}, http.MethodGet, listsBase + "/l-1", nil},
		{"create_list", "shopping_write", map[string]any{"action": "create_list", "name": "n"}, http.MethodPost, listsBase, nil},
		{"update_list", "shopping_write", map[string]any{"action": "update_list", "id": "l-1", "fields": map[string]any{"name": "n"}}, http.MethodGet, listsBase + "/l-1", nil},
		{"add_items", "shopping_write", map[string]any{"action": "add_items", "listId": "l-1", "items": []any{map[string]any{"note": "n"}}}, http.MethodPost, itemsBase + "/create-bulk", nil},
		{"add_items refetch", "shopping_write", map[string]any{"action": "add_items", "listId": "l-1", "items": []any{map[string]any{"note": "n"}}}, http.MethodGet, listsBase + "/l-1", func(a *fakeAPI) {
			a.on(http.MethodPost, itemsBase+"/create-bulk", http.StatusOK, `{}`)
		}},
		{"update_items get", "shopping_write", map[string]any{"action": "update_items", "listId": "l-1", "items": []any{map[string]any{"id": "i-1"}}}, http.MethodGet, itemsBase + "/i-1", nil},
		{"update_items put", "shopping_write", map[string]any{"action": "update_items", "listId": "l-1", "items": []any{map[string]any{"id": "i-1"}}}, http.MethodPut, itemsBase, func(a *fakeAPI) {
			a.on(http.MethodGet, itemsBase+"/i-1", http.StatusOK, `{"id":"i-1","shoppingListId":"l-1"}`)
		}},
		{"delete_items", "shopping_write", map[string]any{"action": "delete_items", "ids": []any{"a"}}, http.MethodDelete, itemsBase, nil},
		{"add_recipe recipe", "shopping_write", map[string]any{"action": "add_recipe", "listId": "l-1", "slug": "soup"}, http.MethodGet, "/api/recipes/soup", nil},
		{"add_recipe post", "shopping_write", map[string]any{"action": "add_recipe", "listId": "l-1", "slug": "soup"}, http.MethodPost, listsBase + "/l-1/recipe", func(a *fakeAPI) {
			a.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
		}},
		{"remove_recipe post", "shopping_write", map[string]any{"action": "remove_recipe", "listId": "l-1", "slug": "soup"}, http.MethodPost, listsBase + "/l-1/recipe/r-1/delete", func(a *fakeAPI) {
			a.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			if tt.prepare != nil {
				tt.prepare(api)
			}
			api.on(tt.method, tt.path, http.StatusNotFound, gone)
			cs := connectTools(t, api)

			res := callTool(t, cs, tt.tool, tt.args)
			if got := resultText(t, res); !res.IsError || got != want {
				t.Errorf("result = %q (error %v), want %q", got, res.IsError, want)
			}
		})
	}
}

func TestShoppingTools_BadMealieBody(t *testing.T) {
	tests := []struct {
		name         string
		args         map[string]any
		method, path string
	}{
		{"create_list", map[string]any{"action": "create_list", "name": "n"}, http.MethodPost, listsBase},
		{"update_items", map[string]any{"action": "update_items", "listId": "l-1", "items": []any{map[string]any{"id": "i-1"}}}, http.MethodGet, itemsBase + "/i-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(tt.method, tt.path, http.StatusOK, `[1]`)
			cs := connectTools(t, api)

			if res := callTool(t, cs, "shopping_write", tt.args); !res.IsError {
				t.Errorf("result = %s, want a tool error", resultText(t, res))
			}
		})
	}
}

func TestShoppingWrite_RecipeActionsRejectBadListID(t *testing.T) {
	for _, name := range []string{"add_recipe", "remove_recipe"} {
		t.Run(name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
			cs := connectTools(t, api)

			res := callTool(t, cs, "shopping_write", map[string]any{"action": name, "listId": "a/b", "slug": "soup"})
			if got := resultText(t, res); !res.IsError || got != `"a/b" is not a valid slug or id.` {
				t.Errorf("result = %q (error %v), want the invalid segment error", got, res.IsError)
			}
			for _, req := range api.requests() {
				if req.Method != http.MethodGet {
					t.Errorf("unexpected write %s %s", req.Method, req.Path)
				}
			}
		})
	}
}

func writeRequests(api *fakeAPI) []seenRequest {
	var writes []seenRequest
	for _, req := range api.requests() {
		if req.Method != http.MethodGet {
			writes = append(writes, req)
		}
	}
	return writes
}

func TestShoppingWrite_UpdateItemsSendsZeroAndEmpty(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, itemsBase+"/i-1", http.StatusOK, `{"id":"i-1","shoppingListId":"l-1","note":"old","quantity":3}`)
	api.on(http.MethodPut, itemsBase, http.StatusOK, `{}`)
	api.on(http.MethodGet, listsBase+"/l-1", http.StatusOK, shoppingListBody)
	cs := connectTools(t, api)

	callTool(t, cs, "shopping_write", map[string]any{"action": "update_items", "listId": "l-1", "items": []any{map[string]any{"id": "i-1", "quantity": 0, "note": ""}}})
	wantBody := `[{"id":"i-1","note":"","quantity":0,"shoppingListId":"l-1"}]`
	if writes := writeRequests(api); len(writes) != 1 || writes[0].Method != http.MethodPut || writes[0].Body != wantBody {
		t.Errorf("writes = %+v, want one PUT with body %s", writes, wantBody)
	}
}

func TestShoppingWrite_UpdateItemsOtherListRejected(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, itemsBase+"/i-1", http.StatusOK, `{"id":"i-1","shoppingListId":"other"}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "shopping_write", map[string]any{"action": "update_items", "listId": "l-1", "items": []any{map[string]any{"id": "i-1", "checked": true}}})
	requireToolError(t, res, `Item "i-1" is on another shopping list, not "l-1". Nothing was changed.`)
	if writes := writeRequests(api); len(writes) != 0 {
		t.Errorf("writes = %+v, want none", writes)
	}
}

func TestShoppingWrite_UpdateItemsDuplicateIDs(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	items := []any{map[string]any{"id": "i-1", "checked": true}, map[string]any{"id": "i-1", "checked": false}}
	res := callTool(t, cs, "shopping_write", map[string]any{"action": "update_items", "listId": "l-1", "items": items})
	requireToolError(t, res, `Item "i-1" appears more than once. Nothing was changed.`)
	if seen := api.requests(); len(seen) != 0 {
		t.Errorf("requests = %+v, want none", seen)
	}
}

func TestShoppingWrite_UpdateItemsSecondGetFails(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, itemsBase+"/i-1", http.StatusOK, `{"id":"i-1","shoppingListId":"l-1"}`)
	api.on(http.MethodGet, itemsBase+"/i-2", http.StatusInternalServerError, `{"detail":"boom"}`)
	cs := connectTools(t, api)

	items := []any{map[string]any{"id": "i-1", "checked": true}, map[string]any{"id": "i-2", "checked": true}}
	res := callTool(t, cs, "shopping_write", map[string]any{"action": "update_items", "listId": "l-1", "items": items})
	requireToolError(t, res, string(errMealieUnavailable))
	if writes := writeRequests(api); len(writes) != 0 {
		t.Errorf("writes = %+v, want none", writes)
	}
}

func TestShoppingWrite_AddItemsRejectsID(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res := callTool(t, cs, "shopping_write", map[string]any{"action": "add_items", "listId": "l-1", "items": []any{map[string]any{"id": "i-1", "note": "milk"}}})
	requireToolError(t, res, `Action "add_items" creates new items, so leave out id. Use update_items to change an existing item.`)
	if seen := api.requests(); len(seen) != 0 {
		t.Errorf("requests = %+v, want none", seen)
	}
}

func TestShoppingWrite_AddItemsSendsChecked(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, itemsBase+"/create-bulk", http.StatusOK, `{}`)
	api.on(http.MethodGet, listsBase+"/l-1", http.StatusOK, shoppingListBody)
	cs := connectTools(t, api)

	callTool(t, cs, "shopping_write", map[string]any{"action": "add_items", "listId": "l-1", "items": []any{map[string]any{"note": "milk", "checked": true}}})
	wantBody := `[{"shoppingListId":"l-1","note":"milk","checked":true}]`
	if writes := writeRequests(api); len(writes) != 1 || writes[0].Body != wantBody {
		t.Errorf("writes = %+v, want one POST with body %s", writes, wantBody)
	}
}

func TestShoppingWrite_RecipeBadListIDMakesNoRequest(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res := callTool(t, cs, "shopping_write", map[string]any{"action": "add_recipe", "listId": "../x", "slug": "soup"})
	requireToolError(t, res, `"../x" is not a valid slug or id.`)
	if seen := api.requests(); len(seen) != 0 {
		t.Errorf("requests = %+v, want none", seen)
	}
}
