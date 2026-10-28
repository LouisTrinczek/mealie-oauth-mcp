package store

import (
	"context"
	"fmt"
	"time"
)

func (s *Store) HasConsent(ctx context.Context, userID, clientID string) (bool, error) {
	var granted bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM consents WHERE mealie_user_id = ? AND client_id = ?)`,
		userID, clientID).Scan(&granted)
	if err != nil {
		return false, fmt.Errorf("checking consent: %w", err)
	}
	return granted, nil
}

func (s *Store) GrantConsent(ctx context.Context, userID, clientID string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO consents (mealie_user_id, client_id, granted_at) VALUES (?, ?, ?)
		ON CONFLICT (mealie_user_id, client_id) DO NOTHING`,
		userID, clientID, millis(now))
	if err != nil {
		return fmt.Errorf("granting consent: %w", err)
	}
	return nil
}
