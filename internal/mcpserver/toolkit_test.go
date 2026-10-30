package mcpserver

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
)

const sessionEnded = "Your Mealie session ended. Reconnect this app to log in again."

type entriesInput struct {
	Entries []struct {
		EntryType string `json:"entryType,omitempty"`
	} `json:"entries,omitempty"`
}

type channelInput struct {
	actionField
	Feed chan int `json:"feed,omitempty"`
}

func testSession(t *testing.T, client MealieClient, tokens *fakeTokens) *session {
	t.Helper()
	info := &auth.TokenInfo{Extra: map[string]any{"session_id": testSessionID}}
	return &session{tokens: tokens, mealie: client, info: info, token: secret.FromString(testMealieToken)}
}

func schemaJSON(t *testing.T, schema any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	return out
}

func TestToolkit_ActionEnum(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectTools(t, api)

	schema := schemaJSON(t, findTool(t, cs, "account_read").InputSchema)
	action := schema["properties"].(map[string]any)["action"].(map[string]any)
	if want := []any{"household", "group", "households"}; !reflect.DeepEqual(action["enum"], want) {
		t.Errorf("action enum = %v, want %v", action["enum"], want)
	}
	if want := []any{"action"}; !reflect.DeepEqual(schema["required"], want) {
		t.Errorf("required = %v, want %v", schema["required"], want)
	}

	res := callTool(t, cs, "account_read", map[string]any{"action": "nope"})
	if !res.IsError {
		t.Fatal("IsError = false for an unknown action")
	}
	if text := resultText(t, res); !strings.HasPrefix(text, `validating "arguments"`) {
		t.Errorf("message = %q, want a schema validation error", text)
	}
	if got := api.requests(); len(got) != 0 {
		t.Errorf("Mealie got %d requests for an unknown action", len(got))
	}
}

func TestInputSchema_EnumsReachArrayItems(t *testing.T) {
	schema, err := inputSchema[entriesInput](map[string][]any{"entryType": {"a", "b"}})
	if err != nil {
		t.Fatalf("inputSchema: %v", err)
	}
	got := schema.Properties["entries"].Items.Properties["entryType"].Enum
	if want := []any{"a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("entries.items.properties.entryType.enum = %v, want %v", got, want)
	}
}

func TestInputSchema_Error(t *testing.T) {
	if _, err := inputSchema[channelInput](nil); err == nil || !strings.Contains(err.Error(), "inferring") {
		t.Errorf("inputSchema error = %v, want an inferring error", err)
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	err := addActionTool(srv, Deps{}, toolSpec{name: "broken"}, []action[channelInput]{{name: "x"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "inferring") {
		t.Errorf("addActionTool error = %v, want an inferring error", err)
	}
}

func TestToolkit_ErrorMapping(t *testing.T) {
	tests := []struct {
		status int
		want   string
	}{
		{http.StatusNotFound, "Mealie could not find that (404): msg. Check the slug or id; search or list to find it."},
		{http.StatusForbidden, "Your Mealie user is not allowed to do this (403): msg."},
		{http.StatusUnprocessableEntity, "Mealie rejected the request (422): msg. Fix the arguments and try again."},
		{http.StatusConflict, "Mealie rejected the request (409): msg. Fix the arguments and try again."},
		{http.StatusTeapot, "Mealie returned an error (418): msg."},
		{http.StatusFound, "Mealie redirected the request. Check that MEALIE_URL points directly at Mealie."},
		{http.StatusUnauthorized, sessionEnded},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, "/api/households/self", tt.status, `{"detail":"msg"}`)
			tokens := &fakeTokens{}
			cs := connectInMemory(t, toolDeps(t, api, tokens))

			requireToolError(t, callTool(t, cs, "account_read", map[string]any{"action": "household"}), tt.want)
			_, reported := tokens.calls()
			if wantReported := tt.status == http.StatusUnauthorized; (len(reported) == 1) != wantReported {
				t.Errorf("ReportUnauthorized calls = %v, want reported only on 401", reported)
			}
		})
	}
}

func TestSession_MapsClientErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"BadSegment", &mealie.SegmentError{Value: "a/b"}, `"a/b" is not a valid slug or id.`},
		{"Unavailable", mealie.ErrUnavailable, "Mealie is unavailable right now. Try again shortly."},
		{"BadRequest", &mealie.APIError{Status: http.StatusBadRequest, Message: "msg"}, "Mealie rejected the request (400): msg. Fix the arguments and try again."},
		{"Other", errors.New("transport failed: Bearer " + testMealieToken), "Mealie request failed. Try again."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testSession(t, &fakeMealie{err: tt.err}, &fakeTokens{})

			if _, err := s.call(t.Context(), http.MethodGet, "/api/x", nil, nil); err == nil || err.Error() != tt.want {
				t.Errorf("call error = %v, want %q", err, tt.want)
			}
			if _, err := s.self(t.Context()); err == nil || err.Error() != tt.want {
				t.Errorf("self error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSession_Self(t *testing.T) {
	profile := &fakeMealie{user: mealie.User{ID: "u-1"}}
	s := testSession(t, profile, &fakeTokens{})

	user, err := s.self(t.Context())
	if err != nil || user.ID != "u-1" {
		t.Errorf("self = %+v, %v; want user u-1", user, err)
	}
	if got := profile.seenTokens(); len(got) != 1 || got[0] != testMealieToken {
		t.Errorf("Self got tokens %v, want the session's Mealie token", got)
	}

	tokens := &fakeTokens{}
	s = testSession(t, &fakeMealie{err: &mealie.APIError{Status: http.StatusUnauthorized}}, tokens)
	if _, err := s.self(t.Context()); err == nil || err.Error() != sessionEnded {
		t.Errorf("self error = %v, want the session-ended text", err)
	}
	if _, reported := tokens.calls(); len(reported) != 1 || reported[0] != testSessionID {
		t.Errorf("ReportUnauthorized calls = %v, want [%s]", reported, testSessionID)
	}
}

func TestToolkit_TokenFailureMakesNoRequest(t *testing.T) {
	api := newFakeMealie(t)
	cs := connectInMemory(t, toolDeps(t, api, &fakeTokens{err: auth.ErrInvalidToken}))

	requireToolError(t, callTool(t, cs, "account_read", map[string]any{"action": "household"}), sessionEnded)
	if got := api.requests(); len(got) != 0 {
		t.Errorf("Mealie got %d requests without a Mealie token", len(got))
	}
}

func TestMergeUpdate_PreservesUnknownFields(t *testing.T) {
	const path = "/api/households/mealplans/1"
	api := newFakeMealie(t)
	api.on(http.MethodGet, path, http.StatusOK, `{"id":"1","name":"A","futureField":{"k":[1,2]},"other":true}`)
	api.on(http.MethodPut, path, http.StatusOK, `{"id":"1","name":"B"}`)
	s := testSession(t, api.client(t), &fakeTokens{})

	raw, err := mergeUpdate(t.Context(), s, path, map[string]any{"name": "B"})
	if err != nil {
		t.Fatalf("mergeUpdate: %v", err)
	}
	if string(raw) != `{"id":"1","name":"B"}` {
		t.Errorf("result = %s, want the PUT response", raw)
	}
	seen := api.requests()
	if len(seen) != 2 || seen[0].Method != http.MethodGet || seen[1].Method != http.MethodPut {
		t.Fatalf("requests = %+v, want GET then PUT", seen)
	}
	var got, want any
	if err := json.Unmarshal([]byte(seen[1].Body), &got); err != nil {
		t.Fatalf("PUT body %q: %v", seen[1].Body, err)
	}
	if err := json.Unmarshal([]byte(`{"id":"1","name":"B","futureField":{"k":[1,2]},"other":true}`), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("PUT body = %s, want %v", seen[1].Body, want)
	}
}

func TestMergeUpdate_StopsBeforePutOnFailure(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"Missing", http.StatusNotFound, `{"detail":"gone"}`, "Mealie could not find that (404): gone."},
		{"NotAnObject", http.StatusOK, `[1,2]`, "reading /api/x before the update"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newFakeMealie(t)
			api.on(http.MethodGet, "/api/x", tt.status, tt.body)
			s := testSession(t, api.client(t), &fakeTokens{})

			_, err := mergeUpdate(t.Context(), s, "/api/x", map[string]any{"name": "B"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %v, want it to contain %q", err, tt.want)
			}
			if got := api.requests(); len(got) != 1 {
				t.Errorf("requests = %+v, want only the GET", got)
			}
		})
	}
}

func TestToolkit_TooLarge(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/households/self", http.StatusOK, `{"name":"`+strings.Repeat("x", 70000)+`"}`)
	cs := connectTools(t, api)

	requireToolError(t, callTool(t, cs, "account_read", map[string]any{"action": "household"}),
		"The result is too large (69 KiB). Use a smaller perPage, a narrower filter, or leave out full.")
}

func TestToolkit_DescriptionListsActions(t *testing.T) {
	tool := findTool(t, connectTools(t, newFakeMealie(t)), "account_read")

	if !strings.HasPrefix(tool.Description, "Read the current user's household, group, and the households in the group.\n\nActions:\n") {
		t.Errorf("description = %q, want the summary then the action list", tool.Description)
	}
	for _, line := range []string{"\n- household: ", "\n- group: ", "\n- households: "} {
		if !strings.Contains(tool.Description, line) {
			t.Errorf("description %q lacks %q", tool.Description, line)
		}
	}
	a := tool.Annotations
	if a == nil || !a.ReadOnlyHint || !a.IdempotentHint || a.DestructiveHint != nil || a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations = %+v, want read-only, idempotent, closed world", a)
	}
}

func TestToolSpec_WriteAnnotations(t *testing.T) {
	tests := []struct {
		name      string
		openWorld bool
	}{
		{"ClosedWorld", false},
		{"OpenWorld", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := toolSpec{openWorld: tt.openWorld}.annotations()
			if a.ReadOnlyHint || a.IdempotentHint || a.DestructiveHint == nil || !*a.DestructiveHint {
				t.Errorf("annotations = %+v, want a destructive write", a)
			}
			if a.OpenWorldHint == nil || *a.OpenWorldHint != tt.openWorld {
				t.Errorf("OpenWorldHint = %v, want %v", a.OpenWorldHint, tt.openWorld)
			}
		})
	}
}

func TestNeed(t *testing.T) {
	if err := need("get", "slug", "soup"); err != nil {
		t.Errorf("need with a value = %v, want nil", err)
	}
	err := need("get", "slug", "")
	if err == nil || err.Error() != `Action "get" needs slug.` {
		t.Errorf("need without a value = %v", err)
	}
}

func TestTrimmed(t *testing.T) {
	raw := json.RawMessage(`{"name":"Soup","description":"","groupId":"g"}`)
	tests := []struct {
		name string
		full bool
		want string
	}{
		{"Full", true, `{"name":"Soup","description":"","groupId":"g"}`},
		{"Trimmed", false, `{"name":"Soup"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := trimmed(raw, tt.full)
			if err != nil {
				t.Fatalf("trimmed: %v", err)
			}
			encoded, err := json.Marshal(got)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(encoded) != tt.want {
				t.Errorf("trimmed = %s, want %s", encoded, tt.want)
			}
		})
	}
}

func TestOk(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want any
	}{
		{"Empty", nil, map[string]bool{"ok": true}},
		{"Body", json.RawMessage(`{"id":"1","note":""}`), json.RawMessage(`{"id":"1"}`)},
		{"NotJSON", json.RawMessage(`done`), json.RawMessage(`done`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ok(tt.raw); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ok = %#v, want %#v", got, tt.want)
			}
		})
	}
}
