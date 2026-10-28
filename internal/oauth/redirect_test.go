package oauth

import (
	"net/url"
	"strings"
	"testing"
)

func TestMatchRedirectURI(t *testing.T) {
	tests := []struct {
		name       string
		registered []string
		requested  string
		want       bool
	}{
		{"exact match", []string{"https://app.example/cb"}, "https://app.example/cb", true},
		{"second registered matches", []string{"https://a.example/cb", "https://app.example/cb"}, "https://app.example/cb", true},
		{"private use scheme", []string{"com.example.app:/cb"}, "com.example.app:/cb", true},
		{"trailing slash differs", []string{"https://app.example/cb"}, "https://app.example/cb/", false},
		{"query order differs", []string{"https://app.example/cb?a=1&b=2"}, "https://app.example/cb?b=2&a=1", false},
		{"host case differs", []string{"https://app.example/cb"}, "https://APP.example/cb", false},
		{"https port differs", []string{"https://app.example/cb"}, "https://app.example:8443/cb", false},
		{"https loopback port differs", []string{"https://127.0.0.1/cb"}, "https://127.0.0.1:51234/cb", false},
		{"loopback port differs", []string{"http://127.0.0.1/cb"}, "http://127.0.0.1:51234/cb", true},
		{"loopback both ports differ", []string{"http://127.0.0.1:1000/cb"}, "http://127.0.0.1:2000/cb", true},
		{"ipv6 loopback port differs", []string{"http://[::1]/cb"}, "http://[::1]:51234/cb", true},
		{"localhost port differs", []string{"http://localhost/cb?x=1"}, "http://localhost:51234/cb?x=1", true},
		{"loopback path differs", []string{"http://127.0.0.1/cb"}, "http://127.0.0.1:51234/other", false},
		{"loopback query differs", []string{"http://127.0.0.1/cb?a=1"}, "http://127.0.0.1:51234/cb?a=2", false},
		{"loopback empty query differs", []string{"http://127.0.0.1/cb"}, "http://127.0.0.1:51234/cb?", false},
		{"localhost versus 127.0.0.1", []string{"http://localhost/cb"}, "http://127.0.0.1:51234/cb", false},
		{"loopback userinfo", []string{"http://127.0.0.1/cb"}, "http://user@127.0.0.1:51234/cb", false},
		{"non loopback http port differs", []string{"http://app.example/cb"}, "http://app.example:8080/cb", false},
		{"fragment", []string{"https://app.example/cb"}, "https://app.example/cb#frag", false},
		{"registered fragment exact", []string{"https://app.example/cb#frag"}, "https://app.example/cb#frag", false},
		{"empty fragment", []string{"https://app.example/cb#"}, "https://app.example/cb#", false},
		{"loopback fragment", []string{"http://127.0.0.1/cb"}, "http://127.0.0.1:51234/cb#x", false},
		{"unparsable", []string{"http://[::1/cb"}, "http://[::1/cb", false},
		{"nothing registered", nil, "https://app.example/cb", false},
		{"empty requested", []string{""}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchRedirectURI(tt.registered, tt.requested); got != tt.want {
				t.Errorf("MatchRedirectURI = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateRedirectURIForRegistration(t *testing.T) {
	tests := []struct {
		uri     string
		wantErr bool
	}{
		{"https://app.example/cb", false},
		{"https://app.example:8443/cb?x=1", false},
		{"http://127.0.0.1/cb", false},
		{"http://127.0.0.1:8080/cb", false},
		{"http://[::1]:8080/cb", false},
		{"http://localhost:3000/callback", false},
		{"com.example.app:/cb", false},
		{"com.example.app://cb", false},
		{"http://app.example/cb", true},
		{"http://10.0.0.1/cb", true},
		{"http://localhost.evil.example/cb", true},
		{"javascript:alert(1)", true},
		{"JAVASCRIPT:alert(1)", true},
		{"data:text/html,hi", true},
		{"file:///etc/passwd", true},
		{"https://app.example/cb#frag", true},
		{"https://app.example/cb#", true},
		{"com.example.app:/cb#x", true},
		{"myapp:/cb", true},
		{"/relative/cb", true},
		{"", true},
		{"https:///cb", true},
		{"https:opaque", true},
		{"https://[::1/cb", true},
	}
	for _, tt := range tests {
		t.Run(tt.uri, func(t *testing.T) {
			err := ValidateRedirectURIForRegistration(tt.uri)
			if (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func FuzzMatchRedirectURI(f *testing.F) {
	f.Add("https://app.example/cb", "https://app.example/cb")
	f.Add("http://127.0.0.1/cb", "http://127.0.0.1:51234/cb")
	f.Add("http://[::1]/cb", "http://[::1]:1/cb")
	f.Add("http://localhost/cb", "http://127.0.0.1:1/cb")
	f.Add("com.example.app:/cb", "com.example.app:/cb#x")
	f.Fuzz(func(t *testing.T, registered, requested string) {
		if !MatchRedirectURI([]string{registered}, requested) {
			return
		}
		got, err := url.Parse(requested)
		if err != nil {
			t.Fatalf("matched unparsable %q: %v", requested, err)
		}
		want, err := url.Parse(registered)
		if err != nil {
			t.Fatalf("matched unparsable registered %q: %v", registered, err)
		}
		if got.Scheme != want.Scheme || got.Hostname() != want.Hostname() {
			t.Fatalf("matched %q against %q with different scheme or host", requested, registered)
		}
		if strings.Contains(requested, "#") {
			t.Fatalf("matched %q with a fragment", requested)
		}
	})
}

func FuzzValidateRedirectURI(f *testing.F) {
	f.Add("https://app.example/cb")
	f.Add("http://127.0.0.1:8080/cb")
	f.Add("com.example.app:/cb")
	f.Add("javascript:alert(1)")
	f.Add("https://app.example/cb#x")
	f.Fuzz(func(t *testing.T, uri string) {
		if ValidateRedirectURIForRegistration(uri) == nil && strings.Contains(uri, "#") {
			t.Fatalf("accepted %q with a fragment", uri)
		}
	})
}
