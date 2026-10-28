package mealie

import (
	"fmt"
	"strconv"
	"strings"
)

const MinVersion = "v3.23.0"

func CompareVersions(a, b string) (int, error) {
	left, err := parseVersion(a)
	if err != nil {
		return 0, err
	}
	right, err := parseVersion(b)
	if err != nil {
		return 0, err
	}
	for i := range left {
		if left[i] != right[i] {
			if left[i] < right[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	return 0, nil
}

func parseVersion(v string) ([3]int, error) {
	var parts [3]int
	fields := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(fields) != len(parts) {
		return parts, fmt.Errorf("invalid version %q: want MAJOR.MINOR.PATCH", v)
	}
	for i, field := range fields {
		n, err := strconv.ParseUint(field, 10, 31)
		if err != nil {
			return parts, fmt.Errorf("invalid version %q: %w", v, err)
		}
		parts[i] = int(n)
	}
	return parts, nil
}
