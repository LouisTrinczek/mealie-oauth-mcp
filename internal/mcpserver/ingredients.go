package mcpserver

import (
	"context"
	"net/http"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/view"
)

const (
	foodsKind       = "foods"
	unitsKind       = "units"
	labelsKind      = "labels"
	maxParseEntries = 50
)

var ingredientKindNames = []any{foodsKind, unitsKind, labelsKind}

type ingredientKind struct {
	base        string
	fields      []string
	mergeMethod string
	mergeFrom   string
	mergeTo     string
}

var ingredientKinds = map[string]ingredientKind{
	foodsKind:  {base: "/api/foods", fields: []string{"id", "name", "pluralName", "label"}, mergeMethod: http.MethodPut, mergeFrom: "fromFood", mergeTo: "toFood"},
	unitsKind:  {base: "/api/units", fields: []string{"id", "name", "abbreviation"}, mergeMethod: http.MethodPut, mergeFrom: "fromUnit", mergeTo: "toUnit"},
	labelsKind: {base: "/api/groups/labels", fields: []string{"id", "name", "color"}, mergeMethod: http.MethodPost, mergeFrom: "fromId", mergeTo: "toId"},
}

func notApplicable(field, kind string) error {
	return toolFailure(field + " does not apply to " + kind + ".")
}

type ingredientsReadInput struct {
	actionField
	Kind        string   `json:"kind" jsonschema:"Which kind: foods, units or labels. Ignored by parse."`
	ID          string   `json:"id,omitempty" jsonschema:"get: id."`
	Search      string   `json:"search,omitempty" jsonschema:"list: text to search for (foods only)."`
	Page        int      `json:"page,omitempty" jsonschema:"list: page number, starting at 1."`
	PerPage     int      `json:"perPage,omitempty" jsonschema:"list: results per page (default 20, max 100)."`
	Ingredients []string `json:"ingredients,omitempty" jsonschema:"parse: ingredient lines such as '2 cups flour' (max 50)."`
}

func addIngredientsRead(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:    "ingredients_read",
		summary: "Read foods, units, and labels, and parse ingredient text into structured ingredients.",
		read:    true,
	}, []action[ingredientsReadInput]{
		{name: "list", help: "List foods, units or labels. Optional: search (foods only), page, perPage.", run: listIngredients},
		{name: "get", help: "Read one food, unit or label. Needs id.", run: getIngredient},
		{name: "empty", help: "List foods or labels that no recipe uses (not for units).", run: emptyIngredients},
		{name: "parse", help: "Parse ingredient lines into structured ingredients. Needs ingredients (max 50). Any kind works and is ignored.", run: parseIngredients},
	}, map[string][]any{"kind": ingredientKindNames})
}

func listIngredients(ctx context.Context, s *session, in ingredientsReadInput) (any, error) {
	kind := ingredientKinds[in.Kind]
	query := pageQuery(in.Page, in.PerPage)
	if in.Search != "" {
		if in.Kind != foodsKind {
			return nil, notApplicable("search", in.Kind)
		}
		query.Set("search", in.Search)
	}
	raw, err := s.call(ctx, http.MethodGet, kind.base, query, nil)
	if err != nil {
		return nil, err
	}
	return view.Page(raw, kind.fields)
}

func getIngredient(ctx context.Context, s *session, in ingredientsReadInput) (any, error) {
	if err := need("get", "id", in.ID); err != nil {
		return nil, err
	}
	path, err := mealie.PathOf(ingredientKinds[in.Kind].base, in.ID)
	if err != nil {
		return nil, err
	}
	raw, err := s.call(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	return trimmed(raw, false)
}

func emptyIngredients(ctx context.Context, s *session, in ingredientsReadInput) (any, error) {
	if in.Kind == unitsKind {
		return nil, toolFailure(`Action "empty" does not apply to units.`)
	}
	kind := ingredientKinds[in.Kind]
	raw, err := s.call(ctx, http.MethodGet, kind.base+"/empty", nil, nil)
	if err != nil {
		return nil, err
	}
	return view.List(raw, kind.fields)
}

func parseIngredients(ctx context.Context, s *session, in ingredientsReadInput) (any, error) {
	if len(in.Ingredients) == 0 {
		return nil, need("parse", "ingredients", "")
	}
	if len(in.Ingredients) > maxParseEntries {
		return nil, toolFailure("parse takes at most 50 ingredients.")
	}
	body := map[string]any{"parser": "nlp", "ingredients": in.Ingredients}
	raw, err := s.call(ctx, http.MethodPost, "/api/parser/ingredients", nil, body)
	if err != nil {
		return nil, err
	}
	return trimmed(raw, false)
}

type ingredientsWriteInput struct {
	actionField
	Kind         string         `json:"kind" jsonschema:"Which kind: foods, units or labels."`
	ID           string         `json:"id,omitempty" jsonschema:"update, delete: id."`
	Name         string         `json:"name,omitempty" jsonschema:"create: name."`
	PluralName   string         `json:"pluralName,omitempty" jsonschema:"create: plural name (foods and units only)."`
	Abbreviation string         `json:"abbreviation,omitempty" jsonschema:"create: abbreviation (units only)."`
	Color        string         `json:"color,omitempty" jsonschema:"create: color such as #ff0000 (labels only)."`
	FromID       string         `json:"fromId,omitempty" jsonschema:"merge: id of the entry to merge away."`
	ToID         string         `json:"toId,omitempty" jsonschema:"merge: id of the entry that keeps the references."`
	Fields       map[string]any `json:"fields,omitempty" jsonschema:"update: fields to change, using Mealie's names."`
}

func addIngredientsWrite(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:    "ingredients_write",
		summary: "Create, change, delete, and merge foods, units, and labels.",
	}, []action[ingredientsWriteInput]{
		{name: "create", help: "Create a food, unit or label. Needs name. Optional: pluralName (foods, units), abbreviation (units), color (labels).", run: createIngredient},
		{name: "update", help: "Change a food, unit or label. Needs id, fields.", run: updateIngredient},
		{name: "delete", help: "Delete a food, unit or label. Needs id.", run: deleteIngredient},
		{name: "merge", help: "Move everything from one entry into another and delete the first. Needs fromId, toId.", run: mergeIngredients},
	}, map[string][]any{"kind": ingredientKindNames})
}

func createBody(in ingredientsWriteInput) (map[string]string, error) {
	if err := need("create", "name", in.Name); err != nil {
		return nil, err
	}
	body := map[string]string{"name": in.Name}
	optional := []struct {
		field, value string
		kinds        []string
	}{
		{"pluralName", in.PluralName, []string{foodsKind, unitsKind}},
		{"abbreviation", in.Abbreviation, []string{unitsKind}},
		{"color", in.Color, []string{labelsKind}},
	}
	for _, o := range optional {
		if o.value == "" {
			continue
		}
		if !slices.Contains(o.kinds, in.Kind) {
			return nil, notApplicable(o.field, in.Kind)
		}
		body[o.field] = o.value
	}
	return body, nil
}

func createIngredient(ctx context.Context, s *session, in ingredientsWriteInput) (any, error) {
	body, err := createBody(in)
	if err != nil {
		return nil, err
	}
	kind := ingredientKinds[in.Kind]
	return createCompact(ctx, s, in.Kind, kind.base, body, kind.fields)
}

func updateIngredient(ctx context.Context, s *session, in ingredientsWriteInput) (any, error) {
	kind := ingredientKinds[in.Kind]
	return updateByID(ctx, s, "update", kind.base, in.ID, in.Fields, kind.fields)
}

func deleteIngredient(ctx context.Context, s *session, in ingredientsWriteInput) (any, error) {
	return deleteByID(ctx, s, "delete", ingredientKinds[in.Kind].base, in.ID)
}

func mergeIngredients(ctx context.Context, s *session, in ingredientsWriteInput) (any, error) {
	for _, id := range []struct{ field, value string }{{"fromId", in.FromID}, {"toId", in.ToID}} {
		if err := need("merge", id.field, id.value); err != nil {
			return nil, err
		}
		if err := mealie.CheckSegment(id.value); err != nil {
			return nil, err
		}
	}
	kind := ingredientKinds[in.Kind]
	body := map[string]string{kind.mergeFrom: in.FromID, kind.mergeTo: in.ToID}
	if _, err := s.call(ctx, kind.mergeMethod, kind.base+"/merge", nil, body); err != nil {
		return nil, err
	}
	return ok(nil), nil
}
