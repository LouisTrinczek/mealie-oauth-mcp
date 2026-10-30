package mealie

import (
	"net/url"
	"strings"
	"unicode"
)

const (
	firstPrintableRune = 0x20
	deleteRune         = 0x7f
	forbiddenRunes     = `/\%?#;`
)

type SegmentError struct {
	Value string
}

func (e *SegmentError) Error() string {
	return `"` + e.Value + `" is not a valid slug or id.`
}

func CheckSegment(s string) error {
	if strings.Trim(s, ".") == "" || strings.TrimFunc(s, unicode.IsSpace) == "" || strings.ContainsFunc(s, isForbiddenRune) {
		return &SegmentError{Value: s}
	}
	return nil
}

func isForbiddenRune(r rune) bool {
	return r < firstPrintableRune || r == deleteRune || strings.ContainsRune(forbiddenRunes, r)
}

func PathOf(prefix string, segments ...string) (string, error) {
	var path strings.Builder
	path.WriteString(prefix)
	for _, segment := range segments {
		if err := CheckSegment(segment); err != nil {
			return "", err
		}
		path.WriteString("/" + url.PathEscape(segment))
	}
	return path.String(), nil
}
