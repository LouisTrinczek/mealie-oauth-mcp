package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/allowlist"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/metrics"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/tracing"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/view"
)

const sessionEnded = "Your Mealie session ended. Reconnect this app to log in again."

type entriesInput struct {
	Entries []struct {
		EntryType string `json:"entryType,omitempty"`
	} `json:"entries,omitempty"`
}

type kindsInput struct {
	Kinds []string `json:"kinds"`
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
		name     string
		err      error
		want     string
		wantSelf string
	}{
		{"BadSegment", &mealie.SegmentError{Value: "a/b"}, `"a/b" is not a valid slug or id.`, `"a/b" is not a valid slug or id.`},
		{"Unavailable", mealie.ErrUnavailable, "Mealie is unavailable right now. Try again shortly.", "Mealie is unavailable right now. Try again shortly."},
		{"BadRequest", &mealie.APIError{Status: http.StatusBadRequest, Message: "msg"}, "Mealie rejected the request (400): msg. Fix the arguments and try again.", "Mealie rejected the request (400): msg. Fix the arguments and try again."},
		{"Other", errors.New("transport failed: Bearer " + testMealieToken), "Mealie request failed. Try again; if it keeps failing, check that Mealie is reachable from this server.", "Could not read your Mealie profile. Try again."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testSession(t, &fakeMealie{err: tt.err}, &fakeTokens{})

			if _, err := s.call(t.Context(), http.MethodGet, "/api/x", nil, nil); err == nil || err.Error() != tt.want {
				t.Errorf("call error = %v, want %q", err, tt.want)
			}
			if _, err := s.self(t.Context()); err == nil || err.Error() != tt.wantSelf {
				t.Errorf("self error = %v, want %q", err, tt.wantSelf)
			}
		})
	}
}

func TestSession_UnconfirmedWrites(t *testing.T) {
	const unconfirmed = "Mealie did not confirm the change in time, so it may or may not have been saved. Check with a read or list action before trying again."
	tests := []struct {
		name   string
		method string
		err    error
		want   string
	}{
		{"GET unavailable", http.MethodGet, mealie.ErrUnavailable, "Mealie is unavailable right now. Try again shortly."},
		{"POST timeout", http.MethodPost, context.DeadlineExceeded, unconfirmed},
		{"PUT 502", http.MethodPut, &mealie.APIError{Status: http.StatusBadGateway, Message: "msg"}, unconfirmed},
		{"PATCH unavailable", http.MethodPatch, mealie.ErrUnavailable, unconfirmed},
		{"DELETE 503", http.MethodDelete, &mealie.APIError{Status: http.StatusServiceUnavailable, Message: "msg"}, unconfirmed},
		{"POST 400", http.MethodPost, &mealie.APIError{Status: http.StatusBadRequest, Message: "msg"}, "Mealie rejected the request (400): msg. Fix the arguments and try again."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := testSession(t, &fakeMealie{err: tt.err}, &fakeTokens{})

			if _, err := s.call(t.Context(), tt.method, "/api/x", nil, nil); err == nil || err.Error() != tt.want {
				t.Errorf("call error = %v, want %q", err, tt.want)
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
		{"Null", http.StatusOK, `null`, "reading /api/x before the update: Mealie returned no object"},
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
		{"NotJSON", json.RawMessage(`done`), map[string]bool{"ok": true}},
		{"Null", json.RawMessage(`null`), map[string]bool{"ok": true}},
		{"OnlyNulls", json.RawMessage(`{"x":null}`), map[string]bool{"ok": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ok(tt.raw); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ok = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCompactWrite(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want any
	}{
		{"Object", json.RawMessage(`{"id":"1","name":"A","extra":{"k":1}}`), map[string]any{"id": "1", "name": "A"}},
		{"Empty", nil, map[string]bool{"ok": true}},
		{"Null", json.RawMessage(`null`), map[string]bool{"ok": true}},
		{"Array", json.RawMessage(`[1]`), map[string]bool{"ok": true}},
		{"NotJSON", json.RawMessage(`done`), map[string]bool{"ok": true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := compactWrite(tt.raw, []string{"id", "name"}); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("compactWrite = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestAgentError(t *testing.T) {
	unreadable := fmt.Errorf("decoding page: %w", io.ErrUnexpectedEOF)
	tests := []struct {
		name string
		err  error
		read bool
		want error
	}{
		{"ToolFailure", toolFailure("x"), true, toolFailure("x")},
		{"SegmentError", &mealie.SegmentError{Value: "a/b"}, false, &mealie.SegmentError{Value: "a/b"}},
		{"Denied", &allowlist.DeniedError{Reason: "r"}, false, &allowlist.DeniedError{Reason: "r"}},
		{"TooLarge", &view.TooLargeError{Size: 70000}, true, &view.TooLargeError{Size: 70000}},
		{"UnreadableRead", unreadable, true, errUnreadableRead},
		{"UnreadableWrite", unreadable, false, errUnreadableWrite},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agentError(tt.err, tt.read)
			if got.Error() != tt.want.Error() {
				t.Errorf("agentError = %v, want %v", got, tt.want)
			}
			if !errors.Is(tt.err, unreadable) && !errors.Is(got, tt.err) {
				t.Errorf("agentError = %v, want the error unchanged", got)
			}
		})
	}
}

func TestAgentError_LogsReplacedError(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/households/shopping/lists", http.StatusOK, `null`)
	cs, logs := connectToolsWithLogs(t, api)

	requireToolError(t, callTool(t, cs, "shopping_read", map[string]any{"action": "lists"}), string(errUnreadableRead))

	line := logs.String()
	for _, want := range []string{`level=WARN`, `msg="unreadable Mealie response"`, `tool=shopping_read`, `read=true`, `error="decoding page: Mealie returned null"`} {
		if !strings.Contains(line, want) {
			t.Errorf("log = %q, want it to contain %q", line, want)
		}
	}
	if strings.Contains(line, testMealieToken) {
		t.Errorf("log = %q leaks the Mealie token", line)
	}
}

func TestAgentError_PassThroughLogsNothing(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/households/shopping/lists", http.StatusNotFound, `{"detail":"gone"}`)
	cs, logs := connectToolsWithLogs(t, api)

	if res := callTool(t, cs, "shopping_read", map[string]any{"action": "lists"}); !res.IsError {
		t.Fatalf("result = %s, want a tool error", resultText(t, res))
	}
	if logs.Len() != 0 {
		t.Errorf("log = %q, want nothing logged", logs.String())
	}
}

func TestNew_DefaultsMissingLogger(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/households/shopping/lists", http.StatusOK, `null`)
	cs := connectInMemory(t, Deps{Tokens: &fakeTokens{}, Mealie: api.client(t), Clock: fixedClock})

	requireToolError(t, callTool(t, cs, "shopping_read", map[string]any{"action": "lists"}), string(errUnreadableRead))
}

func TestActionTool_UnreadableReadResponse(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodGet, "/api/households/shopping/lists", http.StatusOK, `null`)
	cs := connectTools(t, api)

	requireToolError(t, callTool(t, cs, "shopping_read", map[string]any{"action": "lists"}), string(errUnreadableRead))
}

func TestCreateCompact_UnreadableBodyIsSuccess(t *testing.T) {
	api := newFakeMealie(t)
	api.on(http.MethodPost, "/api/foods", http.StatusCreated, `[]`)
	cs := connectTools(t, api)

	res := callTool(t, cs, "ingredients_write", map[string]any{"action": "create", "kind": "foods", "name": "salt"})
	if res.IsError || resultText(t, res) != `{"ok":true}` {
		t.Errorf("result = %s (error %v), want {\"ok\":true}", resultText(t, res), res.IsError)
	}
}

func TestCompactWrite_NoListedFieldsIsOK(t *testing.T) {
	got := compactWrite([]byte(`{"other":1}`), []string{"id"})
	if want := (map[string]bool{"ok": true}); !reflect.DeepEqual(got, want) {
		t.Errorf("compactWrite = %#v, want %#v", got, want)
	}
}

func TestInputSchema_EnumOnArrayItems(t *testing.T) {
	schema, err := inputSchema[kindsInput](map[string][]any{"kinds": {"a", "b"}})
	if err != nil {
		t.Fatalf("inputSchema: %v", err)
	}
	kinds := schema.Properties["kinds"]
	if want := []any{"a", "b"}; !reflect.DeepEqual(kinds.Items.Enum, want) {
		t.Errorf("kinds.items.enum = %v, want %v", kinds.Items.Enum, want)
	}
	if kinds.Enum != nil {
		t.Errorf("kinds.enum = %v, want nil", kinds.Enum)
	}
}

func TestNeedPresent(t *testing.T) {
	err := needPresent("rate", "rating", false)
	if err == nil || err.Error() != `Action "rate" needs rating.` {
		t.Errorf("needPresent(false) = %v", err)
	}
	if err := needPresent("rate", "rating", true); err != nil {
		t.Errorf("needPresent(true) = %v, want nil", err)
	}
}

func TestAddSingleTool_ObservesCalls(t *testing.T) {
	registry := newMetrics(t)
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	srv := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	calls := 0
	err := addSingleTool(srv, toolkit{logger: slog.New(slog.DiscardHandler), metrics: registry, tracer: tracing.Tracer(provider)},
		&mcp.Tool{Name: "probe", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}}, nil,
		func(context.Context, *mcp.CallToolRequest, kindsInput) (*mcp.CallToolResult, any, error) {
			calls++
			if calls == 1 {
				return &mcp.CallToolResult{}, nil, nil
			}
			return nil, nil, errors.New("SECRET-DETAIL")
		})
	if err != nil {
		t.Fatalf("addSingleTool: %v", err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	cs := connect(t, clientTransport)

	callTool(t, cs, "probe", map[string]any{"kinds": []string{}})
	callTool(t, cs, "probe", map[string]any{"kinds": []string{}})

	scraped := scrapeRegistry(t, registry)
	for _, line := range []string{
		`mealie_mcp_tool_call_duration_seconds_count{outcome="ok",tool="probe"} 1` + "\n",
		`mealie_mcp_tool_call_duration_seconds_count{outcome="error",tool="probe"} 1` + "\n",
	} {
		if !strings.Contains(scraped, line) {
			t.Errorf("metrics missing %q in %s", line, scraped)
		}
	}

	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("spans = %d, want 2", len(spans))
	}
	for i, span := range spans {
		assertProbeSpan(t, i, span)
	}
	if spans[0].Status.Code != codes.Unset {
		t.Errorf("first span status = %v, want unset", spans[0].Status.Code)
	}
	if spans[1].Status.Code != codes.Error || spans[1].Status.Description != "tool call failed" {
		t.Errorf("failing span status = %+v, want error %q", spans[1].Status, "tool call failed")
	}
}

func assertProbeSpan(t *testing.T, i int, span tracetest.SpanStub) {
	t.Helper()
	if span.Name != "tool probe" {
		t.Errorf("span %d name = %q, want %q", i, span.Name, "tool probe")
	}
	if len(span.Attributes) != 1 || string(span.Attributes[0].Key) != "mcp.tool.name" || span.Attributes[0].Value.AsString() != "probe" {
		t.Errorf("span %d attributes = %v, want only mcp.tool.name=probe", i, span.Attributes)
	}
	if strings.Contains(span.Status.Description, "SECRET-DETAIL") || len(span.Events) != 0 {
		t.Errorf("span %d leaks error detail: %+v", i, span)
	}
}

func newMetrics(t *testing.T) *metrics.Registry {
	t.Helper()
	registry, err := metrics.New()
	if err != nil {
		t.Fatalf("metrics.New: %v", err)
	}
	return registry
}

func scrapeRegistry(t *testing.T, registry *metrics.Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	registry.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func TestWhoami_ObservesCall(t *testing.T) {
	registry := newMetrics(t)
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	profile := &fakeMealie{user: mealie.User{ID: "user-1", Username: "alice"}}
	cs := connectOverHTTP(t, Deps{Tokens: &fakeTokens{}, Mealie: profile, Metrics: registry, Tracing: provider})

	if res := callWhoami(t, cs); res.IsError {
		t.Fatalf("tool error: %s", resultText(t, res))
	}

	if want := `mealie_mcp_tool_call_duration_seconds_count{outcome="ok",tool="whoami"} 1` + "\n"; !strings.Contains(scrapeRegistry(t, registry), want) {
		t.Errorf("metrics missing %q", want)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "tool whoami" {
		t.Fatalf("spans = %v, want one span named tool whoami", spans)
	}
	attrs := spans[0].Attributes
	if len(attrs) != 1 || string(attrs[0].Key) != "mcp.tool.name" || attrs[0].Value.AsString() != "whoami" {
		t.Errorf("span attributes = %v, want only mcp.tool.name=whoami", attrs)
	}
}
