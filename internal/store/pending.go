package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const pendingAuthColumns = `id, client_id, redirect_uri, client_state, code_challenge, resource,
	browser_binding_hash, upstream_state_hash, upstream_verifier, upstream_nonce,
	mealie_user_id, mealie_username, mealie_token_ct, mealie_token_expires_at, mealie_token_lifetime_s,
	created_at, expires_at`

func scanPendingAuth(row rowScanner) (PendingAuth, error) {
	var (
		p                              PendingAuth
		userID, username               sql.NullString
		tokenExpiresAt, tokenLifetimeS sql.NullInt64
		createdAt, expiresAt           int64
	)
	err := row.Scan(&p.ID, &p.ClientID, &p.RedirectURI, &p.ClientState, &p.CodeChallenge, &p.Resource,
		&p.BrowserBindingHash, &p.UpstreamStateHash, &p.UpstreamVerifier, &p.UpstreamNonce,
		&userID, &username, &p.MealieTokenCT, &tokenExpiresAt, &tokenLifetimeS,
		&createdAt, &expiresAt)
	if err != nil {
		return PendingAuth{}, err
	}
	p.MealieUserID = userID.String
	p.MealieUsername = username.String
	p.MealieTokenExpiresAt = timeFromNullable(tokenExpiresAt)
	p.MealieTokenLifetime = durationFromNullable(tokenLifetimeS)
	p.CreatedAt = timeFromMillis(createdAt)
	p.ExpiresAt = timeFromMillis(expiresAt)
	return p, nil
}

func (s *Store) CreatePendingAuth(ctx context.Context, p PendingAuth) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pending_auths (id, client_id, redirect_uri, client_state, code_challenge, resource,
			browser_binding_hash, upstream_state_hash, upstream_verifier, upstream_nonce, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.ClientID, p.RedirectURI, p.ClientState, p.CodeChallenge, p.Resource,
		p.BrowserBindingHash, p.UpstreamStateHash, p.UpstreamVerifier, p.UpstreamNonce,
		millis(p.CreatedAt), millis(p.ExpiresAt))
	if err != nil {
		return fmt.Errorf("creating pending auth: %w", err)
	}
	return nil
}

func (s *Store) GetPendingAuth(ctx context.Context, id string) (PendingAuth, error) {
	p, err := scanPendingAuth(s.db.QueryRowContext(ctx,
		`SELECT `+pendingAuthColumns+` FROM pending_auths WHERE id = ?`, id))
	if err != nil {
		return PendingAuth{}, lookupError("getting pending auth", err)
	}
	return p, nil
}

func (s *Store) TakePendingAuthByUpstreamState(ctx context.Context, stateHash []byte, now time.Time) (PendingAuth, error) {
	p, err := scanPendingAuth(s.db.QueryRowContext(ctx,
		`UPDATE pending_auths SET upstream_state_hash = randomblob(32)
		WHERE upstream_state_hash = ? AND expires_at > ?
		RETURNING `+pendingAuthColumns, stateHash, millis(now)))
	if err != nil {
		return PendingAuth{}, lookupError("taking pending auth", err)
	}
	p.UpstreamStateHash = nil
	return p, nil
}

func (s *Store) SetPendingAuthLogin(ctx context.Context, id string, l Login) error {
	return s.execOne(ctx, "setting pending auth login", `
		UPDATE pending_auths
		SET mealie_user_id = ?, mealie_username = ?, mealie_token_ct = ?,
			mealie_token_expires_at = ?, mealie_token_lifetime_s = ?
		WHERE id = ?`,
		l.MealieUserID, l.MealieUsername, l.MealieTokenCT,
		millis(l.MealieTokenExpiresAt), seconds(l.MealieTokenLifetime), id)
}

func (s *Store) DeletePendingAuth(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM pending_auths WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting pending auth: %w", err)
	}
	return nil
}
