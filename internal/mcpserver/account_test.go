package mcpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

const (
	householdBody  = `{"id":"h-1","slug":"family","name":"Family","groupId":"g-1","group":"home","preferences":{"privateHousehold":true,"firstDayOfWeek":0,"recipePublic":false,"id":"p-1"},"webhooks":[],"users":[{"id":"u-1","username":"alice","fullName":"Alice"}],"createdAt":"2026-01-01T00:00:00","updateAt":null}`
	groupBody      = `{"id":"g-1","slug":"home","name":"Home","households":[{"id":"h-1","slug":"family","name":"Family"}],"preferences":{"privateGroup":true,"groupId":"g-1"},"categories":[],"webhooks":null}`
	householdsBody = `{"page":2,"per_page":100,"total":102,"total_pages":2,"items":[{"id":"h-1","slug":"family","name":"Family","groupId":"g-1","preferences":{"privateHousehold":true}},{"id":"h-2","slug":"guests","name":"Guests","groupId":"g-1"}],"next":null,"previous":"/households?page=1"}`
)

func TestAccountRead_Actions(t *testing.T) {
	tests := []struct {
		name   string
		args   map[string]any
		path   string
		body   string
		query  url.Values
		golden string
	}{
		{"Household", map[string]any{"action": "household"}, "/api/households/self", householdBody, url.Values{}, "account_household"},
		{"Group", map[string]any{"action": "group"}, "/api/groups/self", groupBody, url.Values{}, "account_group"},
		{
			"Households",
			map[string]any{"action": "households", "page": 2, "perPage": 500},
			"/api/groups/households",
			householdsBody,
			url.Values{"page": {"2"}, "perPage": {"100"}},
			"account_households",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, tt.path, http.StatusOK, tt.body)
			cs := connectTools(t, api)

			res := callTool(t, cs, "account_read", tt.args)
			if res.IsError {
				t.Fatalf("tool error: %s", resultText(t, res))
			}
			if res.StructuredContent != nil {
				t.Errorf("structured content = %v, want only text", res.StructuredContent)
			}
			assertGolden(t, tt.golden, resultText(t, res))
			seen := api.requests()
			if len(seen) != 1 || seen[0].Path != tt.path || !reflect.DeepEqual(seen[0].Query, tt.query) {
				t.Errorf("requests = %+v, want one GET %s with query %v", seen, tt.path, tt.query)
			}
		})
	}
}

func TestAccountRead_HouseholdsPagesByDefault(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/groups/households", http.StatusOK, householdsBody)
	cs := connectTools(t, api)

	if res := callTool(t, cs, "account_read", map[string]any{"action": "households"}); res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}
	want := url.Values{"page": {"1"}, "perPage": {"20"}}
	if seen := api.requests(); len(seen) != 1 || !reflect.DeepEqual(seen[0].Query, want) {
		t.Errorf("requests = %+v, want query %v", seen, want)
	}
}

func TestAccountRead_HouseholdsReportsMealieErrors(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/groups/households", http.StatusForbidden, `{"detail":"no"}`)
	cs := connectTools(t, api)

	requireToolError(t, callTool(t, cs, "account_read", map[string]any{"action": "households"}),
		"Your Mealie user is not allowed to do this (403): no.")
}

func TestAccountRead_FullReturnsRawObject(t *testing.T) {
	tests := []struct {
		action string
		path   string
		body   string
	}{
		{"household", "/api/households/self", "{\n  \"id\": \"h-1\",\n  \"name\": \"Family\",\n  \"extra\": null,\n  \"webhooks\": []\n}"},
		{"group", "/api/groups/self", "{\n  \"id\": \"g-1\",\n  \"name\": \"Home\",\n  \"categories\": []\n}"},
	}
	for _, tt := range tests {
		t.Run(tt.action, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, tt.path, http.StatusOK, tt.body)
			cs := connectTools(t, api)

			res := callTool(t, cs, "account_read", map[string]any{"action": tt.action, "full": true})
			if res.IsError {
				t.Fatalf("tool error: %s", resultText(t, res))
			}
			var want bytes.Buffer
			if err := json.Compact(&want, []byte(tt.body)); err != nil {
				t.Fatalf("compacting fake body: %v", err)
			}
			if got := resultText(t, res); got != want.String() {
				t.Errorf("text = %q, want %q", got, want.String())
			}
		})
	}
}

func TestAccountRead_KeepsHTMLCharacters(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/households/self", http.StatusOK, `{"id":"h-1","name":"A & B"}`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "account_read", map[string]any{"action": "household"})

	if got := resultText(t, res); !strings.Contains(got, `"name":"A & B"`) {
		t.Errorf("text = %q, want the name A & B unescaped", got)
	}
}
