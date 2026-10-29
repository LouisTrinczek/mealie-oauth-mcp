package session

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/audit"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/config"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/token"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/tokencrypt"
)

const (
	testResource      = "https://mcp.example.com/mcp"
	testSessionID     = "session-1"
	testClientID      = "client-1"
	testUserID        = "user-1"
	testMealieToken   = "mealie-token-original"
	testMealieRenewed = "mealie-token-renewed"
	testLifetime      = 48 * time.Hour
	testAccessTTL     = time.Hour
	testSessionMaxAge = 720 * time.Hour
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

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type fakeMealie struct {
	calls   atomic.Int32
	refresh func(context.Context, secret.Secret) (mealie.Token, error)
}

func (m *fakeMealie) RefreshToken(ctx context.Context, current secret.Secret) (mealie.Token, error) {
	m.calls.Add(1)
	return m.refresh(ctx, current)
}

func renewedToken(context.Context, secret.Secret) (mealie.Token, error) {
	return mealie.Token{AccessToken: secret.FromString(testMealieRenewed), ExpiresIn: testLifetime}, nil
}

type testEnv struct {
	manager *Manager
	store   *store.Store
	keyring *tokencrypt.Keyring
	mealie  *fakeMealie
	clock   *fakeClock
	logs    *syncBuffer
	access  string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	s, err := store.Open(t.Context(), t.TempDir(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	keyring, err := tokencrypt.NewKeyring([]config.EncryptionKey{{ID: "k1", Material: secret.New(bytes.Repeat([]byte{7}, 32))}})
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	env := &testEnv{
		store:   s,
		keyring: keyring,
		mealie:  &fakeMealie{refresh: renewedToken},
		clock:   &fakeClock{now: time.Now().Truncate(time.Second)},
		logs:    &syncBuffer{},
	}
	logger := slog.New(slog.NewJSONHandler(env.logs, nil))
	env.manager = New(Deps{
		Store:    s,
		Keyring:  keyring,
		Mealie:   env.mealie,
		Audit:    audit.New(logger),
		Logger:   logger,
		Resource: testResource,
		Now:      env.clock.Now,
	})
	env.seedSession(t, testResource)
	return env
}

func (e *testEnv) seedSession(t *testing.T, resource string) {
	t.Helper()
	ctx := t.Context()
	now := e.clock.Now()
	if err := e.store.UpsertClient(ctx, store.Client{ID: testClientID, Source: "dcr", Name: "App", RedirectURIs: []string{"http://127.0.0.1/cb"}}); err != nil {
		t.Fatalf("UpsertClient: %v", err)
	}
	ct, err := e.keyring.Seal([]byte(testMealieToken), []byte("session:"+testSessionID))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	session := store.Session{
		ID:                   testSessionID,
		ClientID:             testClientID,
		MealieUserID:         testUserID,
		MealieUsername:       "alice",
		MealieTokenCT:        ct,
		MealieTokenExpiresAt: now.Add(testLifetime),
		MealieTokenLifetime:  testLifetime,
		Resource:             resource,
		CreatedAt:            now,
		ExpiresAt:            now.Add(testSessionMaxAge),
		LastUsedAt:           now,
	}
	code := store.AuthCode{Hash: token.Hash("code"), SessionID: testSessionID, ClientID: testClientID, RedirectURI: "http://127.0.0.1/cb", CodeChallenge: "c", Resource: resource, ExpiresAt: now.Add(time.Minute)}
	if err := e.store.CreateSessionWithCode(ctx, session, code); err != nil {
		t.Fatalf("CreateSessionWithCode: %v", err)
	}
	access, _, err := token.New(token.Access)
	if err != nil {
		t.Fatalf("token.New: %v", err)
	}
	e.access = access.Reveal()
	accessRow := store.AccessToken{Hash: token.Hash(e.access), SessionID: testSessionID, ExpiresAt: now.Add(testAccessTTL)}
	refresh := store.RefreshToken{Hash: token.Hash("refresh"), SessionID: testSessionID, ExpiresAt: now.Add(testSessionMaxAge)}
	if err := e.store.IssueTokens(ctx, accessRow, refresh); err != nil {
		t.Fatalf("IssueTokens: %v", err)
	}
}

func (e *testEnv) storedSession(t *testing.T) store.Session {
	t.Helper()
	session, err := e.store.GetSession(t.Context(), testSessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	return session
}

func (e *testEnv) requireRevoked(t *testing.T) {
	t.Helper()
	if e.storedSession(t).RevokedAt.IsZero() {
		t.Fatal("session was not revoked")
	}
}

func (e *testEnv) requireNotRevoked(t *testing.T) {
	t.Helper()
	if !e.storedSession(t).RevokedAt.IsZero() {
		t.Fatal("session was revoked")
	}
}

func (e *testEnv) verify(t *testing.T) (*auth.TokenInfo, error) {
	t.Helper()
	return e.manager.Verify(t.Context(), e.access, nil)
}

func TestVerify_Valid(t *testing.T) {
	env := newTestEnv(t)
	info, err := env.verify(t)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if info.UserID != testUserID {
		t.Errorf("UserID = %q, want %q", info.UserID, testUserID)
	}
	if want := env.clock.Now().Add(testAccessTTL); !info.Expiration.Equal(want) {
		t.Errorf("Expiration = %v, want %v", info.Expiration, want)
	}
	if len(info.Scopes) != 1 || info.Scopes[0] != "mealie" {
		t.Errorf("Scopes = %v, want [mealie]", info.Scopes)
	}
	if info.Extra["session_id"] != testSessionID {
		t.Errorf("Extra = %v, want session_id %q", info.Extra, testSessionID)
	}
	if env.mealie.calls.Load() != 0 {
		t.Errorf("RefreshToken called %d times on a fresh token", env.mealie.calls.Load())
	}
}

func TestVerify_TouchesSession(t *testing.T) {
	env := newTestEnv(t)
	env.clock.advance(10 * time.Minute)
	if _, err := env.verify(t); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := env.storedSession(t).LastUsedAt; !got.Equal(env.clock.Now()) {
		t.Errorf("LastUsedAt = %v, want %v", got, env.clock.Now())
	}
}

func TestVerify_Rejected(t *testing.T) {
	unknown, _, err := token.New(token.Access)
	if err != nil {
		t.Fatalf("token.New: %v", err)
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, env *testEnv)
		token func(env *testEnv) string
	}{
		{"WrongPrefix", nil, func(*testEnv) string { return "mmcp_rt_" + strings.Repeat("a", 43) }},
		{"Unknown", nil, func(*testEnv) string { return unknown.Reveal() }},
		{"ExpiredAccessToken", func(t *testing.T, env *testEnv) { env.clock.advance(testAccessTTL) }, nil},
		{"RevokedSession", func(t *testing.T, env *testEnv) {
			if err := env.store.RevokeSession(t.Context(), testSessionID, env.clock.Now()); err != nil {
				t.Fatalf("RevokeSession: %v", err)
			}
		}, nil},
		{"WrongResource", func(t *testing.T, env *testEnv) { env.manager.resource = "https://other.example.com/mcp" }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			if tt.setup != nil {
				tt.setup(t, env)
			}
			tok := env.access
			if tt.token != nil {
				tok = tt.token(env)
			}
			_, err := env.manager.Verify(t.Context(), tok, nil)
			if !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("Verify error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func TestVerify_SessionCap(t *testing.T) {
	env := newTestEnv(t)
	beyondCap := testSessionMaxAge + 2*time.Hour
	if err := env.store.UpdateSessionMealieToken(t.Context(), testSessionID, env.storedSession(t).MealieTokenCT, env.clock.Now().Add(beyondCap), beyondCap); err != nil {
		t.Fatalf("UpdateSessionMealieToken: %v", err)
	}
	env.issueAccessToken(t, testSessionMaxAge+time.Hour)
	env.clock.advance(testSessionMaxAge)
	_, err := env.verify(t)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("Verify error = %v, want ErrInvalidToken", err)
	}
	if env.mealie.calls.Load() != 0 {
		t.Error("RefreshToken must not be called past the session cap")
	}
	if strings.Contains(env.logs.String(), `"event":"session_expired"`) {
		t.Error("the Mealie expiry rule rejected the token, not the session cap")
	}
}

func TestVerify_RenewsPastHalfLife(t *testing.T) {
	env := newTestEnv(t)
	env.clock.advance(25 * time.Hour)
	env.issueAccessToken(t, 2*time.Hour)
	if _, err := env.verify(t); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := env.mealie.calls.Load(); got != 1 {
		t.Fatalf("RefreshToken calls = %d, want 1", got)
	}
	stored := env.storedSession(t)
	if want := env.clock.Now().Add(testLifetime); !stored.MealieTokenExpiresAt.Equal(want) {
		t.Errorf("MealieTokenExpiresAt = %v, want %v", stored.MealieTokenExpiresAt, want)
	}
	if stored.MealieTokenLifetime != testLifetime {
		t.Errorf("MealieTokenLifetime = %v, want %v", stored.MealieTokenLifetime, testLifetime)
	}
	plain, err := env.keyring.Open(stored.MealieTokenCT, []byte("session:"+testSessionID))
	if err != nil || string(plain) != testMealieRenewed {
		t.Fatalf("stored token = %q, %v; want the renewed token sealed with the session AAD", plain, err)
	}
	if !strings.Contains(env.logs.String(), `"event":"mealie_renewed"`) {
		t.Errorf("no mealie_renewed audit event in %s", env.logs.String())
	}
}

func TestVerify_BeforeHalfLifeDoesNotRenew(t *testing.T) {
	env := newTestEnv(t)
	env.clock.advance(23 * time.Hour)
	env.issueAccessToken(t, 2*time.Hour)
	if _, err := env.verify(t); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got := env.mealie.calls.Load(); got != 0 {
		t.Fatalf("RefreshToken calls = %d, want 0", got)
	}
}

func TestVerify_RenewSendsCurrentMealieToken(t *testing.T) {
	env := newTestEnv(t)
	var sent string
	env.mealie.refresh = func(ctx context.Context, current secret.Secret) (mealie.Token, error) {
		sent = current.Reveal()
		return renewedToken(ctx, current)
	}
	env.clock.advance(25 * time.Hour)
	env.issueAccessToken(t, 2*time.Hour)
	if _, err := env.verify(t); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if sent != testMealieToken {
		t.Errorf("RefreshToken received %q, want the stored token", sent)
	}
}

func TestVerify_ConcurrentRenewOnce(t *testing.T) {
	env := newTestEnv(t)
	release := make(chan struct{})
	env.mealie.refresh = func(ctx context.Context, current secret.Secret) (mealie.Token, error) {
		<-release
		return renewedToken(ctx, current)
	}
	env.clock.advance(25 * time.Hour)
	env.issueAccessToken(t, 2*time.Hour)

	const callers = 20
	var started, done sync.WaitGroup
	started.Add(callers)
	done.Add(callers)
	errs := make(chan error, callers)
	for range callers {
		go func() {
			defer done.Done()
			started.Done()
			_, err := env.verify(t)
			errs <- err
		}()
	}
	started.Wait()
	for env.mealie.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	close(release)
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Verify: %v", err)
		}
	}
	if got := env.mealie.calls.Load(); got != 1 {
		t.Fatalf("RefreshToken calls = %d, want exactly 1", got)
	}
}

func TestVerify_MealieTokenExpired(t *testing.T) {
	env := newTestEnv(t)
	env.clock.advance(49 * time.Hour)
	env.issueAccessToken(t, 2*time.Hour)
	_, err := env.verify(t)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("Verify error = %v, want ErrInvalidToken", err)
	}
	env.requireRevoked(t)
	if env.mealie.calls.Load() != 0 {
		t.Error("an expired Mealie token must not be refreshed")
	}
	if !strings.Contains(env.logs.String(), `"event":"session_expired"`) {
		t.Errorf("no session_expired audit event in %s", env.logs.String())
	}
}

func TestVerify_MealieRefresh401RevokesSession(t *testing.T) {
	env := newTestEnv(t)
	env.mealie.refresh = func(context.Context, secret.Secret) (mealie.Token, error) {
		return mealie.Token{}, &mealie.APIError{Status: http.StatusUnauthorized, Message: "bad"}
	}
	env.clock.advance(25 * time.Hour)
	env.issueAccessToken(t, 2*time.Hour)
	_, err := env.verify(t)
	if !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("Verify error = %v, want ErrInvalidToken", err)
	}
	env.requireRevoked(t)
}

func TestVerify_MealieOutageKeepsSession(t *testing.T) {
	env := newTestEnv(t)
	env.mealie.refresh = func(context.Context, secret.Secret) (mealie.Token, error) {
		return mealie.Token{}, &mealie.APIError{Status: http.StatusServiceUnavailable, Message: "down"}
	}
	env.clock.advance(25 * time.Hour)
	env.issueAccessToken(t, 2*time.Hour)
	if _, err := env.verify(t); err != nil {
		t.Fatalf("Verify during outage: %v", err)
	}
	env.requireNotRevoked(t)
	stored := env.storedSession(t)
	if plain, err := env.keyring.Open(stored.MealieTokenCT, []byte("session:"+testSessionID)); err != nil || string(plain) != testMealieToken {
		t.Errorf("stored token changed during outage: %q, %v", plain, err)
	}
	if !strings.Contains(env.logs.String(), `"level":"WARN"`) {
		t.Errorf("no warning logged in %s", env.logs.String())
	}
}

func TestVerify_UnusableRenewalIsTreatedAsOutage(t *testing.T) {
	tests := []struct {
		name  string
		token mealie.Token
	}{
		{"EmptyAccessToken", mealie.Token{ExpiresIn: testLifetime}},
		{"ZeroLifetime", mealie.Token{AccessToken: secret.FromString(testMealieRenewed)}},
		{"NegativeLifetime", mealie.Token{AccessToken: secret.FromString(testMealieRenewed), ExpiresIn: -time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			env.mealie.refresh = func(context.Context, secret.Secret) (mealie.Token, error) { return tt.token, nil }
			env.clock.advance(25 * time.Hour)
			env.issueAccessToken(t, 2*time.Hour)
			if _, err := env.verify(t); err != nil {
				t.Fatalf("Verify: %v", err)
			}
			env.requireNotRevoked(t)
			stored := env.storedSession(t)
			if stored.MealieTokenLifetime != testLifetime {
				t.Errorf("lifetime = %v, want unchanged %v", stored.MealieTokenLifetime, testLifetime)
			}
			if plain, err := env.keyring.Open(stored.MealieTokenCT, []byte("session:"+testSessionID)); err != nil || string(plain) != testMealieToken {
				t.Errorf("stored token changed: %q, %v", plain, err)
			}
			if !strings.Contains(env.logs.String(), `"level":"WARN"`) {
				t.Errorf("no warning logged in %s", env.logs.String())
			}
		})
	}
}

func TestVerify_ErrorMessagesGeneric(t *testing.T) {
	env := newTestEnv(t)
	env.mealie.refresh = func(context.Context, secret.Secret) (mealie.Token, error) {
		return mealie.Token{}, errors.New("upstream said " + testMealieToken)
	}
	env.clock.advance(25 * time.Hour)
	env.issueAccessToken(t, 2*time.Hour)
	if _, err := env.verify(t); err != nil {
		t.Fatalf("Verify after an unclassified refresh failure: %v", err)
	}
	env.clock.advance(time.Hour)
	_, expiredErr := env.manager.Verify(t.Context(), "mmcp_rt_"+strings.Repeat("a", 43), nil)
	for _, err := range []error{expiredErr} {
		message := err.Error()
		if message != "invalid token" && message != "internal error" {
			t.Errorf("error message %q is not generic", message)
		}
		if strings.Contains(message, env.access) || strings.Contains(message, testMealieToken) {
			t.Errorf("error message %q leaks a secret", message)
		}
	}
	if strings.Contains(env.logs.String(), env.access) || strings.Contains(env.logs.String(), testMealieToken) {
		t.Errorf("logs leak a secret: %s", env.logs.String())
	}
}

func TestVerify_StoreFailureIsInternalError(t *testing.T) {
	env := newTestEnv(t)
	if err := env.store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	_, err := env.verify(t)
	if err == nil || errors.Is(err, auth.ErrInvalidToken) || err.Error() != "internal error" {
		t.Fatalf("Verify error = %v, want internal error", err)
	}
}

func TestMealieToken_DecryptsWithSessionAAD(t *testing.T) {
	env := newTestEnv(t)
	info, err := env.verify(t)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	tok, err := env.manager.MealieToken(t.Context(), info)
	if err != nil {
		t.Fatalf("MealieToken: %v", err)
	}
	if tok.Reveal() != testMealieToken {
		t.Errorf("MealieToken = %q, want the stored token", tok.Reveal())
	}
}

func TestMealieToken_RevokedSession(t *testing.T) {
	env := newTestEnv(t)
	info, err := env.verify(t)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if err := env.store.RevokeSession(t.Context(), testSessionID, env.clock.Now()); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, err := env.manager.MealieToken(t.Context(), info); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("MealieToken error = %v, want ErrInvalidToken", err)
	}
}

func TestMealieToken_RejectsForeignInfo(t *testing.T) {
	env := newTestEnv(t)
	tests := []struct {
		name string
		info *auth.TokenInfo
	}{
		{"Nil", nil},
		{"NoExtra", &auth.TokenInfo{}},
		{"WrongType", &auth.TokenInfo{Extra: map[string]any{"session_id": 7}}},
		{"UnknownSession", &auth.TokenInfo{Extra: map[string]any{"session_id": "nope"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := env.manager.MealieToken(t.Context(), tt.info); !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("MealieToken error = %v, want ErrInvalidToken", err)
			}
		})
	}
}

func (e *testEnv) storeUndecryptableToken(t *testing.T, keyring *tokencrypt.Keyring, aad string) {
	t.Helper()
	ct, err := keyring.Seal([]byte(testMealieToken), []byte(aad))
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if err := e.store.UpdateSessionMealieToken(t.Context(), testSessionID, ct, e.clock.Now().Add(testLifetime), testLifetime); err != nil {
		t.Fatalf("UpdateSessionMealieToken: %v", err)
	}
}

func removedKeyring(t *testing.T) *tokencrypt.Keyring {
	t.Helper()
	keyring, err := tokencrypt.NewKeyring([]config.EncryptionKey{{ID: "k-removed", Material: secret.New(bytes.Repeat([]byte{9}, 32))}})
	if err != nil {
		t.Fatalf("NewKeyring: %v", err)
	}
	return keyring
}

func TestUndecryptableMealieTokenRevokesSession(t *testing.T) {
	tests := []struct {
		name string
		seal func(t *testing.T, env *testEnv)
		call func(t *testing.T, env *testEnv) error
	}{
		{
			name: "Verify with removed key",
			seal: func(t *testing.T, env *testEnv) {
				env.storeUndecryptableToken(t, removedKeyring(t), "session:"+testSessionID)
			},
			call: func(t *testing.T, env *testEnv) error {
				env.clock.advance(25 * time.Hour)
				env.issueAccessToken(t, 2*time.Hour)
				_, err := env.verify(t)
				return err
			},
		},
		{
			name: "MealieToken with removed key",
			seal: func(t *testing.T, env *testEnv) {
				env.storeUndecryptableToken(t, removedKeyring(t), "session:"+testSessionID)
			},
			call: func(t *testing.T, env *testEnv) error {
				_, err := env.manager.MealieToken(t.Context(), &auth.TokenInfo{Extra: map[string]any{"session_id": testSessionID}})
				return err
			},
		},
		{
			name: "MealieToken with foreign AAD",
			seal: func(t *testing.T, env *testEnv) {
				env.storeUndecryptableToken(t, env.keyring, "session:someone-else")
			},
			call: func(t *testing.T, env *testEnv) error {
				_, err := env.manager.MealieToken(t.Context(), &auth.TokenInfo{Extra: map[string]any{"session_id": testSessionID}})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			tt.seal(t, env)

			if err := tt.call(t, env); !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("error = %v, want ErrInvalidToken", err)
			}
			env.requireRevoked(t)
			if !strings.Contains(env.logs.String(), `"event":"session_revoked"`) {
				t.Errorf("no session_revoked audit event in %s", env.logs.String())
			}
			if env.mealie.calls.Load() != 0 {
				t.Error("RefreshToken must not be called with an undecryptable token")
			}
		})
	}
}

func TestReportUnauthorized_Revokes(t *testing.T) {
	env := newTestEnv(t)
	info, err := env.verify(t)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	env.manager.ReportUnauthorized(t.Context(), info)
	env.requireRevoked(t)
	if !strings.Contains(env.logs.String(), `"event":"session_revoked"`) {
		t.Errorf("no session_revoked audit event in %s", env.logs.String())
	}
	if _, err := env.verify(t); !errors.Is(err, auth.ErrInvalidToken) {
		t.Errorf("Verify after revoke = %v, want ErrInvalidToken", err)
	}
}

func TestReportUnauthorized_IgnoresForeignInfo(t *testing.T) {
	env := newTestEnv(t)
	env.manager.ReportUnauthorized(t.Context(), nil)
	env.manager.ReportUnauthorized(t.Context(), &auth.TokenInfo{})
	env.requireNotRevoked(t)
}

func TestRequireBearerToken_401HasResourceMetadata(t *testing.T) {
	env := newTestEnv(t)
	metadataURL := "https://mcp.example.com/.well-known/oauth-protected-resource/mcp"
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := auth.RequireBearerToken(env.manager.Verify, &auth.RequireBearerTokenOptions{
		ResourceMetadataURL: metadataURL,
		Scopes:              []string{"mealie"},
	})(next)

	tests := []struct {
		name       string
		header     string
		wantStatus int
	}{
		{"Missing", "", http.StatusUnauthorized},
		{"Invalid", "Bearer mmcp_at_" + strings.Repeat("z", 43), http.StatusUnauthorized},
		{"Valid", "Bearer " + env.access, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/mcp", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantStatus != http.StatusUnauthorized {
				return
			}
			if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, `resource_metadata="`+metadataURL+`"`) {
				t.Errorf("WWW-Authenticate = %q, want resource_metadata", got)
			}
		})
	}
}

func (e *testEnv) issueAccessToken(t *testing.T, ttl time.Duration) {
	t.Helper()
	access, _, err := token.New(token.Access)
	if err != nil {
		t.Fatalf("token.New: %v", err)
	}
	now := e.clock.Now()
	accessRow := store.AccessToken{Hash: token.Hash(access.Reveal()), SessionID: testSessionID, ExpiresAt: now.Add(ttl)}
	refresh := store.RefreshToken{Hash: token.Hash(token.RandomID()), SessionID: testSessionID, ExpiresAt: now.Add(testSessionMaxAge)}
	if err := e.store.IssueTokens(t.Context(), accessRow, refresh); err != nil {
		t.Fatalf("IssueTokens: %v", err)
	}
	e.access = access.Reveal()
}

type faultyStore struct {
	Store
	errs                 map[string]error
	failSessionReadsFrom int32
	sessionReads         atomic.Int32
	onSessionRead        func(read int32, s store.Session) store.Session
}

func (f *faultyStore) GetSession(ctx context.Context, id string) (store.Session, error) {
	read := f.sessionReads.Add(1)
	if err := f.errs["GetSession"]; err != nil && read >= max(f.failSessionReadsFrom, 1) {
		return store.Session{}, err
	}
	session, err := f.Store.GetSession(ctx, id)
	if err == nil && f.onSessionRead != nil {
		session = f.onSessionRead(read, session)
	}
	return session, err
}

func (f *faultyStore) RevokeSession(ctx context.Context, id string, now time.Time) error {
	if err := f.errs["RevokeSession"]; err != nil {
		return err
	}
	return f.Store.RevokeSession(ctx, id, now)
}

func (f *faultyStore) TouchSession(ctx context.Context, id string, now time.Time) error {
	if err := f.errs["TouchSession"]; err != nil {
		return err
	}
	return f.Store.TouchSession(ctx, id, now)
}

func (f *faultyStore) UpdateSessionMealieToken(ctx context.Context, id string, ct []byte, expiresAt time.Time, lifetime time.Duration) error {
	if err := f.errs["UpdateSessionMealieToken"]; err != nil {
		return err
	}
	return f.Store.UpdateSessionMealieToken(ctx, id, ct, expiresAt, lifetime)
}

func (e *testEnv) useFaultyStore(faulty *faultyStore) {
	faulty.Store = e.store
	e.manager.store = faulty
}

var errBoom = errors.New("boom")

func TestVerify_StoreFailures(t *testing.T) {
	tests := []struct {
		name    string
		advance time.Duration
		faulty  *faultyStore
		wantErr string
	}{
		{"SessionLookup", 0, &faultyStore{errs: map[string]error{"GetSession": errBoom}}, "internal error"},
		{"RenewReread", 25 * time.Hour, &faultyStore{errs: map[string]error{"GetSession": errBoom}, failSessionReadsFrom: 2}, "internal error"},
		{"StoreRenewed", 25 * time.Hour, &faultyStore{errs: map[string]error{"UpdateSessionMealieToken": errBoom}}, "internal error"},
		{"RevokeExpired", 49 * time.Hour, &faultyStore{errs: map[string]error{"RevokeSession": errBoom}}, "invalid token"},
		{"Touch", 0, &faultyStore{errs: map[string]error{"TouchSession": errBoom}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			env.useFaultyStore(tt.faulty)
			env.clock.advance(tt.advance)
			env.issueAccessToken(t, 2*time.Hour)
			_, err := env.verify(t)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Verify: %v", err)
			case tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr):
				t.Fatalf("Verify error = %v, want %q", err, tt.wantErr)
			}
			if !strings.Contains(env.logs.String(), `"level":"ERROR"`) && !strings.Contains(env.logs.String(), `"level":"WARN"`) {
				t.Errorf("failure was not logged: %s", env.logs.String())
			}
		})
	}
}

func TestVerify_RenewSkippedWhenSessionChangedMeanwhile(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(s store.Session) store.Session
		wantErr bool
	}{
		{"RevokedMeanwhile", func(s store.Session) store.Session { s.RevokedAt = s.CreatedAt; return s }, true},
		{"RenewedMeanwhile", func(s store.Session) store.Session {
			s.MealieTokenExpiresAt = s.MealieTokenExpiresAt.Add(testLifetime)
			return s
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			env.useFaultyStore(&faultyStore{onSessionRead: func(read int32, s store.Session) store.Session {
				if read == 2 {
					return tt.mutate(s)
				}
				return s
			}})
			env.clock.advance(25 * time.Hour)
			env.issueAccessToken(t, 2*time.Hour)
			_, err := env.verify(t)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("Verify error = %v, wantErr %v", err, tt.wantErr)
			}
			if env.mealie.calls.Load() != 0 {
				t.Error("RefreshToken must not be called")
			}
		})
	}
}

func TestVerify_UnexpectedRefreshErrorKeepsSession(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		wantInLog string
	}{
		{"Forbidden", &mealie.APIError{Status: http.StatusForbidden, Message: testMealieToken}, `"mealie_status":403`},
		{"Undecodable", errors.New("decoding " + testMealieToken), `"error_type":"*errors.errorString"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			env.mealie.refresh = func(context.Context, secret.Secret) (mealie.Token, error) { return mealie.Token{}, tt.err }
			env.clock.advance(25 * time.Hour)
			env.issueAccessToken(t, 2*time.Hour)
			if _, err := env.verify(t); err != nil {
				t.Fatalf("Verify: %v", err)
			}
			env.requireNotRevoked(t)
			stored := env.storedSession(t)
			if plain, err := env.keyring.Open(stored.MealieTokenCT, []byte("session:"+testSessionID)); err != nil || string(plain) != testMealieToken {
				t.Errorf("stored token changed: %q, %v", plain, err)
			}
			logs := env.logs.String()
			if !strings.Contains(logs, tt.wantInLog) || !strings.Contains(logs, `"level":"WARN"`) {
				t.Errorf("warning with %s not logged: %s", tt.wantInLog, logs)
			}
			if strings.Contains(logs, testMealieToken) {
				t.Errorf("logs leak the Mealie token: %s", logs)
			}
		})
	}
}
