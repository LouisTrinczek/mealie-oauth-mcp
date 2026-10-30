package mealie

import (
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestPathOf_RejectsUnsafeSegments(t *testing.T) {
	for _, segment := range []string{"", ".", "..", "../admin", "a/b", "a\\b", "%2e%2e", "a?b", "a#b", "a\x00b", "a\x7f"} {
		_, err := PathOf("/api/recipes", segment)
		var segErr *SegmentError
		if !errors.As(err, &segErr) {
			t.Errorf("PathOf(%q) error = %v, want *SegmentError", segment, err)
			continue
		}
		if want := `"` + segment + `" is not a valid slug or id.`; err.Error() != want {
			t.Errorf("PathOf(%q) message = %q, want %q", segment, err.Error(), want)
		}
	}
}

func TestPathOf_EscapesSegments(t *testing.T) {
	tests := []struct {
		segments []string
		want     string
	}{
		{[]string{"pâte brisée"}, "/api/recipes/p%C3%A2te%20bris%C3%A9e"},
		{[]string{"abc", "duplicate"}, "/api/recipes/abc/duplicate"},
	}
	for _, tt := range tests {
		got, err := PathOf("/api/recipes", tt.segments...)
		if err != nil || got != tt.want {
			t.Errorf("PathOf(%q) = %q, %v; want %q", tt.segments, got, err, tt.want)
		}
	}
}

func FuzzPathOf(f *testing.F) {
	for _, seed := range []string{"abc", "..", "a/b", "%2e", "pâte"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, segment string) {
		got, err := PathOf("/api/x", segment)
		if err != nil {
			return
		}
		unescaped, err := url.PathUnescape(got)
		if err != nil || unescaped != "/api/x/"+segment {
			t.Errorf("PathOf(%q) = %q, unescapes to %q, %v", segment, got, unescaped, err)
		}
		if strings.Count(got, "/") != 3 {
			t.Errorf("PathOf(%q) = %q, want exactly three slashes", segment, got)
		}
	})
}
