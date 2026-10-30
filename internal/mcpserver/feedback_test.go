package mcpserver

import (
	"net/http"
	"testing"
)

const (
	selfBody     = `{"id":"u-1","username":"alice"}`
	commentBody  = `{"recipeId":"r-1","text":"Tasty","id":"c-1","createdAt":"2026-10-30","updatedAt":"2026-10-30","userId":"u-1","user":{"id":"u-1","username":"alice","admin":false,"fullName":"Alice A"}}`
	commentsBody = `[` + commentBody + `]`
	ratingsBody  = `{"ratings":[{"recipeId":"r-1","rating":4.5,"isFavorite":true},{"recipeId":"r-2","rating":null,"isFavorite":false}]}`
)

func TestFeedbackRead_Comments(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/soup/comments", http.StatusOK, commentsBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "feedback_read", map[string]any{"action": "comments", "slug": "soup"})
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	assertGolden(t, "feedback_comments", resultText(t, res))
}

func TestFeedbackRead_Ratings(t *testing.T) {
	for _, kind := range []string{"ratings", "favorites"} {
		t.Run(kind, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, "/api/users/self/"+kind, http.StatusOK, ratingsBody)
			cs := connectTools(t, api)

			res := callTool(t, cs, "feedback_read", map[string]any{"action": kind})
			if res.IsError {
				t.Fatalf("tool error: %s", resultText(t, res))
			}
			assertGolden(t, "feedback_ratings", resultText(t, res))
		})
	}
}

func TestFeedbackRead_Failures(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"comments without slug", map[string]any{"action": "comments"}, `Action "comments" needs slug.`},
		{"comments bad slug", map[string]any{"action": "comments", "slug": "a/b"}, `"a/b" is not a valid slug or id.`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			cs := connectTools(t, api)

			requireToolError(t, callTool(t, cs, "feedback_read", tt.args), tt.want)
			if seen := api.requests(); len(seen) != 0 {
				t.Errorf("requests = %+v, want none", seen)
			}
		})
	}
}

func TestFeedbackRead_RatingsOfTheWrongShape(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/users/self/ratings", http.StatusOK, `[]`)
	cs := connectTools(t, api)

	if res := callTool(t, cs, "feedback_read", map[string]any{"action": "ratings"}); !res.IsError {
		t.Errorf("IsError = false, want an error for %s", resultText(t, res))
	}
}

func TestFeedbackWrite_Comment(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
	api.on(http.MethodPost, "/api/comments", http.StatusCreated, commentBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "feedback_write", map[string]any{"action": "comment", "slug": "soup", "text": "Tasty"})
	want := `{"id":"c-1","text":"Tasty","user":{"id":"u-1","username":"alice"}}`
	if got := resultText(t, res); res.IsError || got != want {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, want)
	}
	seen := api.requests()
	if len(seen) != 2 || seen[1].Method != http.MethodPost || seen[1].Path != "/api/comments" || seen[1].Body != `{"recipeId":"r-1","text":"Tasty"}` {
		t.Errorf("requests = %+v, want recipe lookup then POST /api/comments", seen)
	}
}

func TestFeedbackWrite_UpdateComment(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/comments/c-1", http.StatusOK, commentBody)
	api.on(http.MethodPut, "/api/comments/c-1", http.StatusOK, `{"id":"c-1","text":"Better","userId":"u-1","extra":"kept","user":{"id":"u-1","username":"alice","admin":false}}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "feedback_write", map[string]any{"action": "update_comment", "id": "c-1", "text": "Better"})
	if got, want := resultText(t, res), `{"id":"c-1","text":"Better","user":{"id":"u-1","username":"alice"}}`; res.IsError || got != want {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, want)
	}
	const mergedComment = `{"createdAt":"2026-10-30","id":"c-1","recipeId":"r-1","text":"Better","updatedAt":"2026-10-30","user":{"id":"u-1","username":"alice","admin":false,"fullName":"Alice A"},"userId":"u-1"}`
	seen := api.requests()
	if len(seen) != 2 || seen[1].Method != http.MethodPut || seen[1].Body != mergedComment {
		t.Errorf("requests = %+v, want GET then PUT with the new text", seen)
	}
}

func TestFeedbackWrite_DeleteComment(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodDelete, "/api/comments/c-1", http.StatusOK, commentBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "feedback_write", map[string]any{"action": "delete_comment", "id": "c-1"})
	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
	}
}

func TestFeedbackWrite_RateUsesSelfID(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/users/self", http.StatusOK, selfBody)
	api.on(http.MethodPost, "/api/users/u-1/ratings/soup", http.StatusOK, `{"message":"saved"}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "feedback_write", map[string]any{"action": "rate", "slug": "soup", "rating": 0})
	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
	}
	seen := api.requests()
	if len(seen) != 2 || seen[1].Method != http.MethodPost || seen[1].Path != "/api/users/u-1/ratings/soup" || seen[1].Body != `{"rating":0}` {
		t.Errorf("requests = %+v, want self lookup then POST /api/users/u-1/ratings/soup with rating 0", seen)
	}
}

func TestFeedbackWrite_RatingRange(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	for _, rating := range []float64{-1, 5.5} {
		res := callTool(t, cs, "feedback_write", map[string]any{"action": "rate", "slug": "soup", "rating": rating})
		requireToolError(t, res, "rating must be between 0 and 5.")
	}
	if seen := api.requests(); len(seen) != 0 {
		t.Errorf("requests = %+v, want none", seen)
	}
}

func TestFeedbackWrite_FavoriteAndUnfavorite(t *testing.T) {
	tests := []struct{ action, method string }{
		{"favorite", http.MethodPost},
		{"unfavorite", http.MethodDelete},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, "/api/users/self", http.StatusOK, selfBody)
			api.on(tt.method, "/api/users/u-1/favorites/soup", http.StatusOK, `{"message":"done"}`)
			cs := connectTools(t, api)

			res := callTool(t, cs, "feedback_write", map[string]any{"action": tt.action, "slug": "soup"})
			if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
				t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
			}
			seen := api.requests()
			if len(seen) != 2 || seen[1].Method != tt.method || seen[1].Path != "/api/users/u-1/favorites/soup" || seen[1].Body != "" {
				t.Errorf("requests = %+v, want self lookup then %s /api/users/u-1/favorites/soup", seen, tt.method)
			}
		})
	}
}

func TestFeedbackWrite_Failures(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"comment without slug", map[string]any{"action": "comment", "text": "x"}, `Action "comment" needs slug.`},
		{"comment without text", map[string]any{"action": "comment", "slug": "soup"}, `Action "comment" needs text.`},
		{"update without id", map[string]any{"action": "update_comment", "text": "x"}, `Action "update_comment" needs id.`},
		{"update without text", map[string]any{"action": "update_comment", "id": "c-1"}, `Action "update_comment" needs text.`},
		{"delete without id", map[string]any{"action": "delete_comment"}, `Action "delete_comment" needs id.`},
		{"rate without slug", map[string]any{"action": "rate", "rating": 3}, `Action "rate" needs slug.`},
		{"rate without rating", map[string]any{"action": "rate", "slug": "soup"}, `Action "rate" needs rating.`},
		{"rate bad slug", map[string]any{"action": "rate", "slug": "a/b", "rating": 3}, `"a/b" is not a valid slug or id.`},
		{"favorite without slug", map[string]any{"action": "favorite"}, `Action "favorite" needs slug.`},
		{"unfavorite bad slug", map[string]any{"action": "unfavorite", "slug": ".."}, `".." is not a valid slug or id.`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			cs := connectTools(t, api)

			requireToolError(t, callTool(t, cs, "feedback_write", tt.args), tt.want)
			if seen := api.requests(); len(seen) != 0 {
				t.Errorf("requests = %+v, want none", seen)
			}
		})
	}
}

func TestFeedbackWrite_SelfFailures(t *testing.T) {
	t.Run("session ended", func(t *testing.T) {
		api := newFakeMealie(t)
		api.on(http.MethodGet, "/api/users/self", http.StatusUnauthorized, `{"detail":"nope"}`)
		cs := connectTools(t, api)

		res := callTool(t, cs, "feedback_write", map[string]any{"action": "favorite", "slug": "soup"})
		requireToolError(t, res, string(errSessionEnded))
	})
	t.Run("self without id", func(t *testing.T) {
		api := newFakeMealie(t)
		api.on(http.MethodGet, "/api/users/self", http.StatusOK, `{"username":"alice"}`)
		cs := connectTools(t, api)

		res := callTool(t, cs, "feedback_write", map[string]any{"action": "favorite", "slug": "soup"})
		requireToolError(t, res, `"" is not a valid slug or id.`)
	})
}

func TestFeedback_MealieErrorsReachTheAgent(t *testing.T) {
	tests := []struct {
		name, tool, method, path string
		args                     map[string]any
	}{
		{"comments", "feedback_read", http.MethodGet, "/api/recipes/soup/comments", map[string]any{"action": "comments", "slug": "soup"}},
		{"ratings", "feedback_read", http.MethodGet, "/api/users/self/ratings", map[string]any{"action": "ratings"}},
		{"comment recipe", "feedback_write", http.MethodGet, "/api/recipes/soup", map[string]any{"action": "comment", "slug": "soup", "text": "x"}},
		{"rate", "feedback_write", http.MethodPost, "/api/users/u-1/ratings/soup", map[string]any{"action": "rate", "slug": "soup", "rating": 3}},
		{"favorite", "feedback_write", http.MethodPost, "/api/users/u-1/favorites/soup", map[string]any{"action": "favorite", "slug": "soup"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, "/api/users/self", http.StatusOK, selfBody)
			api.on(tt.method, tt.path, http.StatusNotFound, `{"detail":"gone"}`)
			cs := connectTools(t, api)

			requireToolError(t, callTool(t, cs, tt.tool, tt.args), "Mealie could not find that (404): gone. Check the slug or id; search or list to find it.")
		})
	}
}
