package mcpserver

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/view"
)

const (
	shoppingListsPath = "/api/households/shopping/lists"
	shoppingItemsPath = "/api/households/shopping/items"
)

var (
	itemFields     = []string{"id", "display", "checked", "quantity", "note", "label"}
	listLineFields = []string{"id", "name"}
)

type shoppingReadInput struct {
	actionField
	ID      string `json:"id,omitempty" jsonschema:"list: shopping list id."`
	Page    int    `json:"page,omitempty" jsonschema:"lists: page number, starting at 1."`
	PerPage int    `json:"perPage,omitempty" jsonschema:"lists: results per page (default 20, max 100)."`
}

func addShoppingRead(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:    "shopping_read",
		summary: "Read shopping lists and their items.",
		read:    true,
	}, []action[shoppingReadInput]{
		{name: "lists", help: "List shopping lists. Optional: page, perPage.", run: listShoppingLists},
		{name: "list", help: "Read one shopping list with its items and recipes. Needs id.", run: getShoppingList},
	}, nil)
}

func listShoppingLists(ctx context.Context, s *session, in shoppingReadInput) (any, error) {
	raw, err := s.call(ctx, http.MethodGet, shoppingListsPath, pageQuery(in.Page, in.PerPage), nil)
	if err != nil {
		return nil, err
	}
	return view.Page(raw, listLineFields)
}

func getShoppingList(ctx context.Context, s *session, in shoppingReadInput) (any, error) {
	if err := need("list", "id", in.ID); err != nil {
		return nil, err
	}
	path, err := mealie.PathOf(shoppingListsPath, in.ID)
	if err != nil {
		return nil, err
	}
	return fetchListView(ctx, s, path)
}

func fetchListView(ctx context.Context, s *session, path string) (any, error) {
	raw, err := s.call(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	return listView(raw)
}

func listView(raw json.RawMessage) (any, error) {
	var list struct {
		ID               string           `json:"id"`
		Name             string           `json:"name"`
		ListItems        []map[string]any `json:"listItems"`
		RecipeReferences []struct {
			Recipe struct {
				Name string `json:"name"`
			} `json:"recipe"`
		} `json:"recipeReferences"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("decoding shopping list: %w", err)
	}
	items := make([]any, 0, len(list.ListItems))
	for _, item := range list.ListItems {
		items = append(items, view.Compact(item, itemFields))
	}
	recipes := make([]string, 0, len(list.RecipeReferences))
	for _, reference := range list.RecipeReferences {
		if reference.Recipe.Name != "" {
			recipes = append(recipes, reference.Recipe.Name)
		}
	}
	return map[string]any{"id": list.ID, "name": list.Name, "items": items, "recipes": recipes}, nil
}

type shoppingItem struct {
	ID       string   `json:"id,omitempty" jsonschema:"update_items: item id. Leave it out for add_items."`
	Note     *string  `json:"note,omitempty" jsonschema:"Free-text item, e.g. 2 lemons."`
	Display  string   `json:"display,omitempty" jsonschema:"add_items only: item text, saved as the note when note is empty (Mealie derives the shown text itself). update_items ignores it; change note instead."`
	Quantity *float64 `json:"quantity,omitempty" jsonschema:"Quantity of the item."`
	Checked  *bool    `json:"checked,omitempty" jsonschema:"update_items: true to check the item off, false to uncheck it. add_items: creates the item already checked."`
	FoodID   string   `json:"foodId,omitempty" jsonschema:"Food id."`
	UnitID   string   `json:"unitId,omitempty" jsonschema:"Unit id."`
	LabelID  string   `json:"labelId,omitempty" jsonschema:"Label id."`
}

type shoppingWriteInput struct {
	actionField
	ID     string         `json:"id,omitempty" jsonschema:"update_list, delete_list: shopping list id."`
	ListID string         `json:"listId,omitempty" jsonschema:"add_items, update_items, add_recipe, remove_recipe: shopping list id."`
	Name   string         `json:"name,omitempty" jsonschema:"create_list: list name."`
	Slug   string         `json:"slug,omitempty" jsonschema:"add_recipe, remove_recipe: recipe slug or id."`
	Scale  float64        `json:"scale,omitempty" jsonschema:"add_recipe, remove_recipe: how many times the recipe to add or remove (default 1)."`
	Items  []shoppingItem `json:"items,omitempty" jsonschema:"add_items, update_items: the items."`
	IDs    []string       `json:"ids,omitempty" jsonschema:"delete_items: item ids."`
	Fields map[string]any `json:"fields,omitempty" jsonschema:"update_list: list fields to change, using Mealie's names."`
}

func addShoppingWrite(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:    "shopping_write",
		summary: "Manage shopping lists: items, checking off, and adding a recipe's ingredients.",
	}, []action[shoppingWriteInput]{
		{name: "create_list", help: "Create a shopping list. Needs name.", run: createShoppingList},
		{name: "update_list", help: "Change a shopping list. Needs id, fields.", run: updateShoppingList},
		{name: "delete_list", help: "Delete a shopping list. Needs id.", run: deleteShoppingList},
		{name: "add_items", help: "Add items to a list. Needs listId, items (each with note or display).", run: addShoppingItems},
		{name: "update_items", help: "Change or check off items. Needs listId, items (each with id). quantity 0 and an empty note are sent as given.", run: updateShoppingItems},
		{name: "delete_items", help: "Delete items. Needs ids.", run: deleteShoppingItems},
		{name: "add_recipe", help: "Add a recipe's ingredients to a list. Needs listId, slug. Optional: scale.", run: addListRecipe},
		{name: "remove_recipe", help: "Remove a recipe's ingredients from a list. Needs listId, slug. Optional: scale.", run: removeListRecipe},
	}, nil)
}

func createShoppingList(ctx context.Context, s *session, in shoppingWriteInput) (any, error) {
	if err := need("create_list", "name", in.Name); err != nil {
		return nil, err
	}
	raw, err := s.call(ctx, http.MethodPost, shoppingListsPath, nil, map[string]string{"name": in.Name})
	if err != nil {
		return nil, err
	}
	return listView(raw)
}

func updateShoppingList(ctx context.Context, s *session, in shoppingWriteInput) (any, error) {
	raw, err := updateRaw(ctx, s, "update_list", shoppingListsPath, in.ID, in.Fields)
	if err != nil {
		return nil, err
	}
	return listView(raw)
}

func deleteShoppingList(ctx context.Context, s *session, in shoppingWriteInput) (any, error) {
	return deleteByID(ctx, s, "delete_list", shoppingListsPath, in.ID)
}

type newShoppingItem struct {
	ShoppingListID string  `json:"shoppingListId"`
	Note           string  `json:"note"`
	Quantity       float64 `json:"quantity,omitempty"`
	Checked        bool    `json:"checked,omitempty"`
	FoodID         string  `json:"foodId,omitempty"`
	UnitID         string  `json:"unitId,omitempty"`
	LabelID        string  `json:"labelId,omitempty"`
}

func addShoppingItems(ctx context.Context, s *session, in shoppingWriteInput) (any, error) {
	if err := need("add_items", "listId", in.ListID); err != nil {
		return nil, err
	}
	if len(in.Items) == 0 {
		return nil, needPresent("add_items", "items", false)
	}
	listPath, err := mealie.PathOf(shoppingListsPath, in.ListID)
	if err != nil {
		return nil, err
	}
	body := make([]newShoppingItem, 0, len(in.Items))
	for _, item := range in.Items {
		if item.ID != "" {
			return nil, toolFailure(`Action "add_items" creates new items, so leave out id. Use update_items to change an existing item.`)
		}
		note := cmp.Or(deref(item.Note), item.Display)
		if note == "" {
			return nil, toolFailure(`Action "add_items" needs note or display on every item.`)
		}
		body = append(body, newShoppingItem{
			ShoppingListID: in.ListID,
			Note:           note,
			Quantity:       deref(item.Quantity),
			Checked:        deref(item.Checked),
			FoodID:         item.FoodID,
			UnitID:         item.UnitID,
			LabelID:        item.LabelID,
		})
	}
	if _, err := s.call(ctx, http.MethodPost, shoppingItemsPath+"/create-bulk", nil, body); err != nil {
		return nil, err
	}
	return fetchListView(ctx, s, listPath)
}

func deref[T any](value *T) T {
	var zero T
	if value == nil {
		return zero
	}
	return *value
}

func itemChanges(item shoppingItem) map[string]any {
	changes := map[string]any{}
	if item.Note != nil {
		changes["note"] = *item.Note
	}
	for key, value := range map[string]string{
		"foodId": item.FoodID, "unitId": item.UnitID, "labelId": item.LabelID,
	} {
		if value != "" {
			changes[key] = value
		}
	}
	if item.Quantity != nil {
		changes["quantity"] = *item.Quantity
	}
	if item.Checked != nil {
		changes["checked"] = *item.Checked
	}
	return changes
}

func updateShoppingItems(ctx context.Context, s *session, in shoppingWriteInput) (any, error) {
	if err := need("update_items", "listId", in.ListID); err != nil {
		return nil, err
	}
	if len(in.Items) == 0 {
		return nil, needPresent("update_items", "items", false)
	}
	seen := make(map[string]bool, len(in.Items))
	for _, item := range in.Items {
		if err := need("update_items", "id", item.ID); err != nil {
			return nil, err
		}
		if seen[item.ID] {
			return nil, toolFailure(fmt.Sprintf("Item %q appears more than once. Nothing was changed.", item.ID))
		}
		seen[item.ID] = true
	}
	listPath, err := mealie.PathOf(shoppingListsPath, in.ListID)
	if err != nil {
		return nil, err
	}
	merged := make([]map[string]any, 0, len(in.Items))
	for _, item := range in.Items {
		path, err := mealie.PathOf(shoppingItemsPath, item.ID)
		if err != nil {
			return nil, err
		}
		current, err := mergedObject(ctx, s, path, itemChanges(item))
		if err != nil {
			return nil, err
		}
		merged = append(merged, current)
	}
	for i, current := range merged {
		if !onList(current, in.ListID) {
			return nil, toolFailure(fmt.Sprintf("Item %q is on another shopping list, not %q. Nothing was changed.", in.Items[i].ID, in.ListID))
		}
	}
	if _, err := s.call(ctx, http.MethodPut, shoppingItemsPath, nil, merged); err != nil {
		return nil, err
	}
	return fetchListView(ctx, s, listPath)
}

func onList(item map[string]any, listID string) bool {
	raw, ok := item["shoppingListId"].(json.RawMessage)
	if !ok {
		return false
	}
	var current string
	return json.Unmarshal(raw, &current) == nil && current == listID
}

func deleteShoppingItems(ctx context.Context, s *session, in shoppingWriteInput) (any, error) {
	if len(in.IDs) == 0 {
		return nil, needPresent("delete_items", "ids", false)
	}
	for _, id := range in.IDs {
		if err := mealie.CheckSegment(id); err != nil {
			return nil, err
		}
	}
	if _, err := s.call(ctx, http.MethodDelete, shoppingItemsPath, url.Values{"ids": in.IDs}, nil); err != nil {
		return nil, err
	}
	return ok(nil), nil
}

func recipeScale(scale float64) float64 {
	if scale == 0 {
		return 1
	}
	return scale
}

func listRecipeTarget(ctx context.Context, s *session, action string, in shoppingWriteInput) (string, error) {
	if err := need(action, "listId", in.ListID); err != nil {
		return "", err
	}
	if err := need(action, "slug", in.Slug); err != nil {
		return "", err
	}
	if err := mealie.CheckSegment(in.ListID); err != nil {
		return "", err
	}
	return recipeID(ctx, s, in.Slug)
}

func addListRecipe(ctx context.Context, s *session, in shoppingWriteInput) (any, error) {
	id, err := listRecipeTarget(ctx, s, "add_recipe", in)
	if err != nil {
		return nil, err
	}
	path, err := mealie.PathOf(shoppingListsPath, in.ListID, "recipe")
	if err != nil {
		return nil, err
	}
	body := []map[string]any{{"recipeId": id, "recipeIncrementQuantity": recipeScale(in.Scale)}}
	raw, err := s.call(ctx, http.MethodPost, path, nil, body)
	if err != nil {
		return nil, err
	}
	return listView(raw)
}

func removeListRecipe(ctx context.Context, s *session, in shoppingWriteInput) (any, error) {
	id, err := listRecipeTarget(ctx, s, "remove_recipe", in)
	if err != nil {
		return nil, err
	}
	path, err := mealie.PathOf(shoppingListsPath, in.ListID, "recipe", id, "delete")
	if err != nil {
		return nil, err
	}
	raw, err := s.call(ctx, http.MethodPost, path, nil, map[string]float64{"recipeDecrementQuantity": recipeScale(in.Scale)})
	if err != nil {
		return nil, err
	}
	return listView(raw)
}
