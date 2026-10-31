//go:build integration || devenv

package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	clientRedirectURI   = "http://127.0.0.1:39999/cb"
	clientHost          = "127.0.0.1:39999"
	userEmail           = "it-user@example.com"
	bindingCookie       = "mmcp_auth"
	requestTimeout      = 30 * time.Second
	testTimeout         = 5 * time.Minute
	maxRedirects        = 10
	maxRateLimitRetries = 3
	startupPoll         = 50 * time.Millisecond
)

type leakWatch map[string][]string

func (w leakWatch) remember(kind, value string) {
	w[kind] = append(w[kind], value)
}

type reply struct {
	status int
	header http.Header
	body   []byte
	url    *url.URL
}

type serverMetadata struct {
	Issuer                        string   `json:"issuer"`
	AuthorizationEndpoint         string   `json:"authorization_endpoint"`
	TokenEndpoint                 string   `json:"token_endpoint"`
	RegistrationEndpoint          string   `json:"registration_endpoint"`
	RevocationEndpoint            string   `json:"revocation_endpoint"`
	CodeChallengeMethodsSupported []string `json:"code_challenge_methods_supported"`
}

type resourceDocument struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
}

type tokens struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
}

type flow struct {
	ctx          context.Context
	leaks        leakWatch
	publicURL    string
	api          *http.Client
	browser      *http.Client
	metadata     serverMetadata
	clientID     string
	verifier     string
	state        string
	upstreamCode string
	consentForm  url.Values
	consentURL   string
	code         string
	tokens       tokens
	metricsURL   string
}

func waitForOK(ctx context.Context, t *testing.T, endpoint string, timeout time.Duration) {
	t.Helper()
	client := &http.Client{Timeout: requestTimeout}
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(startupPoll) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			t.Fatalf("building request for %s: %v", endpoint, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return
		}
	}
	t.Fatalf("%s did not answer 200 within %v", endpoint, timeout)
}

func newFlow(ctx context.Context, t *testing.T, leaks leakWatch, publicURL string, transport http.RoundTripper) *flow {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("creating cookie jar: %v", err)
	}
	f := &flow{
		ctx:       ctx,
		leaks:     leaks,
		publicURL: publicURL,
		api: &http.Client{
			Timeout:       requestTimeout,
			Transport:     transport,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	f.browser = &http.Client{Timeout: requestTimeout, Transport: transport, Jar: jar, CheckRedirect: f.followBrowserRedirect}
	return f
}

func (f *flow) resourceURL() string {
	return f.publicURL + "/mcp"
}

func (f *flow) resourceMetadataURL() string {
	return f.publicURL + "/.well-known/oauth-protected-resource/mcp"
}

func (f *flow) followBrowserRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Host == clientHost {
		return http.ErrUseLastResponse
	}
	if req.URL.Scheme+"://"+req.URL.Host+req.URL.Path == f.publicURL+"/callback" {
		f.upstreamCode = req.URL.Query().Get("code")
		f.leaks.remember("upstream authorization code", f.upstreamCode)
	}
	if len(via) >= maxRedirects {
		return errors.New("too many redirects")
	}
	return nil
}

func (f *flow) fetchMetadata(t *testing.T) {
	var resource resourceDocument
	f.getJSON(t, f.resourceMetadataURL(), &resource)
	if resource.Resource != f.resourceURL() {
		t.Fatalf("protected resource metadata resource = %q, want %q", resource.Resource, f.resourceURL())
	}
	if !slices.Contains(resource.AuthorizationServers, f.publicURL) {
		t.Fatalf("authorization_servers = %v, want it to contain %s", resource.AuthorizationServers, f.publicURL)
	}
	f.getJSON(t, f.publicURL+"/.well-known/oauth-authorization-server", &f.metadata)
	m := f.metadata
	if m.Issuer != f.publicURL || !slices.Contains(m.CodeChallengeMethodsSupported, "S256") {
		t.Fatalf("authorization server metadata issuer %q, PKCE methods %v", m.Issuer, m.CodeChallengeMethodsSupported)
	}
	for name, endpoint := range map[string]string{
		"authorization": m.AuthorizationEndpoint, "token": m.TokenEndpoint,
		"registration": m.RegistrationEndpoint, "revocation": m.RevocationEndpoint,
	} {
		if !strings.HasPrefix(endpoint, f.publicURL+"/") {
			t.Fatalf("%s endpoint %q is not under %s", name, endpoint, f.publicURL)
		}
	}
}

func (f *flow) register(t *testing.T) {
	body, err := json.Marshal(map[string]any{"redirect_uris": []string{clientRedirectURI}, "client_name": "Integration Test"})
	if err != nil {
		t.Fatal(err)
	}
	req := f.request(t, http.MethodPost, f.metadata.RegistrationEndpoint, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	got := send(t, f.api, req)
	if got.status != http.StatusCreated {
		t.Fatalf("POST /register: status %d, body %s", got.status, got.body)
	}
	var client struct {
		ClientID     string   `json:"client_id"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	decode(t, got.body, &client)
	if client.ClientID == "" || !slices.Equal(client.RedirectURIs, []string{clientRedirectURI}) {
		t.Fatalf("registration returned client_id %q, redirect_uris %v", client.ClientID, client.RedirectURIs)
	}
	f.clientID = client.ClientID
}

func (f *flow) authorizeToConsent(t *testing.T) {
	got := f.startLogin(t)
	if got.status != http.StatusOK || got.url.Path != "/callback" {
		t.Fatalf("authorize ended at %s with status %d, want the consent page at /callback", got.url.Path, got.status)
	}
	if f.upstreamCode == "" {
		t.Fatal("mock-oidc did not redirect to /callback with a code")
	}
	if !bytes.Contains(got.body, []byte("Allow access to Mealie?")) {
		t.Fatalf("callback did not render consent: %s", got.body)
	}
	if policy := got.header.Get("Content-Security-Policy"); !strings.Contains(policy, "form-action 'self' http://"+clientHost) {
		t.Fatalf("consent CSP %q does not allow the client redirect origin", policy)
	}
	f.rememberBindingCookie(t)
	f.consentURL = formField(t, got.body, `action="([^"]+)"`)
	if f.consentURL != f.publicURL+"/consent" {
		t.Fatalf("consent form posts to %q, want %s/consent", f.consentURL, f.publicURL)
	}
	f.consentForm = url.Values{
		"pending_id": {formField(t, got.body, `name="pending_id" value="([^"]+)"`)},
		"csrf":       {formField(t, got.body, `name="csrf" value="([^"]+)"`)},
	}
}

func (f *flow) rememberBindingCookie(t *testing.T) {
	t.Helper()
	server, err := url.Parse(f.publicURL)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range f.browser.Jar.Cookies(server) {
		if cookie.Name == bindingCookie {
			_, binding, _ := strings.Cut(cookie.Value, ".")
			f.leaks.remember("browser binding secret", binding)
			return
		}
	}
	t.Fatalf("browser has no %s cookie for %s", bindingCookie, f.publicURL)
}

func (f *flow) allowConsent(t *testing.T) {
	form := maps.Clone(f.consentForm)
	form.Set("decision", "allow")
	got := f.postForm(t, f.browser, f.consentURL, form)
	f.code = f.clientRedirectCode(t, got)
}

func (f *flow) clientRedirectCode(t *testing.T, got reply) string {
	t.Helper()
	if got.status != http.StatusFound {
		t.Fatalf("status %d, want a 302 to the client: %s", got.status, got.body)
	}
	location, err := url.Parse(got.header.Get("Location"))
	if err != nil {
		t.Fatalf("parsing Location: %v", err)
	}
	q := location.Query()
	code := q.Get("code")
	f.leaks.remember("authorization code", code)
	if location.Scheme+"://"+location.Host+location.Path != clientRedirectURI {
		t.Fatalf("redirected to %s://%s%s, want %s", location.Scheme, location.Host, location.Path, clientRedirectURI)
	}
	if !strings.HasPrefix(code, "mmcp_ac_") || q.Get("state") != f.state || q.Get("iss") != f.publicURL {
		t.Fatalf("client redirect has code prefix ok=%t, state ok=%t, iss %q",
			strings.HasPrefix(code, "mmcp_ac_"), q.Get("state") == f.state, q.Get("iss"))
	}
	return code
}

func (f *flow) exchangeCode(t *testing.T) {
	got := f.postForm(t, f.api, f.metadata.TokenEndpoint, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {f.code},
		"redirect_uri":  {clientRedirectURI},
		"client_id":     {f.clientID},
		"code_verifier": {f.verifier},
		"resource":      {f.resourceURL()},
	})
	f.tokens = f.issuedTokens(t, got)
}

func (f *flow) issuedTokens(t *testing.T, got reply) tokens {
	t.Helper()
	if got.status != http.StatusOK {
		t.Fatalf("token endpoint: status %d, body %s", got.status, got.body)
	}
	var issued tokens
	decode(t, got.body, &issued)
	f.leaks.remember("MCP access token", issued.AccessToken)
	f.leaks.remember("MCP refresh token", issued.RefreshToken)
	if !strings.HasPrefix(issued.AccessToken, "mmcp_at_") || !strings.HasPrefix(issued.RefreshToken, "mmcp_rt_") {
		t.Fatal("token endpoint returned tokens without the mmcp_at_ / mmcp_rt_ prefixes")
	}
	if issued.TokenType != "Bearer" || issued.ExpiresIn <= 0 {
		t.Fatalf("token_type %q, expires_in %d", issued.TokenType, issued.ExpiresIn)
	}
	return issued
}

func (f *flow) connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "integration-test", Version: "1"}, nil)
	session, err := client.Connect(f.ctx, &mcp.StreamableClientTransport{
		Endpoint:   f.resourceURL(),
		HTTPClient: &http.Client{Timeout: requestTimeout, Transport: bearerTransport(f.tokens.AccessToken)},
	}, nil)
	if err != nil {
		t.Fatalf("connecting MCP client: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("closing MCP session: %v", err)
		}
	})
	return session
}

func (f *flow) whoami(t *testing.T) {
	session := f.connect(t)
	listed, err := session.ListTools(f.ctx, nil)
	if err != nil {
		t.Fatalf("listing tools: %v", err)
	}
	if !slices.ContainsFunc(listed.Tools, func(tool *mcp.Tool) bool { return tool.Name == "whoami" }) {
		t.Fatal("tools/list has no whoami")
	}
	result, err := session.CallTool(f.ctx, &mcp.CallToolParams{Name: "whoami"})
	if err != nil {
		t.Fatalf("calling whoami: %v", err)
	}
	assertWhoami(t, result)
}

func assertWhoami(t *testing.T, result *mcp.CallToolResult) {
	t.Helper()
	if result.IsError {
		t.Fatalf("whoami returned a tool error: %v", result.Content)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var who struct {
		Username string `json:"username"`
		Email    string `json:"email"`
	}
	decode(t, raw, &who)
	if who.Username == "" || who.Email != userEmail {
		t.Fatalf("whoami = %s, want a username and email %s", raw, userEmail)
	}
	t.Logf("Mealie provisioned username %q", who.Username)
}

func (f *flow) startLogin(t *testing.T) reply {
	t.Helper()
	f.verifier = rand.Text() + rand.Text()
	f.leaks.remember("PKCE verifier", f.verifier)
	f.state = rand.Text()
	challenge := sha256.Sum256([]byte(f.verifier))
	query := url.Values{
		"response_type":         {"code"},
		"client_id":             {f.clientID},
		"redirect_uri":          {clientRedirectURI},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		"state":                 {f.state},
		"resource":              {f.resourceURL()},
	}
	return send(t, f.browser, f.request(t, http.MethodGet, f.metadata.AuthorizationEndpoint+"?"+query.Encode(), nil))
}

func (f *flow) getJSON(t *testing.T, endpoint string, v any) {
	t.Helper()
	got := send(t, f.api, f.request(t, http.MethodGet, endpoint, nil))
	if got.status != http.StatusOK {
		t.Fatalf("GET %s: status %d", endpoint, got.status)
	}
	decode(t, got.body, v)
}

func (f *flow) postForm(t *testing.T, client *http.Client, endpoint string, form url.Values) reply {
	t.Helper()
	req := f.request(t, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return send(t, client, req)
}

func (f *flow) request(t *testing.T, method, endpoint string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(f.ctx, method, endpoint, body)
	if err != nil {
		t.Fatalf("building %s request: %v", method, err)
	}
	return req
}

func send(t *testing.T, client *http.Client, req *http.Request) reply {
	t.Helper()
	got := sendOnce(t, client, req)
	for attempt := 1; got.status == http.StatusTooManyRequests && attempt <= maxRateLimitRetries; attempt++ {
		retryAfter := got.header.Get("Retry-After")
		t.Logf("%s %s was rate limited, retrying after %ss", req.Method, req.URL.Path, retryAfter)
		waitRetryAfter(t, req.Context(), retryAfter)
		got = sendOnce(t, client, rewound(t, req))
	}
	return got
}

func rewound(t *testing.T, req *http.Request) *http.Request {
	t.Helper()
	retry := req.Clone(req.Context())
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			t.Fatalf("rewinding %s %s: %v", req.Method, req.URL.Path, err)
		}
		retry.Body = body
	}
	return retry
}

func waitRetryAfter(t *testing.T, ctx context.Context, retryAfter string) {
	t.Helper()
	seconds, err := strconv.Atoi(retryAfter)
	if err != nil || seconds <= 0 {
		t.Fatalf("429 without a usable Retry-After (%q)", retryAfter)
	}
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		t.Fatalf("waiting out the rate limit: %v", ctx.Err())
	case <-timer.C:
	}
}

func sendOnce(t *testing.T, client *http.Client, req *http.Request) reply {
	t.Helper()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s %s: %v", req.Method, req.URL.Path, err)
	}
	return reply{status: resp.StatusCode, header: resp.Header, body: body, url: resp.Request.URL}
}

func decode(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
}

func formField(t *testing.T, page []byte, pattern string) string {
	t.Helper()
	match := regexp.MustCompile(pattern).FindSubmatch(page)
	if match == nil {
		t.Fatalf("consent page has no match for %s", pattern)
	}
	return string(match[1])
}

type bearerTransport string

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(req)
}
