package allowlist

import (
	"errors"
	"net/url"
	"slices"
	"strings"
	"testing"
)

const (
	notAllowedSuffix = " is not allowed. mealie_api covers recipes, timeline, meal plans, organizers, shopping, foods, units, labels, comments, ratings and favorites, the ingredient parser, and your own user, household and group."
	notAPIPath       = "mealie_api only reaches /api/ paths."
	badSegmentPath   = `The path is not a plain Mealie path: it has an empty, "." or ".." segment, or a forbidden character.`
)

var everyMethod = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}

var bypassAttempts = []struct {
	path string
	want string
}{
	{"/api/recipes/../admin/users", badSegmentPath},
	{"/api/recipes/..%2F..%2Fadmin/users", badSegmentPath},
	{"/api/recipes/%2e%2e/%2e%2e/admin", badSegmentPath},
	{"/api//admin/users", badSegmentPath},
	{"/api/recipes/", badSegmentPath},
	{"/API/admin", notAPIPath},
	{`/api/recipes/x\..\..\admin`, "The path is not a plain Mealie path: it contains a backslash."},
	{"/api/recipes/%252e%252e", "The path is not a plain Mealie path: it is encoded more than once."},
	{"/api/recipes/x%00", badSegmentPath},
	{"/api/recipes?x=1", "The path is not a plain Mealie path: it contains ? or #."},
	{"/api/recipes#x", "The path is not a plain Mealie path: it contains ? or #."},
	{"api/recipes", notAPIPath},
	{"/api/households/shopping/items/%2F..%2F..%2Fwebhooks", badSegmentPath},
	{"/api/users/self/../1234", badSegmentPath},
	{"/api/recipes/%zz", "The path is not a plain Mealie path: it is not valid percent-encoding."},
}

func ruleSamples() map[string]bool {
	samples := map[string]bool{}
	for _, r := range rules {
		path := strings.ReplaceAll(r.pattern, "{}", "abc")
		for _, method := range strings.Split(r.methods, ",") {
			samples[method+" "+path] = true
		}
	}
	return samples
}

func assertDenied(t *testing.T, method, path, want string) {
	t.Helper()
	got, err := Check(method, path)
	var denied *DeniedError
	if !errors.As(err, &denied) || got != "" {
		t.Errorf("Check(%q, %q) = %q, %v; want *DeniedError", method, path, got, err)
		return
	}
	if err.Error() != want {
		t.Errorf("Check(%q, %q) error = %q, want %q", method, path, err.Error(), want)
	}
}

func TestCheck_AllowsEveryRule(t *testing.T) {
	for sample := range ruleSamples() {
		method, path, _ := strings.Cut(sample, " ")
		got, err := Check(method, path)
		if err != nil || got != path {
			t.Errorf("Check(%q, %q) = %q, %v; want %q", method, path, got, err, path)
		}
	}
}

func TestCheck_UppercasesMethod(t *testing.T) {
	got, err := Check("get", "/api/recipes")
	if err != nil || got != "/api/recipes" {
		t.Errorf(`Check("get", "/api/recipes") = %q, %v; want "/api/recipes"`, got, err)
	}
}

func TestCheck_DeniesOtherMethods(t *testing.T) {
	tests := []struct {
		method, path, want string
	}{
		{"DELETE", "/api/recipes", "DELETE /api/recipes" + notAllowedSuffix},
		{"PATCH", "/api/foods/x", "PATCH /api/foods/x" + notAllowedSuffix},
		{"trace", "/api/recipes", "TRACE /api/recipes" + notAllowedSuffix},
		{"HEAD", "/api/users/self", "HEAD /api/users/self" + notAllowedSuffix},
	}
	for _, tt := range tests {
		assertDenied(t, tt.method, tt.path, tt.want)
	}
}

func TestCheck_DeniesOutOfScope(t *testing.T) {
	paths := []string{
		"/api/admin/users",
		"/api/admin/about",
		"/api/auth/token",
		"/api/auth/refresh",
		"/api/users/api-tokens",
		"/api/users/password",
		"/api/users/0b1c2d3e-4f50-6172-8394-a5b6c7d8e9f0",
		"/api/users/self/image",
		"/api/households/webhooks",
		"/api/households/events/notifications",
		"/api/households/invitations",
		"/api/households/preferences",
		"/api/households/permissions/member",
		"/api/households/mealplans/ical",
		"/api/households/recipe-actions",
		"/api/groups/ai-providers/settings",
		"/api/groups/seeders/foods",
		"/api/groups/migrations",
		"/api/groups/members",
		"/api/media/recipes/x/images/original.webp",
		"/api/recipes/exports",
		"/api/recipes/create/ai",
		"/api/recipes/create/image",
	}
	for _, path := range paths {
		for _, method := range everyMethod {
			assertDenied(t, method, path, method+" "+path+notAllowedSuffix)
		}
	}
}

func TestCheck_ReservedWordsNeverMatchWildcard(t *testing.T) {
	samples := ruleSamples()
	for _, r := range rules {
		if !strings.Contains(r.pattern, "{}") {
			continue
		}
		for _, word := range reservedWords {
			path := strings.ReplaceAll(strings.Replace(r.pattern, "{}", word, 1), "{}", "abc")
			for _, method := range strings.Split(r.methods, ",") {
				if samples[method+" "+path] {
					continue
				}
				assertDenied(t, method, path, method+" "+path+notAllowedSuffix)
			}
		}
	}
}

func TestCheck_ReservedWordsStillMatchLiteralRows(t *testing.T) {
	tests := []struct{ method, path string }{
		{"GET", "/api/recipes/suggestions"},
		{"GET", "/api/households/mealplans/rules"},
		{"PUT", "/api/foods/merge"},
		{"POST", "/api/recipes/create/url"},
	}
	for _, tt := range tests {
		got, err := Check(tt.method, tt.path)
		if err != nil || got != tt.path {
			t.Errorf("Check(%q, %q) = %q, %v; want %q", tt.method, tt.path, got, err, tt.path)
		}
	}
}

func TestCheck_BypassAttempts(t *testing.T) {
	for _, tt := range bypassAttempts {
		assertDenied(t, "GET", tt.path, tt.want)
	}
}

func TestCheck_CanonicalEscaping(t *testing.T) {
	for _, path := range []string{"/api/recipes/p%C3%A2te", "/api/recipes/pâte"} {
		got, err := Check("GET", path)
		if err != nil || got != "/api/recipes/p%C3%A2te" {
			t.Errorf(`Check("GET", %q) = %q, %v; want "/api/recipes/p%%C3%%A2te"`, path, got, err)
		}
	}
}

func literalRowAt(method string, length, index int, word string) bool {
	return slices.ContainsFunc(rules, func(r rule) bool {
		pattern := strings.Split(r.pattern, "/")
		return slices.Contains(strings.Split(r.methods, ","), method) && len(pattern) == length && pattern[index] == word
	})
}

var reachableAreas = []string{"users", "households", "groups", "recipes", "organizers", "shared", "comments", "parser", "foods", "units"}

func assertSafeCanonical(t *testing.T, method, canonical string) {
	t.Helper()
	if joined := (&url.URL{Scheme: "https", Host: "mealie.test"}).JoinPath(canonical).String(); joined != "https://mealie.test"+canonical {
		t.Fatalf("canonical %q becomes %q after JoinPath", canonical, joined)
	}
	decoded, err := url.PathUnescape(canonical)
	if err != nil || strings.Contains(decoded, "%") {
		t.Fatalf("canonical %q decodes to %q, %v", canonical, decoded, err)
	}
	segments := strings.Split(decoded, "/")
	if slices.Contains(segments[1:], "") || slices.Contains(segments, "..") {
		t.Fatalf("canonical %q has an empty or .. segment", canonical)
	}
	if !slices.Contains(reachableAreas, segments[2]) {
		t.Fatalf("canonical %q reaches area %q", canonical, segments[2])
	}
	if segments[2] == "users" && segments[3] != "self" {
		t.Fatalf("canonical %q reaches another user", canonical)
	}
	for i, segment := range segments {
		if slices.Contains(reservedWords, segment) && !literalRowAt(method, len(segments), i, segment) {
			t.Fatalf("canonical %q matches reserved word %q through a wildcard", canonical, segment)
		}
	}
}

func FuzzCheck(f *testing.F) {
	for _, tt := range bypassAttempts {
		f.Add("GET", tt.path)
	}
	f.Add("GET", "/api/recipes/abc")
	f.Fuzz(func(t *testing.T, method, path string) {
		canonical, err := Check(method, path)
		if err != nil {
			return
		}
		if again, err := Check(method, canonical); err != nil || again != canonical {
			t.Fatalf("Check(%q, %q) = %q, %v; want idempotent %q", method, canonical, again, err, canonical)
		}
		assertSafeCanonical(t, strings.ToUpper(method), canonical)
	})
}
