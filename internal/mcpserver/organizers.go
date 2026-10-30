package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/view"
)

const cookbooksKind = "cookbooks"

var organizerKindNames = []any{cookbooksKind, "categories", "tags", "tools"}

type organizerKind struct {
	base   string
	fields []string
}

var organizerKinds = map[string]organizerKind{
	cookbooksKind: {base: "/api/households/cookbooks", fields: []string{"id", "slug", "name", "description"}},
	"categories":  {base: "/api/organizers/categories", fields: []string{"id", "slug", "name"}},
	"tags":        {base: "/api/organizers/tags", fields: []string{"id", "slug", "name"}},
	"tools":       {base: "/api/organizers/tools", fields: []string{"id", "slug", "name"}},
}

func notForCookbooks(kind, action string) error {
	if kind == cookbooksKind {
		return toolFailure(fmt.Sprintf("Action %q does not apply to cookbooks.", action))
	}
	return nil
}

type organizersReadInput struct {
	actionField
	Kind    string `json:"kind" jsonschema:"Which organizer: cookbooks, categories, tags or tools."`
	ID      string `json:"id,omitempty" jsonschema:"get: organizer id."`
	Slug    string `json:"slug,omitempty" jsonschema:"get: organizer slug."`
	Search  string `json:"search,omitempty" jsonschema:"list: text to search for (not for cookbooks)."`
	Page    int    `json:"page,omitempty" jsonschema:"list: page number, starting at 1."`
	PerPage int    `json:"perPage,omitempty" jsonschema:"list: results per page (default 20, max 100)."`
	Full    bool   `json:"full,omitempty" jsonschema:"get: return Mealie's full object instead of the compact one."`
}

func addOrganizersRead(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:    "organizers_read",
		summary: "List and read cookbooks, categories, tags, and tools.",
		read:    true,
	}, []action[organizersReadInput]{
		{name: "list", help: "List organizers of a kind. Optional: search (not for cookbooks), page, perPage.", run: listOrganizers},
		{name: "get", help: "Read one organizer; linked recipes are listed by name. Needs slug or id. Optional: full.", run: getOrganizer},
		{name: "empty", help: "List organizers that no recipe uses (not for cookbooks).", run: emptyOrganizers},
	}, map[string][]any{"kind": organizerKindNames})
}

func listOrganizers(ctx context.Context, s *session, in organizersReadInput) (any, error) {
	kind := organizerKinds[in.Kind]
	query := pageQuery(in.Page, in.PerPage)
	if in.Kind != cookbooksKind {
		setIfNotEmpty(query, "search", in.Search)
	}
	raw, err := s.call(ctx, http.MethodGet, kind.base, query, nil)
	if err != nil {
		return nil, err
	}
	return view.Page(raw, kind.fields)
}

func organizerPath(in organizersReadInput) (string, error) {
	base := organizerKinds[in.Kind].base
	switch {
	case in.Slug != "" && in.Kind != cookbooksKind:
		return mealie.PathOf(base, "slug", in.Slug)
	case in.Slug != "":
		return mealie.PathOf(base, in.Slug)
	default:
		return mealie.PathOf(base, in.ID)
	}
}

func getOrganizer(ctx context.Context, s *session, in organizersReadInput) (any, error) {
	if in.Slug == "" && in.ID == "" {
		return nil, toolFailure(`Action "get" needs slug or id.`)
	}
	path, err := organizerPath(in)
	if err != nil {
		return nil, err
	}
	raw, err := s.call(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	if in.Full {
		return view.Trim(raw)
	}
	var organizer map[string]any
	if err := json.Unmarshal(raw, &organizer); err != nil {
		return nil, fmt.Errorf("decoding the %s: %w", in.Kind, err)
	}
	return view.Compact(organizer, slices.Concat(organizerKinds[in.Kind].fields, []string{"recipes"})), nil
}

func emptyOrganizers(ctx context.Context, s *session, in organizersReadInput) (any, error) {
	if err := notForCookbooks(in.Kind, "empty"); err != nil {
		return nil, err
	}
	kind := organizerKinds[in.Kind]
	raw, err := s.call(ctx, http.MethodGet, kind.base+"/empty", nil, nil)
	if err != nil {
		return nil, err
	}
	return view.List(raw, kind.fields)
}

type organizersWriteInput struct {
	actionField
	Kind        string         `json:"kind" jsonschema:"Which organizer: cookbooks, categories, tags or tools."`
	ID          string         `json:"id,omitempty" jsonschema:"update, delete: organizer id."`
	Name        string         `json:"name,omitempty" jsonschema:"create: organizer name."`
	Description string         `json:"description,omitempty" jsonschema:"create: cookbook description (cookbooks only)."`
	QueryFilter string         `json:"queryFilter,omitempty" jsonschema:"create: Mealie recipe filter the cookbook shows (cookbooks only)."`
	FromID      string         `json:"fromId,omitempty" jsonschema:"merge: id of the organizer to merge away."`
	ToID        string         `json:"toId,omitempty" jsonschema:"merge: id of the organizer that keeps the recipes."`
	Fields      map[string]any `json:"fields,omitempty" jsonschema:"update: fields to change, using Mealie's names."`
}

func addOrganizersWrite(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:    "organizers_write",
		summary: "Create, rename, delete, and merge cookbooks, categories, tags, and tools.",
	}, []action[organizersWriteInput]{
		{name: "create", help: "Create an organizer. Needs name. Cookbooks also take description, queryFilter.", run: createOrganizer},
		{name: "update", help: "Change an organizer. Needs id, fields.", run: updateOrganizer},
		{name: "delete", help: "Delete an organizer. Needs id.", run: deleteOrganizer},
		{name: "merge", help: "Move everything from one organizer into another and delete the first (not for cookbooks). Needs fromId, toId.", run: mergeOrganizers},
	}, map[string][]any{"kind": organizerKindNames})
}

func checkCreate(in organizersWriteInput) error {
	if err := need("create", "name", in.Name); err != nil {
		return err
	}
	if in.Kind == cookbooksKind {
		return nil
	}
	if in.Description != "" {
		return toolFailure("description does not apply to " + in.Kind + ".")
	}
	if in.QueryFilter != "" {
		return toolFailure("queryFilter does not apply to " + in.Kind + ".")
	}
	return nil
}

func createOrganizer(ctx context.Context, s *session, in organizersWriteInput) (any, error) {
	if err := checkCreate(in); err != nil {
		return nil, err
	}
	kind := organizerKinds[in.Kind]
	body := map[string]string{"name": in.Name}
	if in.Description != "" {
		body["description"] = in.Description
	}
	if in.QueryFilter != "" {
		body["queryFilterString"] = in.QueryFilter
	}
	return createCompact(ctx, s, in.Kind, kind.base, body, kind.fields)
}

func updateOrganizer(ctx context.Context, s *session, in organizersWriteInput) (any, error) {
	kind := organizerKinds[in.Kind]
	return updateByID(ctx, s, "update", kind.base, in.ID, in.Fields, kind.fields)
}

func deleteOrganizer(ctx context.Context, s *session, in organizersWriteInput) (any, error) {
	return deleteByID(ctx, s, "delete", organizerKinds[in.Kind].base, in.ID)
}

func mergeOrganizers(ctx context.Context, s *session, in organizersWriteInput) (any, error) {
	if err := notForCookbooks(in.Kind, "merge"); err != nil {
		return nil, err
	}
	if err := need("merge", "fromId", in.FromID); err != nil {
		return nil, err
	}
	if err := need("merge", "toId", in.ToID); err != nil {
		return nil, err
	}
	body := map[string]string{"fromId": in.FromID, "toId": in.ToID}
	if _, err := s.call(ctx, http.MethodPost, organizerKinds[in.Kind].base+"/merge", nil, body); err != nil {
		return nil, err
	}
	return ok(nil), nil
}
