//go:build devenv

package integration

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	devPublicURL = "http://localhost:8080"
	readyTimeout = 90 * time.Second
)

func TestDevEnvironment(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	t.Cleanup(cancel)
	waitForOK(ctx, t, devPublicURL+"/readyz", readyTimeout)
	f := newFlow(ctx, t, leakWatch{}, devPublicURL, localhostTransport())
	steps := []struct {
		name string
		run  func(*testing.T)
	}{
		{"metadata_documents", f.fetchMetadata},
		{"register_client", f.register},
		{"authorize_through_mealie_shows_consent", f.authorizeToConsent},
		{"consent_allow_redirects_with_code", f.allowConsent},
		{"code_exchange_issues_tokens", f.exchangeCode},
		{"whoami_over_mcp", f.whoami},
	}
	for _, step := range steps {
		if !t.Run(step.name, step.run) {
			t.FailNow()
		}
	}
}

func localhostTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: requestTimeout}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, loopbackForLocalhost(addr))
	}
	return transport
}

func loopbackForLocalhost(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || !strings.HasSuffix(host, ".localhost") {
		return addr
	}
	return net.JoinHostPort("127.0.0.1", port)
}
