package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (s *Store) CreateSessionWithCode(ctx context.Context, session Session, code AuthCode) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO sessions (id, client_id, mealie_user_id, mealie_username, mealie_token_ct,
				mealie_token_expires_at, mealie_token_lifetime_s, resource, created_at, expires_at, last_used_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			session.ID, session.ClientID, session.MealieUserID, session.MealieUsername, session.MealieTokenCT,
			unixSeconds(session.MealieTokenExpiresAt), seconds(session.MealieTokenLifetime), session.Resource,
			unixSeconds(session.CreatedAt), unixSeconds(session.ExpiresAt), unixSeconds(session.LastUsedAt))
		if err != nil {
			return fmt.Errorf("creating session: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO auth_codes (hash, session_id, client_id, redirect_uri, code_challenge, resource, expires_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			code.Hash, code.SessionID, code.ClientID, code.RedirectURI, code.CodeChallenge, code.Resource,
			unixSeconds(code.ExpiresAt))
		if err != nil {
			return fmt.Errorf("creating auth code: %w", err)
		}
		return nil
	})
}

func (s *Store) GetSession(ctx context.Context, id string) (Session, error) {
	var (
		session                                                          Session
		tokenExpiresAt, tokenLifetimeS, createdAt, expiresAt, lastUsedAt int64
		revokedAt                                                        sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, client_id, mealie_user_id, mealie_username, mealie_token_ct,
			mealie_token_expires_at, mealie_token_lifetime_s, resource, created_at, expires_at, last_used_at, revoked_at
		FROM sessions WHERE id = ?`, id).
		Scan(&session.ID, &session.ClientID, &session.MealieUserID, &session.MealieUsername, &session.MealieTokenCT,
			&tokenExpiresAt, &tokenLifetimeS, &session.Resource, &createdAt, &expiresAt, &lastUsedAt, &revokedAt)
	if err != nil {
		return Session{}, lookupError("getting session", err)
	}
	session.MealieTokenExpiresAt = timeFromSeconds(tokenExpiresAt)
	session.MealieTokenLifetime = time.Duration(tokenLifetimeS) * time.Second
	session.CreatedAt = timeFromSeconds(createdAt)
	session.ExpiresAt = timeFromSeconds(expiresAt)
	session.LastUsedAt = timeFromSeconds(lastUsedAt)
	session.RevokedAt = timeFromNullable(revokedAt)
	return session, nil
}

func (s *Store) UpdateSessionMealieToken(ctx context.Context, id string, ct []byte, expiresAt time.Time, lifetime time.Duration) error {
	return s.execOne(ctx, "updating session token",
		`UPDATE sessions SET mealie_token_ct = ?, mealie_token_expires_at = ?, mealie_token_lifetime_s = ? WHERE id = ?`,
		ct, unixSeconds(expiresAt), seconds(lifetime), id)
}

func (s *Store) TouchSession(ctx context.Context, id string, now time.Time) error {
	return s.execOne(ctx, "touching session", `UPDATE sessions SET last_used_at = ? WHERE id = ?`, unixSeconds(now), id)
}

func (s *Store) RevokeSession(ctx context.Context, id string, now time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`UPDATE sessions SET revoked_at = COALESCE(revoked_at, ?) WHERE id = ?`, unixSeconds(now), id)
		if err != nil {
			return fmt.Errorf("revoking session: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("revoking session: %w", err)
		}
		if affected == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM access_tokens WHERE session_id = ?`, id); err != nil {
			return fmt.Errorf("deleting session access tokens: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM refresh_tokens WHERE session_id = ?`, id); err != nil {
			return fmt.Errorf("deleting session refresh tokens: %w", err)
		}
		return nil
	})
}
