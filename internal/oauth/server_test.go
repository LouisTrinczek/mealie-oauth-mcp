package oauth

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/audit"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/config"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/token"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/tokencrypt"
)

const (
	testClientID       = "dcr-client"
	testClientName     = "Test App"
	testClientRedirect = "http://127.0.0.1:7777/callback"
	testClientState    = "client-state-123"
	testClientVerifier = "client-verifier-0123456789-0123456789-abcdefghij"
	testMealieClientID = "mealie-oidc-client"
	testMealieScope    = "openid email profile"
	testMealieLifetime = 48 * time.Hour
	testUserID         = "user-1"
	testUsername       = "alice"
	expiredCopy        = "This login link expired or was already used. Start again from your AI app."
	mealieRefusedCopy  = "Mealie refused this login. Ask the Mealie admin to check the OIDC settings (signup, user claim)."
)

type fakeMealie struct {
	nativeConfig func(context.Context) (mealie.NativeConfig, error)
	nativeToken  func(context.Context, mealie.NativeTokenRequest) (mealie.Token, error)
	self         func(context.Context, secret.Secret) (mealie.User, error)
}

func (m *fakeMealie) NativeConfig(ctx context.Context) (mealie.NativeConfig, error) {
	return m.nativeConfig(ctx)
}

func (m *fakeMealie) NativeToken(ctx context.Context, req mealie.NativeTokenRequest) (mealie.Token, error) {
	return m.nativeToken(ctx, req)
}

func (m *fakeMealie) Self(ctx context.Context, tok secret.Secret) (mealie.User, error) {
	return m.self(ctx, tok)
}

type idpGrant struct {
	challenge   string
	nonce       string
	redirectURI string
}

type fakeIdP struct {
	server      *httptest.Server
	callbackURL string
	mu          sync.Mutex
	grants      map[string]idpGrant
	codes       []string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	idp := &fakeIdP{grants: map[string]idpGrant{}}
	idp.server = httptest.NewServer(http.HandlerFunc(idp.authorize))
	t.Cleanup(idp.server.Close)
	return idp
}

func (p *fakeIdP) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "pkce required", http.StatusBadRequest)
		return
	}
	if q.Get("redirect_uri") != p.callbackURL {
		http.Error(w, "unregistered redirect_uri", http.StatusBadRequest)
		return
	}
	code := "idp-code-" + token.RandomID()
	p.mu.Lock()
	p.grants[code] = idpGrant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirectURI: q.Get("redirect_uri")}
	p.codes = append(p.codes, code)
	p.mu.Unlock()
	http.Redirect(w, r, p.callbackURL+"?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
}

func (p *fakeIdP) redeem(req mealie.NativeTokenRequest) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	grant, ok := p.grants[req.Code.Reveal()]
	delete(p.grants, req.Code.Reveal())
	return ok && grant.redirectURI == req.RedirectURI && grant.nonce == req.Nonce && VerifyPKCE(grant.challenge, req.CodeVerifier)
}

func (p *fakeIdP) issuedCodes() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.codes...)
}

type fakeClientResolver struct {
	metadata map[string]ClientMetadata
	err      error
}

func (f *fakeClientResolver) Fetch(_ context.Context, clientID string) (ClientMetadata, error) {
	if f.err != nil {
		return ClientMetadata{}, f.err
	}
	md, ok := f.metadata[clientID]
	if !ok {
		return ClientMetadata{}, errors.New("no client metadata")
	}
	return md, nil
}

type recordingStore struct {
	Store
	mu       sync.Mutex
	sessions []store.Session
}

func (s *recordingStore) CreateSessionWithCode(ctx context.Context, session store.Session, code store.AuthCode) error {
	if err := s.Store.CreateSessionWithCode(ctx, session, code); err != nil {
		return err
	}
	s.mu.Lock()
	s.sessions = append(s.sessions, session)
	s.mu.Unlock()
	return nil
}

func (s *recordingStore) created() []store.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]store.Session(nil), s.sessions...)
}

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

type testEnv struct {
	srv          *Server
	http         *httptest.Server
	mealie       *fakeMealie
	idp          *fakeIdP
	clock        *fakeClock
	store        *store.Store
	recorder     *recordingStore
	cimd         *fakeClientResolver
	keyring      *tokencrypt.Keyring
	client       *http.Client
	logs         *syncBuffer
	mealieTokens []string
	verifiers    []string
}

func newTestServer(t *testing.T, configure ...func(*Config)) *testEnv {
	t.Helper()
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	env := &testEnv{
		http:  ts,
		idp:   newFakeIdP(t),
		clock: newFakeClock(),
		cimd:  &fakeClientResolver{metadata: map[string]ClientMetadata{}},
		logs:  &syncBuffer{},
	}
	env.idp.callbackURL = ts.URL + "/callback"
	env.store = openTestStore(t)
	env.recorder = &recordingStore{Store: env.store}
	env.keyring = newTestKeyring(t)
	env.mealie = env.defaultMealie()
	env.client = newBrowser(t)
	cfg := Config{
		Issuer:         ts.URL,
		Resource:       ts.URL + "/mcp",
		CallbackURL:    ts.URL + "/callback",
		AccessTokenTTL: time.Hour,
		SessionMaxAge:  720 * time.Hour,
	}
	for _, fn := range configure {
		fn(&cfg)
	}
	logger := slog.New(slog.NewJSONHandler(env.logs, nil))
	srv, err := New(cfg, Deps{
		Store:   env.recorder,
		Keyring: env.keyring,
		Mealie:  env.mealie,
		CIMD:    env.cimd,
		Audit:   audit.New(logger),
		Logger:  logger,
		Now:     env.clock.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv.Routes(mux)
	env.srv = srv
	env.seedClient(t, store.Client{ID: testClientID, Source: "dcr", Name: testClientName, RedirectURIs: []string{testClientRedirect}})
	return env
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(t.Context(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newTestKeyring(t *testing.T) *tokencrypt.Keyring {
	t.Helper()
	keyring, err := tokencrypt.NewKeyring([]config.EncryptionKey{{ID: "k1", Material: secret.New(bytes.Repeat([]byte{7}, 32))}})
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return keyring
}

func newBrowser(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{
		Jar:           jar,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (e *testEnv) defaultMealie() *fakeMealie {
	return &fakeMealie{
		nativeConfig: func(context.Context) (mealie.NativeConfig, error) {
			return mealie.NativeConfig{
				AuthorizationEndpoint: e.idp.server.URL + "/authorize",
				ClientID:              testMealieClientID,
				Scope:                 testMealieScope,
			}, nil
		},
		nativeToken: func(_ context.Context, req mealie.NativeTokenRequest) (mealie.Token, error) {
			e.verifiers = append(e.verifiers, req.CodeVerifier, req.Nonce)
			if !e.idp.redeem(req) {
				return mealie.Token{}, &mealie.APIError{Status: http.StatusUnauthorized, Message: "invalid code"}
			}
			tok := "mealie-token-" + token.RandomID()
			e.mealieTokens = append(e.mealieTokens, tok)
			return mealie.Token{AccessToken: secret.FromString(tok), ExpiresIn: testMealieLifetime}, nil
		},
		self: func(context.Context, secret.Secret) (mealie.User, error) {
			return mealie.User{ID: testUserID, Username: testUsername}, nil
		},
	}
}

func (e *testEnv) seedClient(t *testing.T, c store.Client) {
	t.Helper()
	c.CreatedAt = e.clock.Now()
	if err := e.store.UpsertClient(t.Context(), c); err != nil {
		t.Fatalf("UpsertClient: %v", err)
	}
}

func authorizeParams() url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {testClientID},
		"redirect_uri":          {testClientRedirect},
		"code_challenge":        {S256(testClientVerifier)},
		"code_challenge_method": {"S256"},
		"state":                 {testClientState},
	}
}

func (e *testEnv) withResource(params url.Values) url.Values {
	params.Set("resource", e.http.URL+"/mcp")
	return params
}

type reply struct {
	StatusCode int
	Header     http.Header
	cookies    []*http.Cookie
}

func (r reply) Cookies() []*http.Cookie {
	return r.cookies
}

func send(t *testing.T, client *http.Client, req *http.Request) (reply, string) {
	t.Helper()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", req.Method, req.URL.Path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	return reply{StatusCode: resp.StatusCode, Header: resp.Header, cookies: resp.Cookies()}, string(body)
}

func get(t *testing.T, client *http.Client, target string) (reply, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return send(t, client, req)
}

func postForm(t *testing.T, client *http.Client, target string, form url.Values) (reply, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return send(t, client, req)
}

func (e *testEnv) authorize(t *testing.T, params url.Values) (reply, string) {
	t.Helper()
	return get(t, e.client, e.http.URL+"/authorize?"+params.Encode())
}

func (e *testEnv) callbackURL(t *testing.T, params url.Values) string {
	t.Helper()
	resp, _ := e.authorize(t, params)
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize status = %d, want 302", resp.StatusCode)
	}
	idpResp, _ := get(t, e.client, resp.Header.Get("Location"))
	if idpResp.StatusCode != http.StatusFound {
		t.Fatalf("idp status = %d, want 302", idpResp.StatusCode)
	}
	return idpResp.Header.Get("Location")
}

func (e *testEnv) callback(t *testing.T, params url.Values) (reply, string) {
	t.Helper()
	return get(t, e.client, e.callbackURL(t, params))
}

type consentPage struct {
	body      string
	pendingID string
	csrf      string
}

var (
	pendingIDField = regexp.MustCompile(`name="pending_id" value="([^"]*)"`)
	csrfField      = regexp.MustCompile(`name="csrf" value="([^"]*)"`)
)

func (e *testEnv) loginToConsent(t *testing.T, params url.Values) consentPage {
	t.Helper()
	resp, body := e.callback(t, params)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d, want 200 consent page; body: %s", resp.StatusCode, body)
	}
	pending := pendingIDField.FindStringSubmatch(body)
	csrf := csrfField.FindStringSubmatch(body)
	if pending == nil || csrf == nil {
		t.Fatalf("consent page lacks pending_id or csrf: %s", body)
	}
	return consentPage{body: body, pendingID: pending[1], csrf: csrf[1]}
}

func (e *testEnv) decide(t *testing.T, client *http.Client, page consentPage, decision string) (reply, string) {
	t.Helper()
	return postForm(t, client, e.http.URL+"/consent", url.Values{
		"pending_id": {page.pendingID},
		"csrf":       {page.csrf},
		"decision":   {decision},
	})
}

func clientRedirect(t *testing.T, resp reply) url.Values {
	t.Helper()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", resp.StatusCode)
	}
	location, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parsing Location: %v", err)
	}
	if got := location.Scheme + "://" + location.Host + location.Path; got != testClientRedirect {
		t.Fatalf("redirected to %q, want %q", got, testClientRedirect)
	}
	return location.Query()
}

func (e *testEnv) wantClientError(t *testing.T, resp reply, code string) {
	t.Helper()
	q := clientRedirect(t, resp)
	if q.Get("error") != code {
		t.Fatalf("error = %q, want %q", q.Get("error"), code)
	}
	if q.Get("state") != testClientState {
		t.Fatalf("state = %q, want %q", q.Get("state"), testClientState)
	}
	if q.Get("iss") != e.http.URL {
		t.Fatalf("iss = %q, want %q", q.Get("iss"), e.http.URL)
	}
	if q.Has("code") {
		t.Fatal("error redirect carries a code")
	}
}

func (e *testEnv) wantCode(t *testing.T, resp reply) string {
	t.Helper()
	q := clientRedirect(t, resp)
	if q.Get("state") != testClientState || q.Get("iss") != e.http.URL {
		t.Fatalf("state = %q, iss = %q", q.Get("state"), q.Get("iss"))
	}
	code := q.Get("code")
	if !token.HasKind(code, token.Code) {
		t.Fatalf("code = %q, want an %s code", code, token.Code)
	}
	return code
}

func wantPage(t *testing.T, resp reply, body string, status int, wantText string) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, status, body)
	}
	if loc := resp.Header.Get("Location"); loc != "" {
		t.Fatalf("error page redirected to %q", loc)
	}
	if !strings.Contains(body, wantText) {
		t.Fatalf("body lacks %q: %s", wantText, body)
	}
}

func (e *testEnv) wantNoSession(t *testing.T) {
	t.Helper()
	if sessions := e.recorder.created(); len(sessions) != 0 {
		t.Fatalf("created %d sessions, want none", len(sessions))
	}
}

func (e *testEnv) onlySession(t *testing.T) store.Session {
	t.Helper()
	sessions := e.recorder.created()
	if len(sessions) != 1 {
		t.Fatalf("created %d sessions, want 1", len(sessions))
	}
	stored, err := e.store.GetSession(t.Context(), sessions[0].ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	return stored
}
