package mcpserver

import (
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

const (
	entryBodyJSON     = `{"id":5,"date":"2026-10-31","entryType":"dinner","title":"","text":"","recipeId":"r-1","recipeScale":1,"groupId":"g-1","userId":"u-1","householdId":"h-1","recipe":{"id":"r-1","slug":"soup","name":"Soup","description":"long","image":"x"}}`
	entriesPageBody   = `{"page":1,"per_page":20,"total":1,"total_pages":1,"items":[` + entryBodyJSON + `],"next":null,"previous":null}`
	ruleBodyJSON      = `{"id":"u-1","day":"monday","entryType":"dinner","queryFilterString":"tags.name CONTAINS ALL [\"quick\"]","groupId":"g-1"}`
	rulesPageBody     = `{"page":1,"per_page":20,"total":1,"total_pages":1,"items":[` + ruleBodyJSON + `],"next":null,"previous":null}`
	mealplansEndpoint = "/api/households/mealplans"
)

func TestMealplansRead_ListDefaultsToComingWeek(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, mealplansEndpoint, http.StatusOK, entriesPageBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_read", map[string]any{"action": "list"})
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	assertGolden(t, "mealplans_list", resultText(t, res))
	want := url.Values{
		"start_date": {"2026-10-30"}, "end_date": {"2026-11-05"},
		"orderBy": {"date"}, "orderDirection": {"asc"}, "page": {"1"}, "perPage": {"20"},
	}
	if seen := api.requests(); len(seen) != 1 || !reflect.DeepEqual(seen[0].Query, want) {
		t.Errorf("requests = %+v, want one GET with query %v", seen, want)
	}
}

func TestMealplansRead_ListPastRange(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, mealplansEndpoint, http.StatusOK, entriesPageBody)
	cs := connectTools(t, api)

	callTool(t, cs, "mealplans_read", map[string]any{"action": "list", "start": "2026-09-01", "end": "2026-09-30", "page": 2, "perPage": 5})
	want := url.Values{
		"start_date": {"2026-09-01"}, "end_date": {"2026-09-30"},
		"orderBy": {"date"}, "orderDirection": {"asc"}, "page": {"2"}, "perPage": {"5"},
	}
	if seen := api.requests(); len(seen) != 1 || !reflect.DeepEqual(seen[0].Query, want) {
		t.Errorf("requests = %+v, want one GET with query %v", seen, want)
	}
}

func TestMealplansRead_ListOnlyStartDefaultsEndToAWeekLater(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, mealplansEndpoint, http.StatusOK, entriesPageBody)
	cs := connectTools(t, api)

	callTool(t, cs, "mealplans_read", map[string]any{"action": "list", "start": "2026-02-25"})
	if seen := api.requests(); len(seen) != 1 || seen[0].Query.Get("end_date") != "2026-03-03" {
		t.Errorf("requests = %+v, want end_date 2026-03-03", seen)
	}
}

func TestMealplansRead_ListRejectsBadDates(t *testing.T) {
	tests := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "list", "start": "tomorrow"}, "start must be YYYY-MM-DD."},
		{map[string]any{"action": "list", "end": "31/10/2026"}, "end must be YYYY-MM-DD."},
	}
	for _, tt := range tests {
		api := newFakeMealie(t)
		cs := connectTools(t, api)

		res := callTool(t, cs, "mealplans_read", tt.args)
		if got := resultText(t, res); !res.IsError || got != tt.want {
			t.Errorf("result = %q (error %v), want %q", got, res.IsError, tt.want)
		}
		if seen := api.requests(); len(seen) != 0 {
			t.Errorf("requests = %+v, want none", seen)
		}
	}
}

func TestMealplansRead_EndBeforeStart(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_read", map[string]any{"action": "list", "start": "2026-11-10", "end": "2026-11-09"})
	requireToolError(t, res, "end must not be before start.")
	if seen := api.requests(); len(seen) != 0 {
		t.Errorf("requests = %+v, want none", seen)
	}
}

func TestMealplansRead_Today(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, mealplansEndpoint+"/today", http.StatusOK, `[`+entryBodyJSON+`]`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_read", map[string]any{"action": "today"})
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	assertGolden(t, "mealplans_today", resultText(t, res))
}

func TestMealplansRead_TodayRejectsNonList(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, mealplansEndpoint+"/today", http.StatusOK, entryBodyJSON)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_read", map[string]any{"action": "today"})
	requireToolError(t, res, string(errUnreadableRead))
}

func TestMealplansRead_Rules(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, mealplansEndpoint+"/rules", http.StatusOK, rulesPageBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_read", map[string]any{"action": "rules", "page": 3})
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	assertGolden(t, "mealplans_rules", resultText(t, res))
	want := url.Values{"page": {"3"}, "perPage": {"20"}}
	if seen := api.requests(); len(seen) != 1 || !reflect.DeepEqual(seen[0].Query, want) {
		t.Errorf("requests = %+v, want one GET with query %v", seen, want)
	}
}

func TestMealplansRead_ListFailure(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, mealplansEndpoint, http.StatusInternalServerError, `{"detail":"boom"}`)
	cs := connectTools(t, api)

	if res := callTool(t, cs, "mealplans_read", map[string]any{"action": "list"}); !res.IsError {
		t.Error("IsError = false, want the Mealie failure")
	}
}

func TestMealplansWrite_CreateWithSlugResolvesID(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
	api.on(http.MethodPost, mealplansEndpoint, http.StatusCreated, entryBodyJSON)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "create", "date": "2026-10-31", "slug": "soup", "text": "double"})
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	assertGolden(t, "mealplans_create", resultText(t, res))
	seen := api.requests()
	want := `{"date":"2026-10-31","entryType":"dinner","recipeId":"r-1","text":"double"}`
	if len(seen) != 2 || seen[1].Method != http.MethodPost || seen[1].Path != mealplansEndpoint || seen[1].Body != want {
		t.Errorf("requests = %+v, want recipe lookup then POST %s", seen, want)
	}
}

func TestMealplansWrite_CreateWithTitleAndEntryType(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, mealplansEndpoint, http.StatusCreated, entryBodyJSON)
	cs := connectTools(t, api)

	callTool(t, cs, "mealplans_write", map[string]any{"action": "create", "date": "2026-10-31", "entryType": "lunch", "title": "Leftovers"})
	want := `{"date":"2026-10-31","entryType":"lunch","title":"Leftovers"}`
	if seen := api.requests(); len(seen) != 1 || seen[0].Body != want {
		t.Errorf("requests = %+v, want POST %s", seen, want)
	}
}

func TestMealplansWrite_CreateValidation(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing date", map[string]any{"action": "create", "title": "x"}, `Action "create" needs date.`},
		{"bad date", map[string]any{"action": "create", "date": "soon", "title": "x"}, "date must be YYYY-MM-DD."},
		{"neither slug nor title", map[string]any{"action": "create", "date": "2026-10-31"}, `Action "create" needs slug or title.`},
		{"bad slug", map[string]any{"action": "create", "date": "2026-10-31", "slug": "a/b"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			cs := connectTools(t, api)

			res := callTool(t, cs, "mealplans_write", tt.args)
			got := resultText(t, res)
			if !res.IsError || (tt.want != "" && got != tt.want) || got == "" {
				t.Errorf("result = %q (error %v), want %q", got, res.IsError, tt.want)
			}
			if seen := api.requests(); len(seen) != 0 {
				t.Errorf("requests = %+v, want none", seen)
			}
		})
	}
}

func TestMealplansWrite_CreateNeedsSlugOrTitle(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "create", "date": "2026-10-31"})
	if got, want := resultText(t, res), `Action "create" needs slug or title.`; !res.IsError || got != want {
		t.Errorf("result = %q (error %v), want %q", got, res.IsError, want)
	}
}

func TestMealplansWrite_CreateFailsWhenRecipeLookupFails(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/soup", http.StatusNotFound, `{"detail":"nope"}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "create", "date": "2026-10-31", "slug": "soup"})
	if !res.IsError || len(api.requests()) != 1 {
		t.Errorf("result = %q (error %v), requests = %+v, want an error after the lookup", resultText(t, res), res.IsError, api.requests())
	}
}

func TestMealplansWrite_BulkCreateReportsProgress(t *testing.T) {
	api := newFakeMealie(t)
	api.onceNextMealplanPost(http.StatusCreated, entryBodyJSON)
	api.onceNextMealplanPost(http.StatusUnprocessableEntity, `{"detail":"bad"}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "bulk_create", "entries": []any{
		map[string]any{"date": "2026-10-31", "title": "A"},
		map[string]any{"date": "2026-11-01", "title": "B"},
		map[string]any{"date": "2026-11-02", "title": "C"},
	}})
	want := "Created 1 of 3 entries; entry 2 (2026-11-01) failed: Mealie rejected the request (422): bad. Fix the arguments and try again."
	if got := resultText(t, res); !res.IsError || got != want {
		t.Errorf("result = %q (error %v), want %q", got, res.IsError, want)
	}
	if seen := api.requests(); len(seen) != 2 || seen[0].Method != http.MethodPost || seen[1].Method != http.MethodPost {
		t.Errorf("requests = %+v, want exactly two POSTs", seen)
	}
}

func TestMealplansWrite_BulkUnreadableReplyCountsAsCreated(t *testing.T) {
	api := newFakeMealie(t)
	api.onceNextMealplanPost(http.StatusCreated, `[]`)
	api.onceNextMealplanPost(http.StatusInternalServerError, `{"detail":"boom"}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "bulk_create", "entries": []any{
		map[string]any{"date": "2026-10-31", "title": "A"},
		map[string]any{"date": "2026-11-01", "title": "B"},
	}})
	requireToolError(t, res, "Created 1 of 2 entries; entry 2 (2026-11-01) failed: "+string(errWriteUnconfirmed))
}

func TestMealplansWrite_BulkCreateValidatesEveryEntryFirst(t *testing.T) {
	tests := []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{"no slug or title", map[string]any{"date": "2026-11-01"}, `Entry 2 (2026-11-01) is invalid: Action "bulk_create" needs slug or title. Nothing was created.`},
		{"bad date", map[string]any{"date": "soon", "title": "B"}, "Entry 2 (soon) is invalid: date must be YYYY-MM-DD. Nothing was created."},
		{"no date", map[string]any{"title": "B"}, `Entry 2 (no date) is invalid: Action "bulk_create" needs date. Nothing was created.`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			cs := connectTools(t, api)

			res := callTool(t, cs, "mealplans_write", map[string]any{"action": "bulk_create", "entries": []any{
				map[string]any{"date": "2026-10-31", "title": "A"}, tt.entry,
			}})
			if got := resultText(t, res); !res.IsError || got != tt.want {
				t.Errorf("result = %q (error %v), want %q", got, res.IsError, tt.want)
			}
			if seen := api.requests(); len(seen) != 0 {
				t.Errorf("requests = %+v, want none", seen)
			}
		})
	}
}

func TestMealplansWrite_BulkCreateResolvesEveryRecipeFirst(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/nope", http.StatusNotFound, `{"detail":"gone"}`)
	api.on(http.MethodPost, mealplansEndpoint, http.StatusCreated, entryBodyJSON)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "bulk_create", "entries": []any{
		map[string]any{"date": "2026-10-31", "title": "A"},
		map[string]any{"date": "2026-11-01", "slug": "nope"},
	}})
	want := "Entry 2 (2026-11-01) is invalid: Mealie could not find that (404): gone. Check the slug or id; search or list to find it. Nothing was created."
	if got := resultText(t, res); !res.IsError || got != want {
		t.Errorf("result = %q (error %v), want %q", got, res.IsError, want)
	}
	if seen := api.requests(); len(seen) != 1 || seen[0].Method != http.MethodGet {
		t.Errorf("requests = %+v, want only the recipe lookup", seen)
	}
}

func TestMealplansWrite_BulkCreateStopsOnMealieFailure(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, mealplansEndpoint, http.StatusUnprocessableEntity, `{"detail":"bad"}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "bulk_create", "entries": []any{
		map[string]any{"date": "2026-10-31", "title": "A"},
		map[string]any{"date": "2026-11-01", "title": "B"},
	}})
	got := resultText(t, res)
	if !res.IsError || !strings.HasPrefix(got, "Created 0 of 2 entries; entry 1 (2026-10-31) failed: Mealie rejected the request (422)") {
		t.Errorf("result = %q (error %v), want a progress report", got, res.IsError)
	}
	if seen := api.requests(); len(seen) != 1 {
		t.Errorf("requests = %+v, want exactly one POST", seen)
	}
}

func TestMealplansWrite_BulkCreateReturnsItems(t *testing.T) {
	const stewEntryBody = `{"id":6,"date":"2026-11-01","entryType":"dinner","recipe":{"id":"r-2","slug":"stew","name":"Stew"}}`
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/recipes/soup", http.StatusOK, soupRecipeBody)
	api.on(http.MethodGet, "/api/recipes/stew", http.StatusOK, `{"id":"r-2","slug":"stew"}`)
	api.onceNextMealplanPost(http.StatusCreated, entryBodyJSON)
	api.onceNextMealplanPost(http.StatusCreated, stewEntryBody)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "bulk_create", "entries": []any{
		map[string]any{"date": "2026-10-31", "slug": "soup", "entryType": "lunch"},
		map[string]any{"date": "2026-11-01", "slug": "stew"},
	}})
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	assertGolden(t, "mealplans_bulk_create", resultText(t, res))
	seen := api.requests()
	if len(seen) != 4 || seen[2].Body != `{"date":"2026-10-31","entryType":"lunch","recipeId":"r-1"}` || seen[3].Body != `{"date":"2026-11-01","entryType":"dinner","recipeId":"r-2"}` {
		t.Errorf("requests = %+v, want the entries posted in order", seen)
	}
}

func TestMealplansWrite_BulkCreateLimit(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	entries := make([]any, 32)
	for i := range entries {
		entries[i] = map[string]any{"date": "2026-10-31", "title": "A"}
	}
	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "bulk_create", "entries": entries})
	if got, want := resultText(t, res), "bulk_create takes at most 31 entries."; !res.IsError || got != want {
		t.Errorf("result = %q (error %v), want %q", got, res.IsError, want)
	}
	if seen := api.requests(); len(seen) != 0 {
		t.Errorf("requests = %+v, want none", seen)
	}
}

func TestMealplansWrite_BulkCreateNeedsEntries(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "bulk_create"})
	if got, want := resultText(t, res), `Action "bulk_create" needs entries.`; !res.IsError || got != want {
		t.Errorf("result = %q (error %v), want %q", got, res.IsError, want)
	}
}

func TestMealplansWrite_EntryTypeEnumReachesEntries(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	schema := schemaJSON(t, findTool(t, cs, "mealplans_write").InputSchema)
	properties := schema["properties"].(map[string]any)
	items := properties["entries"].(map[string]any)["items"].(map[string]any)
	got := items["properties"].(map[string]any)["entryType"].(map[string]any)["enum"]
	want := []any{"breakfast", "lunch", "dinner", "side", "snack", "drink", "dessert"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entries.items.properties.entryType.enum = %v, want %v", got, want)
	}
	if top := properties["entryType"].(map[string]any)["enum"]; !reflect.DeepEqual(top, want) {
		t.Errorf("entryType.enum = %v, want %v", top, want)
	}

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "bulk_create", "entries": []any{map[string]any{"date": "2026-10-31", "title": "A", "entryType": "brunch"}}})
	if !res.IsError || len(api.requests()) != 0 {
		t.Errorf("result = %q (error %v), requests = %+v, want a validation error", resultText(t, res), res.IsError, api.requests())
	}
}

func TestMealplansWrite_Random(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, mealplansEndpoint+"/random", http.StatusCreated, entryBodyJSON)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "random", "date": "2026-10-31"})
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	assertGolden(t, "mealplans_random", resultText(t, res))
	if seen := api.requests(); len(seen) != 1 || seen[0].Body != `{"date":"2026-10-31","entryType":"dinner"}` {
		t.Errorf("requests = %+v, want POST /random with the default entryType", seen)
	}
}

func TestMealplansWrite_RandomValidation(t *testing.T) {
	tests := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"action": "random"}, `Action "random" needs date.`},
		{map[string]any{"action": "random", "date": "x"}, "date must be YYYY-MM-DD."},
	}
	for _, tt := range tests {
		api := newFakeMealie(t)
		cs := connectTools(t, api)

		res := callTool(t, cs, "mealplans_write", tt.args)
		if got := resultText(t, res); !res.IsError || got != tt.want {
			t.Errorf("result = %q (error %v), want %q", got, res.IsError, tt.want)
		}
	}
}

func TestMealplansWrite_RandomFailure(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, mealplansEndpoint+"/random", http.StatusNotFound, `{"detail":"no recipes"}`)
	cs := connectTools(t, api)

	if res := callTool(t, cs, "mealplans_write", map[string]any{"action": "random", "date": "2026-10-31"}); !res.IsError {
		t.Error("IsError = false, want the Mealie failure")
	}
}

func TestMealplansWrite_UpdateMerges(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, mealplansEndpoint+"/5", http.StatusOK, `{"id":5,"date":"2026-10-31","entryType":"dinner","futureField":{"a":1}}`)
	api.on(http.MethodPut, mealplansEndpoint+"/5", http.StatusOK, `{"id":5,"date":"2026-11-02","entryType":"dinner","futureField":{"a":1}}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "update", "id": "5", "fields": map[string]any{"date": "2026-11-02"}})
	want := `{"date":"2026-11-02","entryType":"dinner","id":"5"}`
	if got := resultText(t, res); res.IsError || got != want {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, want)
	}
	seen := api.requests()
	if len(seen) != 2 || seen[1].Method != http.MethodPut || seen[1].Body != `{"date":"2026-11-02","entryType":"dinner","futureField":{"a":1},"id":5}` {
		t.Errorf("requests = %+v, want GET then merged PUT", seen)
	}
}

func TestMealplansWrite_UpdateUnreadableReplyStillSucceeds(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, mealplansEndpoint+"/5", http.StatusOK, `{"id":5,"date":"2026-10-31","entryType":"dinner"}`)
	api.on(http.MethodPut, mealplansEndpoint+"/5", http.StatusOK, `[]`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "update", "id": "5", "fields": map[string]any{"date": "2026-11-02"}})
	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
	}
}

func TestMealplansWrite_DeleteReturnsOKForAnyBody(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodDelete, mealplansEndpoint+"/5", http.StatusOK, entryBodyJSON)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "delete", "id": "5"})
	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
	}
}

func TestMealplansWrite_RuleCreate(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, mealplansEndpoint+"/rules", http.StatusCreated, ruleBodyJSON)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "rule_create", "entryType": "dinner", "queryFilter": `tags.name CONTAINS ALL ["quick"]`})
	if res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	assertGolden(t, "mealplans_rule_create", resultText(t, res))
	want := `{"day":"unset","entryType":"dinner","queryFilterString":"tags.name CONTAINS ALL [\"quick\"]"}`
	if seen := api.requests(); len(seen) != 1 || seen[0].Body != want {
		t.Errorf("requests = %+v, want POST %s", seen, want)
	}
}

func TestMealplansWrite_RuleCreateKeepsGivenDayAndOmitsEmptyEntryType(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, mealplansEndpoint+"/rules", http.StatusCreated, ruleBodyJSON)
	cs := connectTools(t, api)

	callTool(t, cs, "mealplans_write", map[string]any{"action": "rule_create", "day": "monday"})
	if seen := api.requests(); len(seen) != 1 || seen[0].Body != `{"day":"monday","queryFilterString":""}` {
		t.Errorf("requests = %+v, want POST with the given day and no entryType", seen)
	}
}

func TestMealplansWrite_RuleCreateFailure(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, mealplansEndpoint+"/rules", http.StatusUnprocessableEntity, `{"detail":"bad"}`)
	cs := connectTools(t, api)

	if res := callTool(t, cs, "mealplans_write", map[string]any{"action": "rule_create"}); !res.IsError {
		t.Error("IsError = false, want the Mealie failure")
	}
}

func TestMealplansWrite_RuleUpdateMerges(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, mealplansEndpoint+"/rules/u-1", http.StatusOK, `{"id":"u-1","day":"monday","extra":true}`)
	api.on(http.MethodPut, mealplansEndpoint+"/rules/u-1", http.StatusOK, `{"id":"u-1","day":"friday","extra":true}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "rule_update", "id": "u-1", "fields": map[string]any{"day": "friday"}})
	if got, want := resultText(t, res), `{"day":"friday","id":"u-1"}`; res.IsError || got != want {
		t.Errorf("result = %s (error %v), want %s", got, res.IsError, want)
	}
	if seen := api.requests(); len(seen) != 2 || seen[1].Body != `{"day":"friday","extra":true,"id":"u-1"}` {
		t.Errorf("requests = %+v, want GET then merged PUT", seen)
	}
}

func TestMealplansWrite_RuleDeleteReturnsOKForAnyBody(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodDelete, mealplansEndpoint+"/rules/u-1", http.StatusOK, ruleBodyJSON)
	cs := connectTools(t, api)

	res := callTool(t, cs, "mealplans_write", map[string]any{"action": "rule_delete", "id": "u-1"})
	if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
		t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
	}
}

func TestMealplansWrite_IDValidation(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"update needs id", map[string]any{"action": "update", "fields": map[string]any{"a": 1}}, `Action "update" needs id.`},
		{"update needs fields", map[string]any{"action": "update", "id": "5"}, `Action "update" needs fields.`},
		{"rule_update needs fields", map[string]any{"action": "rule_update", "id": "u-1"}, `Action "rule_update" needs fields.`},
		{"delete needs id", map[string]any{"action": "delete"}, `Action "delete" needs id.`},
		{"rule_delete needs id", map[string]any{"action": "rule_delete"}, `Action "rule_delete" needs id.`},
		{"update rejects traversal", map[string]any{"action": "update", "id": "../x", "fields": map[string]any{"a": 1}}, `"../x" is not a valid slug or id.`},
		{"delete rejects traversal", map[string]any{"action": "delete", "id": "a/b"}, `"a/b" is not a valid slug or id.`},
		{"rule_delete rejects traversal", map[string]any{"action": "rule_delete", "id": "%2e%2e"}, `"%2e%2e" is not a valid slug or id.`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			cs := connectTools(t, api)

			res := callTool(t, cs, "mealplans_write", tt.args)
			got := resultText(t, res)
			if !res.IsError || got != tt.want {
				t.Errorf("result = %q (error %v), want %q", got, res.IsError, tt.want)
			}
			if seen := api.requests(); len(seen) != 0 {
				t.Errorf("requests = %+v, want none", seen)
			}
		})
	}
}

func TestMealplansWrite_UnreadableMealieBodiesStillSucceed(t *testing.T) {
	tests := []struct {
		name  string
		args  map[string]any
		route string
	}{
		{"create", map[string]any{"action": "create", "date": "2026-10-31", "title": "x"}, mealplansEndpoint},
		{"random", map[string]any{"action": "random", "date": "2026-10-31"}, mealplansEndpoint + "/random"},
		{"rule_create", map[string]any{"action": "rule_create"}, mealplansEndpoint + "/rules"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodPost, tt.route, http.StatusOK, `[]`)
			cs := connectTools(t, api)

			res := callTool(t, cs, "mealplans_write", tt.args)
			if got := resultText(t, res); res.IsError || got != `{"ok":true}` {
				t.Errorf("result = %s (error %v), want {\"ok\":true}", got, res.IsError)
			}
		})
	}
}

func TestMealplans_MealieFailuresAreReported(t *testing.T) {
	tests := []struct {
		name   string
		tool   string
		args   map[string]any
		method string
		route  string
	}{
		{"today", "mealplans_read", map[string]any{"action": "today"}, http.MethodGet, mealplansEndpoint + "/today"},
		{"rules", "mealplans_read", map[string]any{"action": "rules"}, http.MethodGet, mealplansEndpoint + "/rules"},
		{"update", "mealplans_write", map[string]any{"action": "update", "id": "5", "fields": map[string]any{"a": 1}}, http.MethodGet, mealplansEndpoint + "/5"},
		{"delete", "mealplans_write", map[string]any{"action": "delete", "id": "5"}, http.MethodDelete, mealplansEndpoint + "/5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(tt.method, tt.route, http.StatusNotFound, `{"detail":"gone"}`)
			cs := connectTools(t, api)

			if res := callTool(t, cs, tt.tool, tt.args); !res.IsError {
				t.Error("IsError = false, want the Mealie failure")
			}
		})
	}
}
