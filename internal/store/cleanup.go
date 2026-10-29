package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const revokedSessionRetention = 24 * time.Hour

const clientMetadataTTL = 24 * time.Hour

const unusedClientRetention = 24 * time.Hour

func (s *Store) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	nowSeconds := unixSeconds(now)
	statements := []struct {
		what  string
		query string
		args  []any
	}{
		{"pending auths", `DELETE FROM pending_auths WHERE expires_at <= ?`, []any{nowSeconds}},
		{"auth codes", `DELETE FROM auth_codes WHERE expires_at <= ?`, []any{nowSeconds}},
		{"access tokens", `DELETE FROM access_tokens WHERE expires_at <= ?`, []any{nowSeconds}},
		{"refresh tokens", `DELETE FROM refresh_tokens WHERE expires_at <= ?`, []any{nowSeconds}},
		{"sessions", `DELETE FROM sessions WHERE expires_at <= ? OR revoked_at < ?`,
			[]any{nowSeconds, unixSeconds(now.Add(-revokedSessionRetention))}},
		{"clients", `DELETE FROM clients
			WHERE source = 'cimd' AND metadata_fetched_at < ?
			AND NOT EXISTS (SELECT 1 FROM sessions WHERE sessions.client_id = clients.id)`,
			[]any{unixSeconds(now.Add(-clientMetadataTTL))}},
		{"unused registered clients", `DELETE FROM clients
			WHERE source = 'dcr' AND created_at < ?
			AND NOT EXISTS (SELECT 1 FROM sessions WHERE sessions.client_id = clients.id)
			AND NOT EXISTS (SELECT 1 FROM consents WHERE consents.client_id = clients.id)`,
			[]any{unixSeconds(now.Add(-unusedClientRetention))}},
	}
	var deleted int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		deleted = 0
		for _, statement := range statements {
			result, err := tx.ExecContext(ctx, statement.query, statement.args...)
			if err != nil {
				return fmt.Errorf("deleting expired %s: %w", statement.what, err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("deleting expired %s: %w", statement.what, err)
			}
			deleted += affected
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}
