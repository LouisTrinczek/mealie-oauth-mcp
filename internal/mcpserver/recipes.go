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
)

var recipeFields = []string{"id", "slug", "name", "description", "totalTime", "rating", "recipeCategory", "tags"}

var requireAllParams = []string{"requireAllCategories", "requireAllTags", "requireAllTools", "requireAllFoods"}

type recipesReadInput struct {
	actionField
	Query          string   `json:"query,omitempty" jsonschema:"search: text to search for."`
	Categories     []string `json:"categories,omitempty" jsonschema:"search: category slugs or ids."`
	Tags           []string `json:"tags,omitempty" jsonschema:"search: tag slugs or ids."`
	Tools          []string `json:"tools,omitempty" jsonschema:"search, suggest: tool slugs or ids."`
	Foods          []string `json:"foods,omitempty" jsonschema:"search, suggest: food slugs or ids."`
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

func setIfNotEmpty(query url.Values, key, value string) {
	if value != "" {
		query.Set(key, value)
	}
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
		suggestion := view.Compact(entry, []string{"missingFoods", "missingTools"})
		suggestion["recipe"] = view.Compact(recipe, recipeFields)
		items = append(items, suggestion)
	}
	return map[string]any{"items": items}, nil
}
