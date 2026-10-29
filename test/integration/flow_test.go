//go:build integration

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
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/app"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/audit"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/config"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/obs"
)

const (
	publicURL           = "http://it:18080"
	listenAddr          = ":18080"
	mealieURL           = "http://mealie:9000"
	resourceURL         = publicURL + "/mcp"
	callbackURL         = publicURL + "/callback"
	resourceMetadata    = publicURL + "/.well-known/oauth-protected-resource/mcp"
	clientRedirectURI   = "http://127.0.0.1:39999/cb"
	clientHost          = "127.0.0.1:39999"
	userEmail           = "it-user@example.com"
	bindingCookie       = "mmcp_auth"
	requestTimeout      = 30 * time.Second
	testTimeout         = 5 * time.Minute
	maxRedirects        = 10
	maxRateLimitRetries = 3
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type leakWatch map[string][]string

func (w leakWatch) remember(kind, value string) {
	w[kind] = append(w[kind], value)
}

func (w leakWatch) check(t *testing.T, logs string) {
	t.Helper()
	if !strings.Contains(logs, string(audit.LoginStarted)) {
		t.Error("server logs have no login_started audit event, so the leak check saw no login traffic")
	}
	for _, kind := range slices.Sorted(maps.Keys(w)) {
		if slices.ContainsFunc(w[kind], func(v string) bool { return v != "" && strings.Contains(logs, v) }) {
			t.Errorf("server logs leak a secret of kind %q", kind)
		}
	}
	if regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.`).MatchString(logs) {
		t.Error("server logs contain a JWT")
	}
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

type oauthError struct {
	Error string `json:"error"`
}

type flow struct {
	ctx          context.Context
	leaks        leakWatch
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
}

func TestFullFlow(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), testTimeout)
	defer cancel()
	logs := &syncBuffer{}
	leaks := leakWatch{}
	t.Cleanup(func() { leaks.check(t, logs.String()) })
	startServer(ctx, t, logs, leaks)
	f := newFlow(ctx, t, leaks)

	steps := []struct {
		name string
		run  func(*testing.T)
	}{
		{"unauthenticated_mcp_is_challenged", f.challenge},
		{"metadata_documents", f.fetchMetadata},
		{"register_client", f.register},
		{"authorize_through_mealie_shows_consent", f.authorizeToConsent},
		{"consent_allow_redirects_with_code", f.allowConsent},
		{"code_exchange_issues_tokens", f.exchangeCode},
		{"whoami_over_mcp", f.whoami},
		{"refresh_rotates_and_reuse_is_rejected", f.refreshAndReuse},
		{"second_authorize_skips_consent", f.authorizeWithoutConsent},
		{"revoke_ends_the_session", f.revoke},
	}
	for _, step := range steps {
		if !t.Run(step.name, step.run) {
			t.FailNow()
		}
	}
}

func startServer(ctx context.Context, t *testing.T, logs io.Writer, leaks leakWatch) {
	t.Helper()
	keyMaterial := make([]byte, 32)
	_, _ = rand.Read(keyMaterial)
	key := base64.StdEncoding.EncodeToString(keyMaterial)
	leaks.remember("encryption key", key)
	env := map[string]string{
		"PUBLIC_URL":          publicURL,
		"INSECURE_ALLOW_HTTP": "true",
		"MEALIE_URL":          mealieURL,
		"ENCRYPTION_KEYS":     "it:" + key,
		"DATA_DIR":            t.TempDir(),
		"LISTEN_ADDR":         listenAddr,
		"LOG_LEVEL":           "debug",
	}
	cfg, err := config.Load(func(name string) (string, bool) { v, ok := env[name]; return v, ok }, os.ReadFile)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	a, err := app.New(ctx, cfg, obs.NewLogger(logs, cfg.LogFormat, cfg.LogLevel), "integration")
	if err != nil {
		t.Fatalf("starting app against Mealie: %v", err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Errorf("closing app: %v", err)
		}
	})
	serve(ctx, t, cfg.ListenAddr, a.Handler())
}

func serve(ctx context.Context, t *testing.T, addr string, handler http.Handler) {
	t.Helper()
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", addr)
	if err != nil {
		t.Fatalf("listening on %s: %v", addr, err)
	}
	srv := &http.Server{Handler: handler, ReadHeaderTimeout: requestTimeout}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), requestTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutting down server: %v", err)
		}
		if err := <-served; !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("serving: %v", err)
		}
	})
}

func newFlow(ctx context.Context, t *testing.T, leaks leakWatch) *flow {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("creating cookie jar: %v", err)
	}
	f := &flow{
		ctx:   ctx,
		leaks: leaks,
		api: &http.Client{
			Timeout:       requestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
	f.browser = &http.Client{Timeout: requestTimeout, Jar: jar, CheckRedirect: f.followBrowserRedirect}
	return f
}

func (f *flow) followBrowserRedirect(req *http.Request, via []*http.Request) error {
	if req.URL.Host == clientHost {
		return http.ErrUseLastResponse
	}
	if req.URL.Scheme+"://"+req.URL.Host+req.URL.Path == callbackURL {
		f.upstreamCode = req.URL.Query().Get("code")
		f.leaks.remember("upstream authorization code", f.upstreamCode)
	}
	if len(via) >= maxRedirects {
		return errors.New("too many redirects")
	}
	return nil
}

func (f *flow) challenge(t *testing.T) {
	got := f.postMCP(t, "")
	if got.status != http.StatusUnauthorized {
		t.Fatalf("POST /mcp without token: status %d, want 401", got.status)
	}
	challenge := got.header.Get("WWW-Authenticate")
	if !strings.Contains(challenge, `resource_metadata="`+resourceMetadata+`"`) {
		t.Fatalf("WWW-Authenticate %q does not point at %s", challenge, resourceMetadata)
	}
}

func (f *flow) fetchMetadata(t *testing.T) {
	var resource resourceDocument
	f.getJSON(t, resourceMetadata, &resource)
	if resource.Resource != resourceURL {
		t.Fatalf("protected resource metadata resource = %q, want %q", resource.Resource, resourceURL)
	}
	if !slices.Contains(resource.AuthorizationServers, publicURL) {
		t.Fatalf("authorization_servers = %v, want it to contain %s", resource.AuthorizationServers, publicURL)
	}
	f.getJSON(t, publicURL+"/.well-known/oauth-authorization-server", &f.metadata)
	m := f.metadata
	if m.Issuer != publicURL || !slices.Contains(m.CodeChallengeMethodsSupported, "S256") {
		t.Fatalf("authorization server metadata issuer %q, PKCE methods %v", m.Issuer, m.CodeChallengeMethodsSupported)
	}
	for name, endpoint := range map[string]string{
		"authorization": m.AuthorizationEndpoint, "token": m.TokenEndpoint,
		"registration": m.RegistrationEndpoint, "revocation": m.RevocationEndpoint,
	} {
		if !strings.HasPrefix(endpoint, publicURL+"/") {
			t.Fatalf("%s endpoint %q is not under %s", name, endpoint, publicURL)
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
	if f.consentURL != publicURL+"/consent" {
		t.Fatalf("consent form posts to %q, want %s/consent", f.consentURL, publicURL)
	}
	f.consentForm = url.Values{
		"pending_id": {formField(t, got.body, `name="pending_id" value="([^"]+)"`)},
		"csrf":       {formField(t, got.body, `name="csrf" value="([^"]+)"`)},
	}
}

func (f *flow) rememberBindingCookie(t *testing.T) {
	t.Helper()
	server, err := url.Parse(publicURL)
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
	t.Fatalf("browser has no %s cookie for %s", bindingCookie, publicURL)
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
	if !strings.HasPrefix(code, "mmcp_ac_") || q.Get("state") != f.state || q.Get("iss") != publicURL {
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
		"resource":      {resourceURL},
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

func (f *flow) whoami(t *testing.T) {
	client := mcp.NewClient(&mcp.Implementation{Name: "integration-test", Version: "1"}, nil)
	session, err := client.Connect(f.ctx, &mcp.StreamableClientTransport{
		Endpoint:   resourceURL,
		HTTPClient: &http.Client{Timeout: requestTimeout, Transport: bearerTransport(f.tokens.AccessToken)},
	}, nil)
	if err != nil {
		t.Fatalf("connecting MCP client: %v", err)
	}
	defer func() {
		if err := session.Close(); err != nil {
			t.Errorf("closing MCP session: %v", err)
		}
	}()
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

func (f *flow) refreshAndReuse(t *testing.T) {
	previous := f.tokens
	next := f.issuedTokens(t, f.refresh(t, previous.RefreshToken))
	if next.AccessToken == previous.AccessToken || next.RefreshToken == previous.RefreshToken {
		t.Fatal("refresh returned the same tokens")
	}
	if status := f.postMCP(t, next.AccessToken).status; status != http.StatusOK {
		t.Fatalf("POST /mcp with the refreshed access token: status %d, want 200", status)
	}
	assertInvalidGrant(t, f.refresh(t, previous.RefreshToken))
	if status := f.postMCP(t, next.AccessToken).status; status != http.StatusUnauthorized {
		t.Fatalf("after refresh-token reuse, POST /mcp: status %d, want 401 because reuse ends the session", status)
	}
}

func (f *flow) authorizeWithoutConsent(t *testing.T) {
	f.upstreamCode = ""
	got := f.startLogin(t)
	if f.upstreamCode == "" {
		t.Fatal("second login did not pass through mock-oidc to /callback")
	}
	f.code = f.clientRedirectCode(t, got)
}

func (f *flow) revoke(t *testing.T) {
	f.exchangeCode(t)
	if status := f.postMCP(t, f.tokens.AccessToken).status; status != http.StatusOK {
		t.Fatalf("POST /mcp with the second login's token: status %d, want 200", status)
	}
	got := f.postForm(t, f.api, f.metadata.RevocationEndpoint, url.Values{
		"token":           {f.tokens.AccessToken},
		"token_type_hint": {"access_token"},
		"client_id":       {f.clientID},
	})
	if got.status != http.StatusOK {
		t.Fatalf("POST /revoke: status %d, body %s", got.status, got.body)
	}
	if status := f.postMCP(t, f.tokens.AccessToken).status; status != http.StatusUnauthorized {
		t.Fatalf("POST /mcp after revoke: status %d, want 401", status)
	}
	assertInvalidGrant(t, f.refresh(t, f.tokens.RefreshToken))
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
		"resource":              {resourceURL},
	}
	return send(t, f.browser, f.request(t, http.MethodGet, f.metadata.AuthorizationEndpoint+"?"+query.Encode(), nil))
}

func (f *flow) refresh(t *testing.T, refreshToken string) reply {
	t.Helper()
	return f.postForm(t, f.api, f.metadata.TokenEndpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {f.clientID},
	})
}

func assertInvalidGrant(t *testing.T, got reply) {
	t.Helper()
	var body oauthError
	decode(t, got.body, &body)
	if got.status != http.StatusBadRequest || body.Error != "invalid_grant" {
		t.Fatalf("status %d, error %q, want 400 invalid_grant", got.status, body.Error)
	}
}

func (f *flow) postMCP(t *testing.T, accessToken string) reply {
	t.Helper()
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"integration-test","version":"1"}}}`
	req := f.request(t, http.MethodPost, resourceURL, strings.NewReader(initialize))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+accessToken)
	}
	return send(t, f.api, req)
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
