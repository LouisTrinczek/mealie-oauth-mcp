package mealie

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestPathOf_RejectsUnsafeSegments(t *testing.T) {
	for _, segment := range []string{"", ".", "..", "...", "....", " ", " \t", "../admin", "a/b", "a\\b", "%2e%2e", "a?b", "a#b", "a;b", "a\x00b", "a\x7f"} {
		t.Run(fmt.Sprintf("%q", segment), func(t *testing.T) {
			_, err := PathOf("/api/recipes", segment)
			var segErr *SegmentError
			if !errors.As(err, &segErr) {
				t.Fatalf("PathOf(%q) error = %v, want *SegmentError", segment, err)
			}
			if want := `"` + segment + `" is not a valid slug or id.`; err.Error() != want {
				t.Errorf("PathOf(%q) message = %q, want %q", segment, err.Error(), want)
			}
		})
	}
}

func TestPathOf_EscapesSegments(t *testing.T) {
	tests := []struct {
		name     string
		segments []string
		want     string
	}{
		{"accents and space", []string{"pâte brisée"}, "/api/recipes/p%C3%A2te%20bris%C3%A9e"},
		{"two segments", []string{"abc", "duplicate"}, "/api/recipes/abc/duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PathOf("/api/recipes", tt.segments...)
			if err != nil || got != tt.want {
				t.Errorf("PathOf(%q) = %q, %v; want %q", tt.segments, got, err, tt.want)
			}
		})
	}
}

func FuzzPathOf(f *testing.F) {
	for _, seed := range []string{"abc", "..", "a/b", "%2e", "pâte", "tomato;soup", "   "} {
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
