package healthcheck

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
)

func Check(ctx context.Context, client *http.Client, listenAddr string) error {
	target, err := healthzURL(listenAddr)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return fmt.Errorf("building health request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("requesting health endpoint: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered status %d", target, resp.StatusCode)
	}
	return nil
}

func healthzURL(listenAddr string) (string, error) {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "", fmt.Errorf("parsing listen address %q: %w", listenAddr, err)
	}
	if isUnspecified(host) {
		host = "127.0.0.1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/healthz"}).String(), nil
}

func isUnspecified(host string) bool {
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}
