package allowlist

import (
	"net/url"
	"slices"
	"strings"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
)

const (
	apiPrefix    = "/api/"
	wildcard     = "{}"
	allowedAreas = "mealie_api covers recipes, timeline, meal plans, organizers, shopping, foods, units, labels, comments, ratings and favorites, the ingredient parser, and your own user, household and group."
)

type DeniedError struct {
	Reason string
}

func (e *DeniedError) Error() string {
	return e.Reason
}

type rule struct {
	methods string
	pattern string
}

var knownMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}

var reservedWords = []string{
	"bulk-actions", "create", "create-bulk", "empty", "exports", "ical", "merge", "random",
	"rules", "shared", "slug", "suggestions", "test-scrape-url", "timeline", "today",
}

var rules = []rule{
	{"GET", "/api/users/self"},
	{"GET", "/api/users/self/ratings"},
	{"GET", "/api/users/self/ratings/{}"},
	{"GET", "/api/users/self/favorites"},
	{"GET,POST,PUT", "/api/households/cookbooks"},
	{"GET,PUT,DELETE", "/api/households/cookbooks/{}"},
	{"GET", "/api/households/self"},
	{"GET", "/api/households/self/recipes/{}"},
	{"GET,POST", "/api/households/shopping/lists"},
	{"GET,PUT,DELETE", "/api/households/shopping/lists/{}"},
	{"PUT", "/api/households/shopping/lists/{}/label-settings"},
	{"POST", "/api/households/shopping/lists/{}/recipe"},
	{"POST", "/api/households/shopping/lists/{}/recipe/{}"},
	{"POST", "/api/households/shopping/lists/{}/recipe/{}/delete"},
	{"GET,POST,PUT,DELETE", "/api/households/shopping/items"},
	{"POST", "/api/households/shopping/items/create-bulk"},
	{"GET,PUT,DELETE", "/api/households/shopping/items/{}"},
	{"GET,POST", "/api/households/mealplans"},
	{"GET", "/api/households/mealplans/today"},
	{"POST", "/api/households/mealplans/random"},
	{"GET,PUT,DELETE", "/api/households/mealplans/{}"},
	{"GET,POST", "/api/households/mealplans/rules"},
	{"GET,PUT,DELETE", "/api/households/mealplans/rules/{}"},
	{"GET", "/api/groups/self"},
	{"GET", "/api/groups/households"},
	{"GET", "/api/groups/households/{}"},
	{"GET,POST", "/api/groups/labels"},
	{"GET", "/api/groups/labels/empty"},
	{"POST", "/api/groups/labels/merge"},
	{"GET,PUT,DELETE", "/api/groups/labels/{}"},
	{"GET,POST,PUT,PATCH", "/api/recipes"},
	{"GET", "/api/recipes/suggestions"},
	{"POST", "/api/recipes/test-scrape-url"},
	{"POST", "/api/recipes/create/url"},
	{"POST", "/api/recipes/create/url/bulk"},
	{"POST", "/api/recipes/create/html-or-json"},
	{"POST", "/api/recipes/bulk-actions/tag"},
	{"POST", "/api/recipes/bulk-actions/settings"},
	{"POST", "/api/recipes/bulk-actions/categorize"},
	{"POST", "/api/recipes/bulk-actions/delete"},
	{"GET,POST", "/api/recipes/timeline/events"},
	{"GET,PUT,DELETE", "/api/recipes/timeline/events/{}"},
	{"GET,PUT,PATCH,DELETE", "/api/recipes/{}"},
	{"POST", "/api/recipes/{}/duplicate"},
	{"PATCH", "/api/recipes/{}/last-made"},
	{"POST", "/api/recipes/{}/image"},
	{"POST", "/api/recipes/{}/assets/url"},
	{"GET", "/api/recipes/{}/comments"},
	{"GET,POST", "/api/organizers/categories"},
	{"GET,POST", "/api/organizers/tags"},
	{"GET,POST", "/api/organizers/tools"},
	{"GET", "/api/organizers/categories/empty"},
	{"GET", "/api/organizers/tags/empty"},
	{"GET", "/api/organizers/tools/empty"},
	{"POST", "/api/organizers/categories/merge"},
	{"POST", "/api/organizers/tags/merge"},
	{"POST", "/api/organizers/tools/merge"},
	{"GET,PUT,DELETE", "/api/organizers/categories/{}"},
	{"GET,PUT,DELETE", "/api/organizers/tags/{}"},
	{"GET,PUT,DELETE", "/api/organizers/tools/{}"},
	{"GET", "/api/organizers/categories/slug/{}"},
	{"GET", "/api/organizers/tags/slug/{}"},
	{"GET", "/api/organizers/tools/slug/{}"},
	{"GET,POST", "/api/shared/recipes"},
	{"GET,DELETE", "/api/shared/recipes/{}"},
	{"GET,POST", "/api/comments"},
	{"GET,PUT,DELETE", "/api/comments/{}"},
	{"POST", "/api/parser/ingredient"},
	{"POST", "/api/parser/ingredients"},
	{"GET,POST", "/api/foods"},
	{"GET", "/api/foods/empty"},
	{"PUT", "/api/foods/merge"},
	{"GET,PUT,DELETE", "/api/foods/{}"},
	{"GET,POST", "/api/units"},
	{"PUT", "/api/units/merge"},
	{"GET,PUT,DELETE", "/api/units/{}"},
}

func Check(method, rawPath string) (string, error) {
	method = strings.ToUpper(method)
	segments, err := plainSegments(rawPath)
	if err != nil {
		return "", err
	}
	canonical := canonicalPath(segments)
	if !slices.Contains(knownMethods, method) || !slices.ContainsFunc(rules, func(r rule) bool { return r.allows(method, segments) }) {
		return "", &DeniedError{Reason: method + " " + canonical + " is not allowed. " + allowedAreas}
	}
	return canonical, nil
}

func plainSegments(rawPath string) ([]string, error) {
	if !strings.HasPrefix(rawPath, apiPrefix) {
		return nil, &DeniedError{Reason: "mealie_api only reaches /api/ paths."}
	}
	if strings.ContainsAny(rawPath, "?#") {
		return nil, notPlain("it contains ? or #")
	}
	if strings.Contains(rawPath, `\`) {
		return nil, notPlain("it contains a backslash")
	}
	decoded, err := url.PathUnescape(rawPath)
	if err != nil {
		return nil, notPlain("it is not valid percent-encoding")
	}
	if strings.Contains(decoded, "%") {
		return nil, notPlain("it is encoded more than once")
	}
	segments := strings.Split(strings.TrimPrefix(decoded, "/"), "/")
	for _, segment := range segments {
		if mealie.CheckSegment(segment) != nil {
			return nil, notPlain(`it has an empty, "." or ".." segment, or a forbidden character`)
		}
	}
	return segments, nil
}

func notPlain(reason string) *DeniedError {
	return &DeniedError{Reason: "The path is not a plain Mealie path: " + reason + "."}
}

func canonicalPath(segments []string) string {
	escaped := make([]string, len(segments))
	for i, segment := range segments {
		escaped[i] = url.PathEscape(segment)
	}
	return "/" + strings.Join(escaped, "/")
}

func (r rule) allows(method string, segments []string) bool {
	pattern := strings.Split(strings.TrimPrefix(r.pattern, "/"), "/")
	return slices.Contains(strings.Split(r.methods, ","), method) && slices.EqualFunc(pattern, segments, matchesSegment)
}

func matchesSegment(pattern, segment string) bool {
	if pattern == wildcard {
		return !slices.Contains(reservedWords, segment)
	}
	return pattern == segment
}
