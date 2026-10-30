package oauth

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRenderHTML_TemplateFailure(t *testing.T) {
	env := newTestServer(t)
	broken := template.Must(template.New("broken").Parse("{{.Missing}}"))
	recorder := httptest.NewRecorder()

	env.srv.renderHTML(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil),
		broken, http.StatusOK, contentSecurityPolicy(), errorView{Message: "hello"})

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "hello") {
		t.Fatalf("partial page was written: %s", recorder.Body.String())
	}
	if !strings.Contains(env.logs.String(), `"msg":"rendering page"`) {
		t.Fatalf("template failure not logged: %s", env.logs.String())
	}
}

func TestRedirectToClient_KeepsExistingQuery(t *testing.T) {
	env := newTestServer(t)
	recorder := httptest.NewRecorder()

	env.srv.redirectToClient(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil),
		"https://client.example.com/cb?tenant=a", "st", url.Values{"code": {"c"}})

	location, err := url.Parse(recorder.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parsing Location: %v", err)
	}
	q := location.Query()
	if q.Get("tenant") != "a" || q.Get("code") != "c" || q.Get("state") != "st" || q.Get("iss") != env.http.URL {
		t.Fatalf("Location = %s, want the original query plus code, state and iss", location)
	}
}
