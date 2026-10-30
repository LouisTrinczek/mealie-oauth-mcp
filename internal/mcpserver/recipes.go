package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/view"
)

const (
	recipesPath        = "/api/recipes"
	defaultSuggestions = 10
	maxSuggestions     = 50
	tagsPath           = "/api/organizers/tags/slug"
	categoriesPath     = "/api/organizers/categories/slug"
)

var recipeFields = []string{"id", "slug", "name", "description", "totalTime", "rating", "recipeCategory", "tags"}

var requireAllParams = []string{"requireAllCategories", "requireAllTags", "requireAllTools", "requireAllFoods"}

type recipesReadInput struct {
	actionField
	Query          string   `json:"query,omitempty" jsonschema:"search: text to search for."`
	Categories     []string `json:"categories,omitempty" jsonschema:"search: category slugs or ids."`
	Tags           []string `json:"tags,omitempty" jsonschema:"search: tag slugs or ids."`
	Tools          []string `json:"tools,omitempty" jsonschema:"search: tool slugs or ids. suggest: tool ids (organizers_read lists them)."`
	Foods          []string `json:"foods,omitempty" jsonschema:"search, suggest: food ids (foods have no slugs; ingredients_read lists them)."`
	RequireAll     bool     `json:"requireAll,omitempty" jsonschema:"search: require all given categories, tags, tools and foods instead of any."`
	OrderBy        string   `json:"orderBy,omitempty" jsonschema:"search: e.g. name, createdAt, rating, lastMade, random."`
	OrderDirection string   `json:"orderDirection,omitempty" jsonschema:"search: sort direction."`
	Page           int      `json:"page,omitempty" jsonschema:"search: page number, starting at 1."`
	PerPage        int      `json:"perPage,omitempty" jsonschema:"search: results per page (default 20, max 100)."`
	Slug           string   `json:"slug,omitempty" jsonschema:"get: recipe slug or id."`
	Full           bool     `json:"full,omitempty" jsonschema:"get: return the complete recipe instead of the trimmed one."`
	Limit          int      `json:"limit,omitempty" jsonschema:"suggest: number of suggestions (default 10, max 50)."`
}

func addRecipesRead(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:    "recipes_read",
		summary: "Find and read recipes. Use search to find slugs, then get for details.",
		read:    true,
	}, []action[recipesReadInput]{
		{name: "search", help: "Search recipes. Optional: query, categories, tags, tools, foods, requireAll, orderBy, orderDirection, page, perPage.", run: searchRecipes},
		{name: "get", help: "Read one recipe. Needs slug. Optional: full.", run: getRecipe},
		{name: "suggest", help: "Suggest recipes you can make with the given foods and tools. Optional: foods, tools, limit.", run: suggestRecipes},
	}, map[string][]any{"orderDirection": {"asc", "desc"}})
}

type recipesWriteInput struct {
	actionField
	Slug        string         `json:"slug,omitempty" jsonschema:"Recipe slug or id."`
	Name        string         `json:"name,omitempty" jsonschema:"create: recipe name. duplicate: name of the copy."`
	URL         string         `json:"url,omitempty" jsonschema:"import_url, set_image_url, add_asset_url: the URL to fetch."`
	Fields      map[string]any `json:"fields,omitempty" jsonschema:"update: recipe fields to change, using Mealie's names, e.g. description, recipeIngredient, recipeInstructions."`
	IncludeTags bool           `json:"includeTags,omitempty" jsonschema:"import_url: also import the page's tags."`
	Timestamp   string         `json:"timestamp,omitempty" jsonschema:"mark_made: when it was made, RFC 3339 (default now)."`
	ExpiresAt   string         `json:"expiresAt,omitempty" jsonschema:"share: when the link expires, RFC 3339."`
	Slugs       []string       `json:"slugs,omitempty" jsonschema:"bulk_tag, bulk_categorize, bulk_delete: recipe slugs."`
	Categories  []string       `json:"categories,omitempty" jsonschema:"bulk_categorize: category slugs."`
	Tags        []string       `json:"tags,omitempty" jsonschema:"bulk_tag: tag slugs."`
}

func addRecipesWrite(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:      "recipes_write",
		summary:   "Create, import, change, and delete recipes. Use recipes_read first to find slugs.",
		openWorld: true,
	}, []action[recipesWriteInput]{
		{name: "create", help: "Create an empty recipe. Needs name. Returns its slug.", run: createRecipe},
		{name: "import_url", help: "Import a recipe from a web page. Needs url. Optional: includeTags. Returns its slug.", run: importRecipe},
		{name: "update", help: "Change recipe fields. Needs slug, fields.", run: updateRecipe},
		{name: "duplicate", help: "Copy a recipe. Needs slug. Optional: name.", run: duplicateRecipe},
		{name: "delete", help: "Delete a recipe. Needs slug.", run: deleteRecipe},
		{name: "mark_made", help: "Set the recipe's last-made date only; it adds no timeline entry. Use history_write made_it to also log it on the timeline. Needs slug. Optional: timestamp.", run: markMadeAction},
		{name: "set_image_url", help: "Set a recipe image from a URL. Needs slug, url.", run: setRecipeImage},
		{name: "add_asset_url", help: "Attach a file from a URL to a recipe. Needs slug, url.", run: addRecipeAsset},
		{name: "share", help: "Create a share link. Needs slug. Optional: expiresAt.", run: shareRecipe},
		{name: "bulk_tag", help: "Add tags to many recipes. Needs slugs, tags.", run: bulkTag},
		{name: "bulk_categorize", help: "Add categories to many recipes. Needs slugs, categories.", run: bulkCategorize},
		{name: "bulk_delete", help: "Delete many recipes. Needs slugs.", run: bulkDelete},
	}, nil)
}

func callRecipe(ctx context.Context, s *session, method, slug, suffix string, body any) (json.RawMessage, error) {
	path, err := mealie.PathOf(recipesPath, slug)
	if err != nil {
		return nil, err
	}
	return s.call(ctx, method, path+suffix, nil, body)
}

func newRecipeSlug(raw json.RawMessage) (any, error) {
	var slug string
	if err := json.Unmarshal(raw, &slug); err != nil {
		return nil, fmt.Errorf("decoding the new recipe slug: %w", err)
	}
	return map[string]string{"slug": slug}, nil
}

func createRecipe(ctx context.Context, s *session, in recipesWriteInput) (any, error) {
	if err := need("create", "name", in.Name); err != nil {
		return nil, err
	}
	raw, err := s.call(ctx, http.MethodPost, recipesPath, nil, map[string]string{"name": in.Name})
	if err != nil {
		return nil, err
	}
	return newRecipeSlug(raw)
}

func importRecipe(ctx context.Context, s *session, in recipesWriteInput) (any, error) {
	if err := need("import_url", "url", in.URL); err != nil {
		return nil, err
	}
	raw, err := s.call(ctx, http.MethodPost, recipesPath+"/create/url", nil, map[string]any{"url": in.URL, "includeTags": in.IncludeTags})
	if err != nil {
		return nil, err
	}
	return newRecipeSlug(raw)
}

func updateRecipe(ctx context.Context, s *session, in recipesWriteInput) (any, error) {
	if err := need("update", "slug", in.Slug); err != nil {
		return nil, err
	}
	if len(in.Fields) == 0 {
		return nil, needPresent("update", "fields", false)
	}
	raw, err := callRecipe(ctx, s, http.MethodPatch, in.Slug, "", in.Fields)
	if err != nil {
		return nil, err
	}
	return compactWrite(raw, recipeFields), nil
}

func duplicateRecipe(ctx context.Context, s *session, in recipesWriteInput) (any, error) {
	if err := need("duplicate", "slug", in.Slug); err != nil {
		return nil, err
	}
	body := map[string]string{}
	if in.Name != "" {
		body["name"] = in.Name
	}
	raw, err := callRecipe(ctx, s, http.MethodPost, in.Slug, "/duplicate", body)
	if err != nil {
		return nil, err
	}
	return compactWrite(raw, recipeFields), nil
}

func deleteRecipe(ctx context.Context, s *session, in recipesWriteInput) (any, error) {
	if err := need("delete", "slug", in.Slug); err != nil {
		return nil, err
	}
	if _, err := callRecipe(ctx, s, http.MethodDelete, in.Slug, "", nil); err != nil {
		return nil, err
	}
	return ok(nil), nil
}

func markMade(ctx context.Context, s *session, slug, timestamp string) error {
	_, err := callRecipe(ctx, s, http.MethodPatch, slug, "/last-made", map[string]string{"timestamp": timestamp})
	return err
}

func markMadeAction(ctx context.Context, s *session, in recipesWriteInput) (any, error) {
	if err := need("mark_made", "slug", in.Slug); err != nil {
		return nil, err
	}
	if err := markMade(ctx, s, in.Slug, s.timestampOrNow(in.Timestamp)); err != nil {
		return nil, err
	}
	return ok(nil), nil
}

func postRecipeURL(ctx context.Context, s *session, action, suffix string, in recipesWriteInput) (json.RawMessage, error) {
	if err := need(action, "slug", in.Slug); err != nil {
		return nil, err
	}
	if err := need(action, "url", in.URL); err != nil {
		return nil, err
	}
	return callRecipe(ctx, s, http.MethodPost, in.Slug, suffix, map[string]string{"url": in.URL})
}

func setRecipeImage(ctx context.Context, s *session, in recipesWriteInput) (any, error) {
	if _, err := postRecipeURL(ctx, s, "set_image_url", "/image", in); err != nil {
		return nil, err
	}
	return ok(nil), nil
}

func addRecipeAsset(ctx context.Context, s *session, in recipesWriteInput) (any, error) {
	raw, err := postRecipeURL(ctx, s, "add_asset_url", "/assets/url", in)
	if err != nil {
		return nil, err
	}
	return ok(raw), nil
}

func shareRecipe(ctx context.Context, s *session, in recipesWriteInput) (any, error) {
	if err := need("share", "slug", in.Slug); err != nil {
		return nil, err
	}
	id, err := recipeID(ctx, s, in.Slug)
	if err != nil {
		return nil, err
	}
	body := map[string]string{"recipeId": id}
	if in.ExpiresAt != "" {
		body["expiresAt"] = in.ExpiresAt
	}
	raw, err := s.call(ctx, http.MethodPost, "/api/shared/recipes", nil, body)
	if err != nil {
		return nil, err
	}
	var shared map[string]any
	if err := json.Unmarshal(raw, &shared); err != nil {
		return nil, fmt.Errorf("decoding the share link: %w", err)
	}
	return view.Compact(shared, []string{"id", "expiresAt"}), nil
}

func checkSlugs(slugs []string) error {
	for _, slug := range slugs {
		if err := mealie.CheckSegment(slug); err != nil {
			return err
		}
	}
	return nil
}

func organizers(ctx context.Context, s *session, prefix string, slugs []string) ([]map[string]any, error) {
	found := make([]map[string]any, 0, len(slugs))
	for _, slug := range slugs {
		path, err := mealie.PathOf(prefix, slug)
		if err != nil {
			return nil, err
		}
		raw, err := s.call(ctx, http.MethodGet, path, nil, nil)
		if err != nil {
			return nil, err
		}
		var organizer map[string]any
		if err := json.Unmarshal(raw, &organizer); err != nil {
			return nil, fmt.Errorf("decoding organizer %q: %w", slug, err)
		}
		found = append(found, view.Compact(organizer, []string{"id", "name", "slug"}))
	}
	return found, nil
}

type bulkTarget struct {
	action       string
	field        string
	lookupPrefix string
	bulkPath     string
}

var (
	tagTarget      = bulkTarget{action: "bulk_tag", field: "tags", lookupPrefix: tagsPath, bulkPath: recipesPath + "/bulk-actions/tag"}
	categoryTarget = bulkTarget{action: "bulk_categorize", field: "categories", lookupPrefix: categoriesPath, bulkPath: recipesPath + "/bulk-actions/categorize"}
)

func bulkOrganize(ctx context.Context, s *session, target bulkTarget, slugs, organizerSlugs []string) (any, error) {
	if len(slugs) == 0 {
		return nil, needPresent(target.action, "slugs", false)
	}
	if len(organizerSlugs) == 0 {
		return nil, needPresent(target.action, target.field, false)
	}
	if err := checkSlugs(slugs); err != nil {
		return nil, err
	}
	resolved, err := organizers(ctx, s, target.lookupPrefix, organizerSlugs)
	if err != nil {
		return nil, err
	}
	if _, err := s.call(ctx, http.MethodPost, target.bulkPath, nil, map[string]any{"recipes": slugs, target.field: resolved}); err != nil {
		return nil, err
	}
	return ok(nil), nil
}

func bulkTag(ctx context.Context, s *session, in recipesWriteInput) (any, error) {
	return bulkOrganize(ctx, s, tagTarget, in.Slugs, in.Tags)
}

func bulkCategorize(ctx context.Context, s *session, in recipesWriteInput) (any, error) {
	return bulkOrganize(ctx, s, categoryTarget, in.Slugs, in.Categories)
}

func bulkDelete(ctx context.Context, s *session, in recipesWriteInput) (any, error) {
	if len(in.Slugs) == 0 {
		return nil, needPresent("bulk_delete", "slugs", false)
	}
	if err := checkSlugs(in.Slugs); err != nil {
		return nil, err
	}
	if _, err := s.call(ctx, http.MethodPost, recipesPath+"/bulk-actions/delete", nil, map[string]any{"recipes": in.Slugs}); err != nil {
		return nil, err
	}
	return ok(nil), nil
}

func recipeID(ctx context.Context, s *session, slug string) (string, error) {
	raw, err := getRecipeRaw(ctx, s, slug)
	if err != nil {
		return "", err
	}
	var recipe struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &recipe); err != nil {
		return "", fmt.Errorf("decoding recipe %q: %w", slug, err)
	}
	if recipe.ID == "" {
		return "", toolFailure(fmt.Sprintf("Mealie returned recipe %q without an id.", slug))
	}
	return recipe.ID, nil
}

func getRecipeRaw(ctx context.Context, s *session, slug string) (json.RawMessage, error) {
	path, err := mealie.PathOf(recipesPath, slug)
	if err != nil {
		return nil, err
	}
	return s.call(ctx, http.MethodGet, path, nil, nil)
}

func searchRecipes(ctx context.Context, s *session, in recipesReadInput) (any, error) {
	query := pageQuery(in.Page, in.PerPage)
	setIfNotEmpty(query, "search", in.Query)
	setIfNotEmpty(query, "orderBy", in.OrderBy)
	setIfNotEmpty(query, "orderDirection", in.OrderDirection)
	query["categories"] = in.Categories
	query["tags"] = in.Tags
	query["tools"] = in.Tools
	query["foods"] = in.Foods
	if in.RequireAll {
		for _, param := range requireAllParams {
			query.Set(param, "true")
		}
	}
	raw, err := s.call(ctx, http.MethodGet, recipesPath, query, nil)
	if err != nil {
		return nil, err
	}
	return view.Page(raw, recipeFields)
}

func getRecipe(ctx context.Context, s *session, in recipesReadInput) (any, error) {
	if err := need("get", "slug", in.Slug); err != nil {
		return nil, err
	}
	raw, err := getRecipeRaw(ctx, s, in.Slug)
	if err != nil {
		return nil, err
	}
	return trimmed(raw, in.Full)
}

func suggestRecipes(ctx context.Context, s *session, in recipesReadInput) (any, error) {
	limit := defaultSuggestions
	if in.Limit > 0 {
		limit = min(in.Limit, maxSuggestions)
	}
	query := url.Values{"foods": in.Foods, "tools": in.Tools, "limit": {strconv.Itoa(limit)}}
	raw, err := s.call(ctx, http.MethodGet, recipesPath+"/suggestions", query, nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return nil, fmt.Errorf("decoding recipe suggestions: %w", err)
	}
	items := make([]map[string]any, 0, len(response.Items))
	for _, entry := range response.Items {
		recipe, _ := entry["recipe"].(map[string]any)
		if len(recipe) == 0 {
			continue
		}
		suggestion := view.Compact(entry, []string{"missingFoods", "missingTools"})
		suggestion["recipe"] = view.Compact(recipe, recipeFields)
		items = append(items, suggestion)
	}
	return map[string]any{"items": items}, nil
}
