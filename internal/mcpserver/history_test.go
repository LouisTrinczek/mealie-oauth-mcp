package mcpserver

import (
	"net/http"
	"net/url"
	"reflect"
	"testing"
)

const (
	soupRecipeBody = `{"id":"r-1","slug":"soup"}`
	eventBody      = `{"id":"e-1","recipeId":"r-1","subject":"Made it","eventType":"info","eventMessage":"Great","timestamp":"2026-10-30T12:00:00Z","userId":"u-1","groupId":"g-1","image":null}`
	eventsPageBody = `{"page":1,"per_page":20,"total":1,"total_pages":1,"items":[` + eventBody + `],"next":null,"previous":null}`
)

func TestHistoryRead_TimelineFiltersByRecipe(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
	api.on(http.MethodGet, "/api/recipes/timeline/events", http.StatusOK, eventsPageBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "history_read", map[string]any{"action": "timeline", "slug": "soup", "page": 2, "perPage": 5})
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	assertGolden(t, "history_timeline", resultText(t, res))
	want := url.Values{
		"queryFilter": {`recipe_id="r-1"`}, "orderBy": {"timestamp"}, "orderDirection": {"desc"},
		"page": {"2"}, "perPage": {"5"},
	}
	seen := api.requests()
	if len(seen) != 2 || seen[1].Method != http.MethodGet || seen[1].Path != "/api/recipes/timeline/events" || !reflect.DeepEqual(seen[1].Query, want) {
		t.Errorf("requests = %+v, want recipe lookup then timeline GET with query %v", seen, want)
	}
}

func TestHistoryRead_TimelineWithoutSlugReadsAllEvents(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/timeline/events", http.StatusOK, eventsPageBody)
	cs := connectTools(t, api)

	callTool(t, cs, "history_read", map[string]any{"action": "timeline"})
	want := url.Values{"orderBy": {"timestamp"}, "orderDirection": {"desc"}, "page": {"1"}, "perPage": {"20"}}
	if seen := api.requests(); len(seen) != 1 || !reflect.DeepEqual(seen[0].Query, want) {
		t.Errorf("requests = %+v, want one GET with query %v", seen, want)
	}
}

func TestHistoryWrite_AddDefaultsEventType(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
	api.on(http.MethodPost, "/api/recipes/timeline/events", http.StatusCreated, eventBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "history_write", map[string]any{"action": "add", "slug": "soup", "subject": "Tweaked"})
	want := `{"eventMessage":"Great","eventType":"info","id":"e-1","recipeId":"r-1","subject":"Made it","timestamp":"2026-10-30T12:00:00Z"}`
	if got := resultText(t, res); res.IsError || got != want {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, want)
	}
	seen := api.requests()
	if len(seen) != 2 || seen[1].Body != `{"eventType":"info","recipeId":"r-1","subject":"Tweaked"}` {
		t.Errorf("requests = %+v, want a POST with the default eventType", seen)
	}
}

func TestHistoryWrite_AddSendsGivenFields(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
	api.on(http.MethodPost, "/api/recipes/timeline/events", http.StatusCreated, eventBody)
	cs := connectTools(t, api)

	callTool(t, cs, "history_write", map[string]any{
		"action": "add", "slug": "soup", "subject": "Note", "eventType": "comment",
		"message": "Needs salt", "timestamp": "2026-01-02T03:04:05Z",
	})
	want := `{"eventMessage":"Needs salt","eventType":"comment","recipeId":"r-1","subject":"Note","timestamp":"2026-01-02T03:04:05Z"}`
	if seen := api.requests(); len(seen) != 2 || seen[1].Body != want {
		t.Errorf("requests = %+v, want POST body %s", seen, want)
	}
}

func TestHistoryWrite_RejectsUnknownEventType(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res := callTool(t, cs, "history_write", map[string]any{"action": "add", "slug": "soup", "subject": "x", "eventType": "system"})
	if !res.IsError {
		t.Error("IsError = false, want a validation error")
	}
	if seen := api.requests(); len(seen) != 0 {
		t.Errorf("requests = %+v, want none", seen)
	}
}

func TestHistoryWrite_MadeIt(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
	api.on(http.MethodPatch, "/api/recipes/soup/last-made", http.StatusOK, "")
	api.on(http.MethodPost, "/api/recipes/timeline/events", http.StatusCreated, eventBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "history_write", map[string]any{"action": "made_it", "slug": "soup", "message": "Great"})
	want := `{"eventMessage":"Great","eventType":"info","id":"e-1","recipeId":"r-1","subject":"Made it","timestamp":"2026-10-30T12:00:00Z"}`
	if got := resultText(t, res); res.IsError || got != want {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, want)
	}
	wantRequests := []seenRequest{
		{Method: http.MethodGet, Path: "/api/recipes/soup"},
		{Method: http.MethodPatch, Path: "/api/recipes/soup/last-made", Body: `{"timestamp":"2026-10-30T12:00:00Z"}`},
		{Method: http.MethodPost, Path: "/api/recipes/timeline/events", Body: `{"eventMessage":"Great","eventType":"info","recipeId":"r-1","subject":"Made it","timestamp":"2026-10-30T12:00:00Z"}`},
	}
	seen := api.requests()
	if len(seen) != len(wantRequests) {
		t.Fatalf("requests = %+v, want %d", seen, len(wantRequests))
	}
	for i, w := range wantRequests {
		if seen[i].Method != w.Method || seen[i].Path != w.Path || seen[i].Body != w.Body {
			t.Errorf("request %d = %+v, want %+v", i, seen[i], w)
		}
	}
}

func TestHistoryWrite_MadeItUsesGivenTimestampForBothRequests(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
	api.on(http.MethodPatch, "/api/recipes/soup/last-made", http.StatusOK, "")
	api.on(http.MethodPost, "/api/recipes/timeline/events", http.StatusCreated, eventBody)
	cs := connectTools(t, api)

	callTool(t, cs, "history_write", map[string]any{"action": "made_it", "slug": "soup", "timestamp": "2026-01-02T03:04:05Z"})
	seen := api.requests()
	if len(seen) != 3 || seen[1].Body != `{"timestamp":"2026-01-02T03:04:05Z"}` ||
		seen[2].Body != `{"eventType":"info","recipeId":"r-1","subject":"Made it","timestamp":"2026-01-02T03:04:05Z"}` {
		t.Errorf("requests = %+v, want the given timestamp on both writes", seen)
	}
}

func TestHistoryWrite_MadeItUnreadableEventIsSuccess(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
	api.on(http.MethodPatch, "/api/recipes/soup/last-made", http.StatusOK, "")
	api.on(http.MethodPost, "/api/recipes/timeline/events", http.StatusCreated, `[]`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "history_write", map[string]any{"action": "made_it", "slug": "soup"})
	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
	}
	seen := api.requests()
	if len(seen) != 3 || seen[1].Method != http.MethodPatch || seen[2].Method != http.MethodPost {
		t.Errorf("requests = %+v, want lookup, PATCH and POST", seen)
	}
}

func TestHistoryWrite_MadeItFailures(t *testing.T) {
	t.Run("LastMadeFailureSendsNoTimelineEntry", func(t *testing.T) {
		api := newFakeMealie(t)
		api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
		api.on(http.MethodPatch, "/api/recipes/soup/last-made", http.StatusUnprocessableEntity, `{"detail":"bad"}`)
		cs := connectTools(t, api)

		res := callTool(t, cs, "history_write", map[string]any{"action": "made_it", "slug": "soup"})
		if !res.IsError {
			t.Errorf("result = %s, want a tool error", resultText(t, res))
		}
		for _, r := range api.requests() {
			if r.Method == http.MethodPost && r.Path == "/api/recipes/timeline/events" {
				t.Errorf("requests = %+v, want no timeline POST", api.requests())
			}
		}
	})
	t.Run("TimelineFailureReportsPartialSuccess", func(t *testing.T) {
		api := newFakeMealie(t)
		api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
		api.on(http.MethodPatch, "/api/recipes/soup/last-made", http.StatusOK, "")
		api.on(http.MethodPost, "/api/recipes/timeline/events", http.StatusUnprocessableEntity, `{"detail":"bad"}`)
		cs := connectTools(t, api)

		res := callTool(t, cs, "history_write", map[string]any{"action": "made_it", "slug": "soup"})
		want := "Marked soup as made, but adding the timeline entry failed: Mealie rejected the request (422): bad. Fix the arguments and try again."
		if got := resultText(t, res); !res.IsError || got != want {
			t.Errorf("result = %s (error %v), want %s", got, res.IsError, want)
		}
	})
}

func TestHistoryWrite_UpdateMerges(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/timeline/events/e-1", http.StatusOK, eventBody)
	api.on(http.MethodPut, "/api/recipes/timeline/events/e-1", http.StatusOK,
		`{"id":"e-1","recipeId":"r-1","subject":"Renamed","eventType":"info","eventMessage":"Great","timestamp":"2026-10-30T12:00:00Z","image":"img.webp","extra":{"k":1}}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "history_write", map[string]any{"action": "update", "id": "e-1", "fields": map[string]any{"subject": "Renamed"}})
	wantResult := `{"eventMessage":"Great","eventType":"info","id":"e-1","recipeId":"r-1","subject":"Renamed","timestamp":"2026-10-30T12:00:00Z"}`
	if got := resultText(t, res); res.IsError || got != wantResult {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, wantResult)
	}
	seen := api.requests()
	want := `{"eventMessage":"Great","eventType":"info","groupId":"g-1","id":"e-1","image":null,"recipeId":"r-1","subject":"Renamed","timestamp":"2026-10-30T12:00:00Z","userId":"u-1"}`
	if len(seen) != 2 || seen[0].Method != http.MethodGet || seen[1].Method != http.MethodPut || seen[1].Body != want {
		t.Errorf("requests = %+v, want GET then PUT with body %s", seen, want)
	}
}

func TestHistoryWrite_DeleteReturnsOkRegardlessOfBody(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodDelete, "/api/recipes/timeline/events/e-1", http.StatusOK, eventBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "history_write", map[string]any{"action": "delete", "id": "e-1"})
	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
	}
}

func TestHistoryTools_RejectBadInput(t *testing.T) {
	tests := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"AddNeedsSlug", "history_write", map[string]any{"action": "add", "subject": "x"}, `Action "add" needs slug.`},
		{"AddNeedsSubject", "history_write", map[string]any{"action": "add", "slug": "soup"}, `Action "add" needs subject.`},
		{"AddBadSlug", "history_write", map[string]any{"action": "add", "slug": "a/b", "subject": "x"}, `"a/b" is not a valid slug or id.`},
		{"MadeItNeedsSlug", "history_write", map[string]any{"action": "made_it"}, `Action "made_it" needs slug.`},
		{"MadeItBadSlug", "history_write", map[string]any{"action": "made_it", "slug": "a/b"}, `"a/b" is not a valid slug or id.`},
		{"UpdateNeedsID", "history_write", map[string]any{"action": "update", "fields": map[string]any{"x": 1}}, `Action "update" needs id.`},
		{"UpdateNeedsFields", "history_write", map[string]any{"action": "update", "id": "e-1"}, `Action "update" needs fields.`},
		{"UpdateBadID", "history_write", map[string]any{"action": "update", "id": "a/b", "fields": map[string]any{"x": 1}}, `"a/b" is not a valid slug or id.`},
		{"DeleteNeedsID", "history_write", map[string]any{"action": "delete"}, `Action "delete" needs id.`},
		{"DeleteBadID", "history_write", map[string]any{"action": "delete", "id": "../x"}, `"../x" is not a valid slug or id.`},
		{"TimelineBadSlug", "history_read", map[string]any{"action": "timeline", "slug": "a/b"}, `"a/b" is not a valid slug or id.`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			cs := connectTools(t, api)

			res := callTool(t, cs, tt.tool, tt.args)
			if got := resultText(t, res); !res.IsError || got != tt.want {
				t.Errorf("result = %s (error %v), want %s", got, res.IsError, tt.want)
			}
			if seen := api.requests(); len(seen) != 0 {
				t.Errorf("requests = %+v, want none", seen)
			}
		})
	}
}

type cannedRoute struct {
	method string
	path   string
	status int
	body   string
}

func TestHistoryTools_ReportMealieFailures(t *testing.T) {
	soup := cannedRoute{http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody}
	tests := []struct {
		name   string
		tool   string
		args   map[string]any
		routes []cannedRoute
	}{
		{"TimelineRecipeLookup", "history_read", map[string]any{"action": "timeline", "slug": "soup"}, []cannedRoute{{http.MethodGet, "/api/recipes/soup", http.StatusInternalServerError, ""}}},
		{"TimelineGet", "history_read", map[string]any{"action": "timeline"}, []cannedRoute{{http.MethodGet, timelinePath, http.StatusInternalServerError, ""}}},
		{"AddRecipeLookup", "history_write", map[string]any{"action": "add", "slug": "soup", "subject": "x"}, []cannedRoute{{http.MethodGet, "/api/recipes/soup", http.StatusInternalServerError, ""}}},
		{"AddPost", "history_write", map[string]any{"action": "add", "slug": "soup", "subject": "x"}, []cannedRoute{soup, {http.MethodPost, timelinePath, http.StatusInternalServerError, ""}}},
		{"MadeItRecipeLookup", "history_write", map[string]any{"action": "made_it", "slug": "soup"}, []cannedRoute{{http.MethodGet, "/api/recipes/soup", http.StatusInternalServerError, ""}}},
		{"MadeItLastMade", "history_write", map[string]any{"action": "made_it", "slug": "soup"}, []cannedRoute{soup, {http.MethodPatch, "/api/recipes/soup/last-made", http.StatusInternalServerError, ""}}},
		{"UpdateGet", "history_write", map[string]any{"action": "update", "id": "e-1", "fields": map[string]any{"x": 1}}, []cannedRoute{{http.MethodGet, timelinePath + "/e-1", http.StatusNotFound, `{"detail":"gone"}`}}},
		{"DeleteCall", "history_write", map[string]any{"action": "delete", "id": "e-1"}, []cannedRoute{{http.MethodDelete, timelinePath + "/e-1", http.StatusNotFound, `{"detail":"gone"}`}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			for _, route := range tt.routes {
				api.on(route.method, route.path, route.status, route.body)
			}
			cs := connectTools(t, api)

			if res := callTool(t, cs, tt.tool, tt.args); !res.IsError {
				t.Errorf("IsError = false, want a tool error: %s", resultText(t, res))
			}
		})
	}
}
