package oauth

import (
	"errors"
	"strings"
)

var errInvalidTarget = errors.New("resource does not identify this server")

func ResolveResource(requested, ours string) (string, error) {
	if requested == "" {
		return ours, nil
	}
	if strings.TrimSuffix(requested, "/") != strings.TrimSuffix(ours, "/") {
		return "", errInvalidTarget
	}
	return ours, nil
}
