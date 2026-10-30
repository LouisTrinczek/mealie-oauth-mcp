package mcpserver

import (
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
)

var update = flag.Bool("update", false, "rewrite golden files")

const statusUnregistered = 599

type seenRequest struct {
	Method   string
	Path     string
	Query    url.Values
	RawQuery string
	Body     string
}

type cannedResponse struct {
	status int
	body   string
}

type fakeAPI struct {
	t      *testing.T
	server *httptest.Server
	mu     sync.Mutex
	routes map[string]cannedResponse
	queued map[string][]cannedResponse
	seen   []seenRequest
}

func newFakeMealie(t *testing.T) *fakeAPI {
	t.Helper()
	api := &fakeAPI{t: t, routes: map[string]cannedResponse{}, queued: map[string][]cannedResponse{}}
	api.server = httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(api.server.Close)
	return api
}

func (a *fakeAPI) on(method, path string, status int, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.routes[method+" "+path] = cannedResponse{status: status, body: body}
}

func (a *fakeAPI) onceNextMealplanPost(status int, body string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	key := http.MethodPost + " " + mealplansEndpoint
	a.queued[key] = append(a.queued[key], cannedResponse{status: status, body: body})
}

func (a *fakeAPI) requests() []seenRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]seenRequest(nil), a.seen...)
}

func (a *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		a.t.Errorf("reading request body: %v", err)
	}
	path := r.URL.EscapedPath()
	a.mu.Lock()
	a.seen = append(a.seen, seenRequest{Method: r.Method, Path: path, Query: r.URL.Query(), RawQuery: r.URL.RawQuery, Body: string(body)})
	key := r.Method + " " + path
	response, found := a.routes[key]
	if queue := a.queued[key]; len(queue) > 0 {
		response, found = queue[0], true
		a.queued[key] = queue[1:]
	}
	a.mu.Unlock()
	if !found {
		a.t.Errorf("unexpected Mealie request %s %s", r.Method, path)
		w.WriteHeader(statusUnregistered)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.status)
	_, _ = io.WriteString(w, response.body)
}

func (a *fakeAPI) client(t *testing.T) *mealie.Client {
	t.Helper()
	base, err := url.Parse(a.server.URL)
	if err != nil {
		t.Fatalf("parse fake Mealie URL: %v", err)
	}
	return mealie.New(base, mealie.Options{})
}

func fixedClock() time.Time {
	return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
}

func toolDeps(t *testing.T, api *fakeAPI, tokens *fakeTokens) Deps {
	t.Helper()
	return Deps{Tokens: tokens, Mealie: api.client(t), Clock: fixedClock}
}

func connectTools(t *testing.T, api *fakeAPI) *mcp.ClientSession {
	t.Helper()
	return connectInMemory(t, toolDeps(t, api, &fakeTokens{}))
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool %s: %v", name, err)
	}
	return res
}

func findTool(t *testing.T, cs *mcp.ClientSession, name string) *mcp.Tool {
	t.Helper()
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for _, tool := range res.Tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not registered", name)
	return nil
}

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	testdata, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatalf("open testdata: %v", err)
	}
	defer func() { _ = testdata.Close() }()
	file := name + ".golden.json"
	if *update {
		if err := testdata.WriteFile(file, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := testdata.ReadFile(file)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from golden\ngot:  %s\nwant: %s", name, got, want)
	}
}
