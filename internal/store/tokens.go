package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const authCodeColumns = `hash, session_id, client_id, redirect_uri, code_challenge, resource, expires_at, used_at`

func scanAuthCode(row rowScanner) (AuthCode, error) {
	var (
		c         AuthCode
		expiresAt int64
		usedAt    sql.NullInt64
	)
	err := row.Scan(&c.Hash, &c.SessionID, &c.ClientID, &c.RedirectURI, &c.CodeChallenge, &c.Resource, &expiresAt, &usedAt)
	if err != nil {
		return AuthCode{}, err
	}
	c.ExpiresAt = timeFromSeconds(expiresAt)
	c.UsedAt = timeFromNullable(usedAt)
	return c, nil
}

func (s *Store) ConsumeAuthCode(ctx context.Context, hash []byte, now time.Time) (AuthCode, error) {
	code, err := scanAuthCode(s.db.QueryRowContext(ctx,
		`UPDATE auth_codes SET used_at = ? WHERE hash = ? AND used_at IS NULL AND expires_at > ?
		RETURNING `+authCodeColumns, unixSeconds(now), hash, unixSeconds(now)))
	if err == nil {
		return code, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return AuthCode{}, fmt.Errorf("consuming auth code: %w", err)
	}
	existing, err := scanAuthCode(s.db.QueryRowContext(ctx,
		`SELECT `+authCodeColumns+` FROM auth_codes WHERE hash = ? AND used_at IS NOT NULL`, hash))
	if err != nil {
		return AuthCode{}, lookupError("reading used auth code", err)
	}
	return existing, ErrAlreadyUsed
}

func insertTokens(ctx context.Context, tx *sql.Tx, access AccessToken, refresh RefreshToken) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO access_tokens (hash, session_id, expires_at) VALUES (?, ?, ?)`,
		access.Hash, access.SessionID, unixSeconds(access.ExpiresAt)); err != nil {
		return fmt.Errorf("inserting access token: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO refresh_tokens (hash, session_id, expires_at) VALUES (?, ?, ?)`,
		refresh.Hash, refresh.SessionID, unixSeconds(refresh.ExpiresAt)); err != nil {
		return fmt.Errorf("inserting refresh token: %w", err)
	}
	return nil
}

func (s *Store) IssueTokens(ctx context.Context, access AccessToken, refresh RefreshToken) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		return insertTokens(ctx, tx, access, refresh)
	})
}

func (s *Store) GetAccessToken(ctx context.Context, hash []byte) (AccessToken, error) {
	var (
		token     AccessToken
		expiresAt int64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT hash, session_id, expires_at FROM access_tokens WHERE hash = ?`, hash).
		Scan(&token.Hash, &token.SessionID, &expiresAt)
	if err != nil {
		return AccessToken{}, lookupError("getting access token", err)
	}
	token.ExpiresAt = timeFromSeconds(expiresAt)
	return token, nil
}

func (s *Store) GetRefreshToken(ctx context.Context, hash []byte) (RefreshToken, error) {
	var (
		token     RefreshToken
		expiresAt int64
		usedAt    sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT hash, session_id, expires_at, used_at FROM refresh_tokens WHERE hash = ?`, hash).
		Scan(&token.Hash, &token.SessionID, &expiresAt, &usedAt)
	if err != nil {
		return RefreshToken{}, lookupError("getting refresh token", err)
	}
	token.ExpiresAt = timeFromSeconds(expiresAt)
	token.UsedAt = timeFromNullable(usedAt)
	return token, nil
}

func (s *Store) RotateRefreshToken(ctx context.Context, oldHash []byte, now time.Time, next AccessToken, nextRefresh RefreshToken) (string, error) {
	var sessionID string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx,
			`UPDATE refresh_tokens SET used_at = ? WHERE hash = ? AND used_at IS NULL AND expires_at > ?
			RETURNING session_id`, unixSeconds(now), oldHash, unixSeconds(now)).Scan(&sessionID)
		if errors.Is(err, sql.ErrNoRows) {
			return reportUnrotatable(ctx, tx, oldHash, &sessionID)
		}
		if err != nil {
			return fmt.Errorf("rotating refresh token: %w", err)
		}
		return insertTokens(ctx, tx, next, nextRefresh)
	})
	return sessionID, err
}

func reportUnrotatable(ctx context.Context, tx *sql.Tx, hash []byte, sessionID *string) error {
	err := tx.QueryRowContext(ctx,
		`SELECT session_id FROM refresh_tokens WHERE hash = ? AND used_at IS NOT NULL`, hash).Scan(sessionID)
	if err != nil {
		return lookupError("reading used refresh token", err)
	}
	return ErrAlreadyUsed
}
