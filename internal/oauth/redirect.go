package oauth

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const maxRedirectURIBytes = 2048

func MatchRedirectURI(registered []string, requested string) bool {
	req, ok := parseRedirectURI(requested)
	if !ok {
		return false
	}
	for _, candidate := range registered {
		if candidate == requested {
			return true
		}
		reg, ok := parseRedirectURI(candidate)
		if ok && loopbackMatch(reg, req) {
			return true
		}
	}
	return false
}

func parseRedirectURI(raw string) (*url.URL, bool) {
	if strings.Contains(raw, "#") {
		return nil, false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return nil, false
	}
	return u, true
}

func loopbackMatch(registered, requested *url.URL) bool {
	return isLoopbackRedirect(registered) && isLoopbackRedirect(requested) &&
		registered.Hostname() == requested.Hostname() &&
		registered.EscapedPath() == requested.EscapedPath() &&
		registered.RawQuery == requested.RawQuery &&
		registered.ForceQuery == requested.ForceQuery
}

func isLoopbackRedirect(u *url.URL) bool {
	return u.Scheme == "http" && u.User == nil && u.Opaque == "" && isLoopbackHost(u.Hostname())
}

func isLoopbackHost(host string) bool {
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	default:
		return false
	}
}

func ValidateRedirectURIForRegistration(raw string) error {
	if len(raw) > maxRedirectURIBytes {
		return fmt.Errorf("redirect URI must be at most %d bytes", maxRedirectURIBytes)
	}
	if strings.Contains(raw, "#") {
		return errors.New("redirect URI must not contain a fragment")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parsing redirect URI: %w", err)
	}
	switch {
	case u.Scheme == "https":
		return requireHost(u)
	case u.Scheme == "http":
		return requireLoopback(u)
	case strings.Contains(u.Scheme, "."):
		return nil
	default:
		return fmt.Errorf("redirect URI scheme %q is not allowed; use https, loopback http, or a private-use scheme like com.example.app", u.Scheme)
	}
}

func requireHost(u *url.URL) error {
	if u.Host == "" {
		return errors.New("https redirect URI must have a host")
	}
	return nil
}

func requireLoopback(u *url.URL) error {
	if !isLoopbackHost(u.Hostname()) {
		return errors.New("http redirect URI must use host 127.0.0.1, [::1] or localhost")
	}
	return nil
}
