package oauth

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type stubResolver map[string][]netip.Addr

func (s stubResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	addrs, ok := s[host]
	if !ok {
		return nil, fmt.Errorf("no such host %q", host)
	}
	return addrs, nil
}

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_800_000_000, 0)}
}

func testDocument(clientID string) map[string]any {
	return map[string]any{
		"client_id":     clientID,
		"client_name":   "Test Client",
		"redirect_uris": []string{"http://127.0.0.1/callback"},
	}
}

func encodeDocument(t *testing.T, doc map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal document: %v", err)
	}
	return raw
}

func requestClientID(r *http.Request) string {
	return "https://" + r.Host + r.URL.Path
}

func serveDocument(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(testDocument(requestClientID(r)))
}

func newTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func newTestFetcher(srv *httptest.Server, clock *fakeClock, opts CIMDOptions) *CIMDFetcher {
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	opts.AllowPrivate = true
	opts.RootCAs = pool
	return newCIMDFetcher(opts, clock.Now)
}

func TestCIMD_Fetch(t *testing.T) {
	var accept atomic.Value
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		accept.Store(r.Header.Get("Accept"))
		serveDocument(w, r)
	}))
	f := newTestFetcher(srv, newFakeClock(), CIMDOptions{})
	clientID := srv.URL + "/client.json"

	got, err := f.Fetch(context.Background(), clientID)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.ClientID != clientID || got.ClientName != "Test Client" {
		t.Errorf("metadata = %+v", got)
	}
	if len(got.RedirectURIs) != 1 || got.RedirectURIs[0] != "http://127.0.0.1/callback" {
		t.Errorf("redirect uris = %v", got.RedirectURIs)
	}
	if accept.Load() != "application/json" {
		t.Errorf("Accept = %v, want application/json", accept.Load())
	}
}

func loopbackRedirectURIs(n int) []string {
	uris := make([]string, n)
	for i := range uris {
		uris[i] = fmt.Sprintf("http://127.0.0.1/cb%d", i)
	}
	return uris
}

func TestCIMD_RejectsTooManyRedirectURIs(t *testing.T) {
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		doc := testDocument(requestClientID(r))
		doc["redirect_uris"] = loopbackRedirectURIs(maxRedirectURIs + 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	}))
	f := newTestFetcher(srv, newFakeClock(), CIMDOptions{})

	if _, err := f.Fetch(context.Background(), srv.URL+"/client.json"); err == nil {
		t.Fatal("Fetch accepted a document with eleven redirect_uris")
	}
}

func TestCIMD_RejectsPrivateIPs(t *testing.T) {
	var hits atomic.Int32
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		serveDocument(w, r)
	}))
	port := strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)
	resolver := stubResolver{
		"internal.example": {netip.MustParseAddr("10.0.0.1")},
		"rebind.example":   {netip.MustParseAddr("127.0.0.1")},
		"mapped.example":   {netip.MustParseAddr("::ffff:127.0.0.1")},
		"mixed.example":    {netip.MustParseAddr("100.64.0.1"), netip.MustParseAddr("127.0.0.1")},
	}
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	f := newCIMDFetcher(CIMDOptions{Resolver: resolver, RootCAs: pool}, newFakeClock().Now)

	tests := []struct {
		name     string
		clientID string
	}{
		{"ipv4 loopback literal", "https://127.0.0.1:" + port + "/client.json"},
		{"hostname resolving to private", "https://internal.example/client.json"},
		{"ipv6 loopback literal", "https://[::1]:" + port + "/client.json"},
		{"metadata service", "https://169.254.169.254/latest/meta-data"},
		{"hostname resolving to loopback", "https://rebind.example:" + port + "/client.json"},
		{"ipv4 mapped loopback literal", "https://[::ffff:127.0.0.1]:" + port + "/client.json"},
		{"hostname resolving to ipv4 mapped loopback", "https://mapped.example:" + port + "/client.json"},
		{"every resolved address blocked", "https://mixed.example:" + port + "/client.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.Fetch(context.Background(), tt.clientID)
			if !errors.Is(err, errBlockedAddress) {
				t.Fatalf("err = %v, want blocked address", err)
			}
		})
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("server hits = %d, want 0", got)
	}
}

func closedPort(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)
	_ = listener.Close()
	return port
}

func TestCIMD_DialErrorsHideResolvedAddresses(t *testing.T) {
	resolver := stubResolver{
		"internal.example": {netip.MustParseAddr("10.1.2.3")},
		"closed.example":   {netip.MustParseAddr("127.0.0.1")},
		"empty.example":    {},
	}
	tests := []struct {
		name         string
		allowPrivate bool
		clientID     string
		want         string
		hidden       string
	}{
		{"blocked", false, "https://internal.example/client.json", "not publicly routable", "10.1.2.3"},
		{"refused", true, "https://closed.example:" + closedPort(t) + "/client.json", "could not connect", "127.0.0.1"},
		{"unresolvable", false, "https://unknown.example/client.json", "could not resolve", "no such host"},
		{"no addresses", false, "https://empty.example/client.json", "could not resolve", "<nil>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newCIMDFetcher(CIMDOptions{Resolver: resolver, AllowPrivate: tt.allowPrivate}, newFakeClock().Now)
			_, err := f.Fetch(context.Background(), tt.clientID)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
			if strings.Contains(err.Error(), tt.hidden) {
				t.Errorf("error %q reveals %q", err, tt.hidden)
			}
		})
	}
}

func TestCIMD_GuardRefusesBlockedRanges(t *testing.T) {
	tests := []struct {
		addr    string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"127.255.255.254", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"192.168.1.1", true},
		{"100.64.0.1", true},
		{"100.127.255.255", true},
		{"169.254.169.254", true},
		{"224.0.0.1", true},
		{"239.255.255.250", true},
		{"0.0.0.0", true},
		{"0.1.2.3", true},
		{"::", true},
		{"::1", true},
		{"fe80::1", true},
		{"fe80::1%eth0", true},
		{"fc00::1", true},
		{"fd12:3456::1", true},
		{"ff02::1", true},
		{"ff05::2", true},
		{"::ffff:127.0.0.1", true},
		{"::ffff:10.0.0.1", true},
		{"::ffff:169.254.169.254", true},
		{"::7f00:1", true},
		{"64:ff9b::a00:1", true},
		{"64:ff9b::7f00:1", true},
		{"64:ff9b:1::1", true},
		{"2002:7f00:1::1", true},
		{"2001:0:4136:e378::1", true},
		{"fec0::1", true},
		{"93.184.216.34", false},
		{"100.63.255.255", false},
		{"100.128.0.0", false},
		{"2606:4700::1111", false},
		{"64:ff9b::5db8:d822", false},
		{"::ffff:93.184.216.34", false},
	}
	guard := addressGuard{}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			address := netip.AddrPortFrom(netip.MustParseAddr(tt.addr), 443).String()
			err := guard.control("tcp", address, nil)
			if got := errors.Is(err, errBlockedAddress); got != tt.blocked {
				t.Errorf("blocked = %v, want %v (err %v)", got, tt.blocked, err)
			}
		})
	}
	if err := guard.control("tcp", "not-an-address", nil); !errors.Is(err, errBlockedAddress) {
		t.Errorf("unparsable address err = %v, want blocked", err)
	}
	if err := (addressGuard{allowPrivate: true}).control("tcp", "127.0.0.1:443", nil); err != nil {
		t.Errorf("allowPrivate err = %v, want nil", err)
	}
}

func TestCIMD_RejectsInvalidClientID(t *testing.T) {
	var hits atomic.Int32
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		serveDocument(w, r)
	}))
	f := newTestFetcher(srv, newFakeClock(), CIMDOptions{})
	tests := []string{
		"",
		"client",
		"/client.json",
		strings.Replace(srv.URL, "https", "http", 1) + "/client.json",
		srv.URL,
		srv.URL + "/",
		srv.URL + "/client.json#frag",
		srv.URL + "/client.json#",
		strings.Replace(srv.URL, "https://", "https://user:pass@", 1) + "/client.json",
		"https:opaque",
		"https:///client.json",
		"https://[::1/client.json",
	}
	for _, clientID := range tests {
		t.Run(clientID, func(t *testing.T) {
			if _, err := f.Fetch(context.Background(), clientID); err == nil {
				t.Fatal("Fetch returned no error")
			}
		})
	}
	if got := hits.Load(); got != 0 {
		t.Errorf("server hits = %d, want 0", got)
	}
}

func TestCIMD_MismatchedClientID(t *testing.T) {
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(testDocument(requestClientID(r) + "/other"))
	}))
	f := newTestFetcher(srv, newFakeClock(), CIMDOptions{})
	if _, err := f.Fetch(context.Background(), srv.URL+"/client.json"); err == nil {
		t.Fatal("Fetch accepted a document with a different client_id")
	}
}

func paddedDocument(t *testing.T, clientID string, size int) []byte {
	t.Helper()
	doc := testDocument(clientID)
	doc["client_name"] = ""
	pad := size - len(encodeDocument(t, doc))
	doc["client_name"] = strings.Repeat("a", pad)
	return encodeDocument(t, doc)
}

func TestCIMD_TooLarge(t *testing.T) {
	const maxBytes = 1024
	tests := []struct {
		name          string
		size          int
		contentLength bool
		wantErr       bool
	}{
		{"at limit", maxBytes, true, false},
		{"over limit with content length", maxBytes + 1, true, true},
		{"over limit without content length", maxBytes + 1, false, true},
		{"far over limit without content length", 64 * maxBytes, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body []byte
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if tt.contentLength {
					w.Header().Set("Content-Length", strconv.Itoa(len(body)))
				}
				_, _ = w.Write(body[:10])
				w.(http.Flusher).Flush()
				_, _ = w.Write(body[10:])
			}))
			clientID := "https://" + srv.Listener.Addr().String() + "/client.json"
			body = paddedDocument(t, clientID, tt.size)
			srv.StartTLS()
			t.Cleanup(srv.Close)
			f := newTestFetcher(srv, newFakeClock(), CIMDOptions{MaxBytes: maxBytes})
			_, err := f.Fetch(context.Background(), clientID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCIMD_NoRedirectFollow(t *testing.T) {
	var followed atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/client.json", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/real.json", http.StatusFound)
	})
	mux.HandleFunc("/real.json", func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(testDocument("https://" + r.Host + "/client.json"))
	})
	srv := newTLSServer(t, mux)
	f := newTestFetcher(srv, newFakeClock(), CIMDOptions{})

	if _, err := f.Fetch(context.Background(), srv.URL+"/client.json"); err == nil {
		t.Fatal("Fetch followed a redirect")
	}
	if got := followed.Load(); got != 0 {
		t.Errorf("redirect target hits = %d, want 0", got)
	}
}

func TestCIMD_RejectsBadResponses(t *testing.T) {
	tests := []struct {
		name    string
		respond func(w http.ResponseWriter, r *http.Request)
		wantErr bool
	}{
		{"json with charset", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_ = json.NewEncoder(w).Encode(testDocument(requestClientID(r)))
		}, false},
		{"not found", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
		}, true},
		{"created instead of ok", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(testDocument(requestClientID(r)))
		}, true},
		{"html content type", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_ = json.NewEncoder(w).Encode(testDocument(requestClientID(r)))
		}, true},
		{"missing content type", func(w http.ResponseWriter, r *http.Request) {
			w.Header()["Content-Type"] = nil
			_ = json.NewEncoder(w).Encode(testDocument(requestClientID(r)))
		}, true},
		{"malformed content type", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json; =")
			_ = json.NewEncoder(w).Encode(testDocument(requestClientID(r)))
		}, true},
		{"invalid json", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "{")
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTLSServer(t, http.HandlerFunc(tt.respond))
			f := newTestFetcher(srv, newFakeClock(), CIMDOptions{})
			_, err := f.Fetch(context.Background(), srv.URL+"/client.json")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestParseCIMD(t *testing.T) {
	const clientID = "https://client.example/cimd.json"
	tests := []struct {
		name    string
		mutate  func(doc map[string]any)
		wantErr bool
	}{
		{"valid", func(map[string]any) {}, false},
		{"auth method none", func(doc map[string]any) { doc["token_endpoint_auth_method"] = "none" }, false},
		{"unknown fields", func(doc map[string]any) { doc["logo_uri"] = "https://client.example/logo.png" }, false},
		{"auth method client secret", func(doc map[string]any) { doc["token_endpoint_auth_method"] = "client_secret_basic" }, true},
		{"missing client id", func(doc map[string]any) { delete(doc, "client_id") }, true},
		{"client id with trailing slash", func(doc map[string]any) { doc["client_id"] = clientID + "/" }, true},
		{"missing redirect uris", func(doc map[string]any) { delete(doc, "redirect_uris") }, true},
		{"empty redirect uris", func(doc map[string]any) { doc["redirect_uris"] = []string{} }, true},
		{"invalid redirect uri", func(doc map[string]any) {
			doc["redirect_uris"] = []string{"https://client.example/cb", "javascript:alert(1)"}
		}, true},
		{"redirect uris wrong type", func(doc map[string]any) { doc["redirect_uris"] = "https://client.example/cb" }, true},
		{"ten redirect uris", func(doc map[string]any) { doc["redirect_uris"] = loopbackRedirectURIs(maxRedirectURIs) }, false},
		{"eleven redirect uris", func(doc map[string]any) { doc["redirect_uris"] = loopbackRedirectURIs(maxRedirectURIs + 1) }, true},
		{"over-long redirect uri", func(doc map[string]any) {
			doc["redirect_uris"] = []string{uriOfLength("https://client.example/", maxRedirectURIBytes+1)}
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := testDocument(clientID)
			tt.mutate(doc)
			got, err := parseCIMD(encodeDocument(t, doc), clientID)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && got.ClientID != clientID {
				t.Errorf("client id = %q, want %q", got.ClientID, clientID)
			}
		})
	}
}

func TestCIMD_Cache(t *testing.T) {
	var good, bad atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/good.json", func(w http.ResponseWriter, r *http.Request) {
		good.Add(1)
		serveDocument(w, r)
	})
	mux.HandleFunc("/bad.json", func(w http.ResponseWriter, _ *http.Request) {
		bad.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	srv := newTLSServer(t, mux)
	clock := newFakeClock()
	f := newTestFetcher(srv, clock, CIMDOptions{})
	ctx := context.Background()

	fetch := func(path string, wantErr bool) {
		t.Helper()
		_, err := f.Fetch(ctx, srv.URL+path)
		if (err != nil) != wantErr {
			t.Fatalf("Fetch %s: err = %v, wantErr %v", path, err, wantErr)
		}
	}
	expectHits := func(counter *atomic.Int32, want int32) {
		t.Helper()
		if got := counter.Load(); got != want {
			t.Fatalf("hits = %d, want %d", got, want)
		}
	}

	fetch("/good.json", false)
	fetch("/bad.json", true)
	fetch("/good.json", false)
	fetch("/bad.json", true)
	expectHits(&good, 1)
	expectHits(&bad, 1)

	clock.advance(59 * time.Second)
	fetch("/bad.json", true)
	expectHits(&bad, 1)

	clock.advance(2 * time.Second)
	fetch("/bad.json", true)
	expectHits(&bad, 2)

	clock.advance(58 * time.Minute)
	fetch("/good.json", false)
	expectHits(&good, 1)

	clock.advance(2 * time.Minute)
	fetch("/good.json", false)
	expectHits(&good, 2)
}

func TestCIMD_CacheTTLOption(t *testing.T) {
	var hits atomic.Int32
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		serveDocument(w, r)
	}))
	clock := newFakeClock()
	f := newTestFetcher(srv, clock, CIMDOptions{CacheTTL: 10 * time.Minute})
	for range 2 {
		if _, err := f.Fetch(context.Background(), srv.URL+"/client.json"); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		clock.advance(11 * time.Minute)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("hits = %d, want 2", got)
	}
}

func TestCIMD_CacheIsBounded(t *testing.T) {
	var hits atomic.Int32
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		serveDocument(w, r)
	}))
	clock := newFakeClock()
	f := newTestFetcher(srv, clock, CIMDOptions{})
	f.maxEntries = 2
	fetch := func(path string) {
		t.Helper()
		if _, err := f.Fetch(context.Background(), srv.URL+path); err != nil {
			t.Fatalf("Fetch %s: %v", path, err)
		}
	}

	fetch("/a.json")
	fetch("/b.json")
	fetch("/c.json")
	fetch("/c.json")
	fetch("/a.json")
	if got := hits.Load(); got != 4 {
		t.Fatalf("hits = %d, want 4", got)
	}

	clock.advance(2 * time.Hour)
	fetch("/c.json")
	fetch("/c.json")
	if got := hits.Load(); got != 5 {
		t.Errorf("hits after expiry = %d, want 5", got)
	}
}

func TestCIMD_CanceledFetchIsNotCached(t *testing.T) {
	var hits atomic.Int32
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		serveDocument(w, r)
	}))
	f := newTestFetcher(srv, newFakeClock(), CIMDOptions{})
	clientID := srv.URL + "/client.json"

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.Fetch(canceled, clientID); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context canceled", err)
	}
	if _, err := f.Fetch(context.Background(), clientID); err != nil {
		t.Fatalf("Fetch after cancel: %v", err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("hits = %d, want 1", got)
	}
}

func TestCIMD_Timeout(t *testing.T) {
	release := make(chan struct{})
	srv := newTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release) })
	f := newTestFetcher(srv, newFakeClock(), CIMDOptions{Timeout: 50 * time.Millisecond})
	start := time.Now()
	if _, err := f.Fetch(context.Background(), srv.URL+"/client.json"); err == nil {
		t.Fatal("Fetch returned no error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Fetch took %v, want it bounded by the timeout", elapsed)
	}
}

func TestCIMD_TransportIgnoresEnvironmentProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://proxy.invalid:3128")
	f := NewCIMDFetcher(CIMDOptions{})
	transport, ok := f.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", f.client.Transport)
	}
	if transport.Proxy != nil {
		t.Error("transport uses a proxy")
	}
	if f.client.CheckRedirect == nil {
		t.Error("client follows redirects")
	}
}

func FuzzCIMDParse(f *testing.F) {
	const clientID = "https://client.example/cimd.json"
	f.Add([]byte(`{"client_id":"https://client.example/cimd.json","redirect_uris":["https://client.example/cb"]}`))
	f.Add([]byte(`{"client_id":"https://client.example/cimd.json","redirect_uris":["javascript:x"]}`))
	f.Add([]byte(`{"redirect_uris":null,"token_endpoint_auth_method":7}`))
	f.Add([]byte(`[]`))
	f.Add([]byte(``))
	f.Fuzz(func(t *testing.T, body []byte) {
		got, err := parseCIMD(body, clientID)
		if err != nil {
			return
		}
		if got.ClientID != clientID || len(got.RedirectURIs) == 0 {
			t.Fatalf("accepted invalid metadata %+v", got)
		}
	})
}
