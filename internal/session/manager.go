package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/sync/singleflight"

	"github.com/louistrinczek/mealie-oauth-mcp/internal/audit"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/mealie"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/secret"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/store"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/token"
	"github.com/louistrinczek/mealie-oauth-mcp/internal/tokencrypt"
)

const (
	mealieScope  = "mealie"
	sessionIDKey = "session_id"
)

var (
	ErrUnavailable = mealie.ErrUnavailable
	errInternal    = errors.New("internal error")
)

type Store interface {
	GetAccessToken(ctx context.Context, hash []byte) (store.AccessToken, error)
	GetSession(ctx context.Context, id string) (store.Session, error)
	UpdateSessionMealieToken(ctx context.Context, id string, ct []byte, expiresAt time.Time, lifetime time.Duration) error
	TouchSession(ctx context.Context, id string, now time.Time) error
	RevokeSession(ctx context.Context, id string, now time.Time) error
}

type TokenRefresher interface {
	RefreshToken(ctx context.Context, current secret.Secret) (mealie.Token, error)
}

type Deps struct {
	Store    Store
	Keyring  *tokencrypt.Keyring
	Mealie   TokenRefresher
	Audit    *audit.Logger
	Logger   *slog.Logger
	Resource string
	Now      func() time.Time
}

type Manager struct {
	store    Store
	keyring  *tokencrypt.Keyring
	mealie   TokenRefresher
	audit    *audit.Logger
	logger   *slog.Logger
	resource string
	now      func() time.Time
	renewals singleflight.Group
}

func New(d Deps) *Manager {
	return &Manager{
		store:    d.Store,
		keyring:  d.Keyring,
		mealie:   d.Mealie,
		audit:    d.Audit,
		logger:   d.Logger,
		resource: d.Resource,
		now:      d.Now,
	}
}

func (m *Manager) Verify(ctx context.Context, tok string, _ *http.Request) (*auth.TokenInfo, error) {
	if !token.HasKind(tok, token.Access) {
		return nil, auth.ErrInvalidToken
	}
	access, err := m.store.GetAccessToken(ctx, token.Hash(tok))
	if err != nil {
		return nil, m.lookupFailure(ctx, "getting access token", err)
	}
	if !m.now().Before(access.ExpiresAt) {
		return nil, auth.ErrInvalidToken
	}
	session, err := m.store.GetSession(ctx, access.SessionID)
	if err != nil {
		return nil, m.lookupFailure(ctx, "getting session", err)
	}
	if !m.usable(session) {
		return nil, auth.ErrInvalidToken
	}
	if !m.now().Before(session.MealieTokenExpiresAt) {
		m.revoke(ctx, session.ID, audit.SessionExpired)
		return nil, auth.ErrInvalidToken
	}
	if err := m.renewIfDue(ctx, session); err != nil {
		return nil, err
	}
	m.touch(ctx, session.ID)
	return &auth.TokenInfo{
		UserID:     session.MealieUserID,
		Expiration: access.ExpiresAt,
		Scopes:     []string{mealieScope},
		Extra:      map[string]any{sessionIDKey: session.ID},
	}, nil
}

func (m *Manager) MealieToken(ctx context.Context, info *auth.TokenInfo) (secret.Secret, error) {
	id, ok := sessionID(info)
	if !ok {
		return secret.Secret{}, auth.ErrInvalidToken
	}
	session, err := m.store.GetSession(ctx, id)
	if err != nil {
		return secret.Secret{}, m.lookupFailure(ctx, "getting session", err)
	}
	if !session.RevokedAt.IsZero() {
		return secret.Secret{}, auth.ErrInvalidToken
	}
	return m.openMealieToken(ctx, session)
}

func (m *Manager) ReportUnauthorized(ctx context.Context, info *auth.TokenInfo) {
	if id, ok := sessionID(info); ok {
		m.revoke(ctx, id, audit.SessionRevoked)
	}
}

func (m *Manager) usable(session store.Session) bool {
	return session.RevokedAt.IsZero() && m.now().Before(session.ExpiresAt) && session.Resource == m.resource
}

func (m *Manager) renewIfDue(ctx context.Context, session store.Session) error {
	if m.now().Before(renewalDue(session)) {
		return nil
	}
	_, err, _ := m.renewals.Do(session.ID, func() (any, error) {
		return nil, m.renew(context.WithoutCancel(ctx), session.ID)
	})
	return err
}

func (m *Manager) renew(ctx context.Context, id string) error {
	session, err := m.store.GetSession(ctx, id)
	if err != nil {
		return m.lookupFailure(ctx, "getting session", err)
	}
	if !session.RevokedAt.IsZero() {
		return auth.ErrInvalidToken
	}
	if m.now().Before(renewalDue(session)) {
		return nil
	}
	current, err := m.openMealieToken(ctx, session)
	if err != nil {
		return err
	}
	fresh, err := m.mealie.RefreshToken(ctx, current)
	if err != nil {
		return m.refreshFailure(ctx, id, err)
	}
	if fresh.AccessToken.Reveal() == "" || fresh.ExpiresIn <= 0 {
		m.logger.WarnContext(ctx, "mealie renewal returned an unusable token, keeping the current one", slog.String("session_id", id))
		return nil
	}
	return m.storeRenewed(ctx, id, fresh)
}

func (m *Manager) refreshFailure(ctx context.Context, id string, err error) error {
	if mealie.IsUnauthorized(err) {
		m.revoke(ctx, id, audit.SessionRevoked)
		return auth.ErrInvalidToken
	}
	m.logger.WarnContext(ctx, "mealie renewal failed, keeping the current token", slog.String("session_id", id), failureAttr(err))
	return nil
}

func failureAttr(err error) slog.Attr {
	var apiErr *mealie.APIError
	if errors.As(err, &apiErr) {
		return slog.Int("mealie_status", apiErr.Status)
	}
	return slog.String("error_type", fmt.Sprintf("%T", err))
}

func (m *Manager) storeRenewed(ctx context.Context, id string, fresh mealie.Token) error {
	ct, err := m.keyring.Seal(fresh.AccessToken.Bytes(), sessionAAD(id))
	if err != nil {
		return m.internal(ctx, "sealing renewed token", err)
	}
	if err := m.store.UpdateSessionMealieToken(ctx, id, ct, m.now().Add(fresh.ExpiresIn), fresh.ExpiresIn); err != nil {
		return m.internal(ctx, "storing renewed token", err)
	}
	m.audit.Event(ctx, audit.MealieTokenRenewed, slog.String("session_id", id))
	return nil
}

func (m *Manager) openMealieToken(ctx context.Context, session store.Session) (secret.Secret, error) {
	plain, err := m.keyring.Open(session.MealieTokenCT, sessionAAD(session.ID))
	if isDecryptionFailure(err) {
		m.logger.WarnContext(ctx, "mealie token cannot be decrypted, revoking the session", slog.String("session_id", session.ID), slog.Any("error", err))
		m.revoke(ctx, session.ID, audit.SessionRevoked)
		return secret.Secret{}, auth.ErrInvalidToken
	}
	if err != nil {
		return secret.Secret{}, m.internal(ctx, "opening mealie token", err)
	}
	return secret.New(plain), nil
}

func isDecryptionFailure(err error) bool {
	return errors.Is(err, tokencrypt.ErrUnknownKey) || errors.Is(err, tokencrypt.ErrDecrypt)
}

func (m *Manager) revoke(ctx context.Context, id string, event audit.EventType) {
	if err := m.store.RevokeSession(ctx, id, m.now()); err != nil {
		m.logger.ErrorContext(ctx, "revoking session failed", slog.String("session_id", id), slog.Any("error", err))
		return
	}
	m.audit.Event(ctx, event, slog.String("session_id", id))
}

func (m *Manager) touch(ctx context.Context, id string) {
	if err := m.store.TouchSession(ctx, id, m.now()); err != nil {
		m.logger.WarnContext(ctx, "touching session failed", slog.String("session_id", id), slog.Any("error", err))
	}
}

func (m *Manager) lookupFailure(ctx context.Context, action string, err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return auth.ErrInvalidToken
	}
	return m.internal(ctx, action, err)
}

func (m *Manager) internal(ctx context.Context, action string, err error) error {
	m.logger.ErrorContext(ctx, action+" failed", slog.Any("error", err))
	return errInternal
}

func renewalDue(session store.Session) time.Time {
	return session.MealieTokenExpiresAt.Add(-session.MealieTokenLifetime / 2)
}

func sessionAAD(id string) []byte {
	return []byte("session:" + id)
}

func sessionID(info *auth.TokenInfo) (string, bool) {
	if info == nil {
		return "", false
	}
	id, ok := info.Extra[sessionIDKey].(string)
	return id, ok
}
