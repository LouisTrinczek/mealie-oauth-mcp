package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/view"
)

const (
	commentsPath = "/api/comments"
	usersPath    = "/api/users"
	maxRating    = 5
)

var (
	commentFields = []string{"id", "text", "user"}
	ratingFields  = []string{"recipeId", "rating", "isFavorite"}
)

type feedbackReadInput struct {
	actionField
	Slug string `json:"slug,omitempty" jsonschema:"comments: recipe slug or id."`
}

func addFeedbackRead(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:    "feedback_read",
		summary: "Read recipe comments and your own ratings and favorites.",
		read:    true,
	}, []action[feedbackReadInput]{
		{name: "comments", help: "List the comments on a recipe. Needs slug.", run: listComments},
		{name: "ratings", help: "List your own recipe ratings.", run: userRatings("ratings")},
		{name: "favorites", help: "List your favorite recipes.", run: userRatings("favorites")},
	}, nil)
}

func listComments(ctx context.Context, s *session, in feedbackReadInput) (any, error) {
	if err := need("comments", "slug", in.Slug); err != nil {
		return nil, err
	}
	path, err := mealie.PathOf(recipesPath, in.Slug, "comments")
	if err != nil {
		return nil, err
	}
	raw, err := s.call(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	return view.List(raw, commentFields)
}

func userRatings(kind string) func(ctx context.Context, s *session, in feedbackReadInput) (any, error) {
	return func(ctx context.Context, s *session, _ feedbackReadInput) (any, error) {
		raw, err := s.call(ctx, http.MethodGet, usersPath+"/self/"+kind, nil, nil)
		if err != nil {
			return nil, err
		}
		var wrapper struct {
			Ratings json.RawMessage `json:"ratings"`
		}
		if err := json.Unmarshal(raw, &wrapper); err != nil {
			return nil, fmt.Errorf("decoding %s: %w", kind, err)
		}
		if len(wrapper.Ratings) == 0 || string(wrapper.Ratings) == "null" {
			return map[string]any{"items": []any{}, "total": 0}, nil
		}
		return view.List(wrapper.Ratings, ratingFields)
	}
}

type feedbackWriteInput struct {
	actionField
	Slug   string   `json:"slug,omitempty" jsonschema:"comment, rate, favorite, unfavorite: recipe slug or id."`
	Text   string   `json:"text,omitempty" jsonschema:"comment, update_comment: the comment text."`
	ID     string   `json:"id,omitempty" jsonschema:"update_comment, delete_comment: comment id."`
	Rating *float64 `json:"rating,omitempty" jsonschema:"rate: your rating from 0 to 5."`
}

func addFeedbackWrite(srv *mcp.Server, d Deps) error {
	return addActionTool(srv, d, toolSpec{
		name:    "feedback_write",
		summary: "Comment on, rate, and favorite recipes as the current user.",
	}, []action[feedbackWriteInput]{
		{name: "comment", help: "Comment on a recipe. Needs slug, text.", run: addComment},
		{name: "update_comment", help: "Change a comment. Needs id, text.", run: updateComment},
		{name: "delete_comment", help: "Delete a comment. Needs id.", run: deleteComment},
		{name: "rate", help: "Rate a recipe. Needs slug, rating (0 to 5).", run: rateRecipe},
		{name: "favorite", help: "Favorite a recipe. Needs slug.", run: favoriteRecipe(http.MethodPost, "favorite")},
		{name: "unfavorite", help: "Remove a recipe from your favorites. Needs slug.", run: favoriteRecipe(http.MethodDelete, "unfavorite")},
	}, nil)
}

func addComment(ctx context.Context, s *session, in feedbackWriteInput) (any, error) {
	if err := need("comment", "slug", in.Slug); err != nil {
		return nil, err
	}
	if err := need("comment", "text", in.Text); err != nil {
		return nil, err
	}
	id, err := recipeID(ctx, s, in.Slug)
	if err != nil {
		return nil, err
	}
	return createCompact(ctx, s, commentsPath, map[string]string{"recipeId": id, "text": in.Text}, commentFields)
}

func updateComment(ctx context.Context, s *session, in feedbackWriteInput) (any, error) {
	if err := need("update_comment", "text", in.Text); err != nil {
		return nil, err
	}
	return updateByID(ctx, s, "update_comment", commentsPath, in.ID, map[string]any{"text": in.Text}, commentFields)
}

func deleteComment(ctx context.Context, s *session, in feedbackWriteInput) (any, error) {
	return deleteByID(ctx, s, "delete_comment", commentsPath, in.ID)
}

func userRecipePath(ctx context.Context, s *session, kind, slug string) (string, error) {
	if err := mealie.CheckSegment(slug); err != nil {
		return "", err
	}
	self, err := s.self(ctx)
	if err != nil {
		return "", err
	}
	return mealie.PathOf(usersPath, self.ID, kind, slug)
}

func rateRecipe(ctx context.Context, s *session, in feedbackWriteInput) (any, error) {
	if err := need("rate", "slug", in.Slug); err != nil {
		return nil, err
	}
	if err := needPresent("rate", "rating", in.Rating != nil); err != nil {
		return nil, err
	}
	if *in.Rating < 0 || *in.Rating > maxRating {
		return nil, toolFailure(fmt.Sprintf("rating must be between 0 and %d.", maxRating))
	}
	path, err := userRecipePath(ctx, s, "ratings", in.Slug)
	if err != nil {
		return nil, err
	}
	if _, err := s.call(ctx, http.MethodPost, path, nil, map[string]float64{"rating": *in.Rating}); err != nil {
		return nil, err
	}
	return ok(nil), nil
}

func favoriteRecipe(method, actionName string) func(ctx context.Context, s *session, in feedbackWriteInput) (any, error) {
	return func(ctx context.Context, s *session, in feedbackWriteInput) (any, error) {
		if err := need(actionName, "slug", in.Slug); err != nil {
			return nil, err
		}
		path, err := userRecipePath(ctx, s, "favorites", in.Slug)
		if err != nil {
			return nil, err
		}
		if _, err := s.call(ctx, method, path, nil, nil); err != nil {
			return nil, err
		}
		return ok(nil), nil
	}
}
