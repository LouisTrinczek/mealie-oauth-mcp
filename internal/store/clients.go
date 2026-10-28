package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

func (s *Store) UpsertClient(ctx context.Context, c Client) error {
	redirectURIs, err := json.Marshal(c.RedirectURIs)
	if err != nil {
		return fmt.Errorf("encoding redirect URIs: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO clients (id, source, name, redirect_uris, created_at, metadata_fetched_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			source = excluded.source,
			name = excluded.name,
			redirect_uris = excluded.redirect_uris,
			metadata_fetched_at = excluded.metadata_fetched_at`,
		c.ID, c.Source, c.Name, string(redirectURIs), unixSeconds(c.CreatedAt), nullableSeconds(c.MetadataFetchedAt))
	if err != nil {
		return fmt.Errorf("upserting client: %w", err)
	}
	return nil
}

func (s *Store) GetClient(ctx context.Context, id string) (Client, error) {
	var (
		c            Client
		redirectURIs string
		createdAt    int64
		fetchedAt    sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, source, name, redirect_uris, created_at, metadata_fetched_at
		FROM clients WHERE id = ?`, id).
		Scan(&c.ID, &c.Source, &c.Name, &redirectURIs, &createdAt, &fetchedAt)
	if err != nil {
		return Client{}, lookupError("getting client", err)
	}
	if err := json.Unmarshal([]byte(redirectURIs), &c.RedirectURIs); err != nil {
		return Client{}, fmt.Errorf("decoding redirect URIs: %w", err)
	}
	c.CreatedAt = timeFromSeconds(createdAt)
	c.MetadataFetchedAt = timeFromNullable(fetchedAt)
	return c, nil
}
