package store

import (
	"reflect"
	"testing"
	"time"
)

func seedExpiryBoundaries(t *testing.T, s *Store, now time.Time) {
	t.Helper()
	after := now.Add(time.Second)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := s.db.ExecContext(t.Context(), query, args...); err != nil {
			t.Fatalf("seed %q: %v", query, err)
		}
	}
	insertSession := func(id, clientID string, expiresAt time.Time, revokedAt any) {
		exec(`INSERT INTO sessions (id, client_id, mealie_user_id, mealie_username, mealie_token_ct,
			mealie_token_expires_at, mealie_token_lifetime_s, resource, created_at, expires_at, last_used_at, revoked_at)
			VALUES (?, ?, 'u', 'n', x'00', 0, 0, 'r', 0, ?, 0, ?)`, id, clientID, unixSeconds(expiresAt), revokedAt)
	}

	exec(`INSERT INTO pending_auths (id, client_id, redirect_uri, client_state, code_challenge, resource,
		browser_binding_hash, upstream_state_hash, upstream_verifier, upstream_nonce, created_at, expires_at)
		VALUES ('pa-expired', 'c', 'r', 's', 'c', 'r', x'00', x'01', 'v', 'n', 0, ?),
			('pa-live', 'c', 'r', 's', 'c', 'r', x'00', x'02', 'v', 'n', 0, ?)`, unixSeconds(now), unixSeconds(after))

	insertSession("s-live", "c", after, nil)
	insertSession("s-expired", "c", now, nil)
	insertSession("s-revoked-old", "c", after, unixSeconds(now.Add(-revokedSessionRetention-time.Second)))
	insertSession("s-revoked-boundary", "c", after, unixSeconds(now.Add(-revokedSessionRetention)))
	insertSession("s-revoked-recent", "c", after, unixSeconds(now.Add(-time.Second)))

	exec(`INSERT INTO auth_codes (hash, session_id, client_id, redirect_uri, code_challenge, resource, expires_at)
		VALUES (x'01', 's-live', 'c', 'r', 'c', 'r', ?), (x'02', 's-live', 'c', 'r', 'c', 'r', ?)`, unixSeconds(now), unixSeconds(after))
	exec(`INSERT INTO access_tokens (hash, session_id, expires_at)
		VALUES (x'01', 's-live', ?), (x'02', 's-live', ?)`, unixSeconds(now), unixSeconds(after))
	exec(`INSERT INTO refresh_tokens (hash, session_id, expires_at)
		VALUES (x'01', 's-live', ?), (x'02', 's-live', ?)`, unixSeconds(now), unixSeconds(after))

	staleFetch := unixSeconds(now.Add(-clientMetadataTTL - time.Second))
	exec(`INSERT INTO clients (id, source, name, redirect_uris, created_at, metadata_fetched_at) VALUES
		('cimd-stale', 'cimd', 'n', '[]', 0, ?),
		('cimd-boundary', 'cimd', 'n', '[]', 0, ?),
		('cimd-fresh', 'cimd', 'n', '[]', 0, ?),
		('cimd-stale-with-session', 'cimd', 'n', '[]', 0, ?),
		('dcr-old', 'dcr', 'n', '[]', 0, NULL)`,
		staleFetch, unixSeconds(now.Add(-clientMetadataTTL)), unixSeconds(now.Add(-time.Second)), staleFetch)
	insertSession("s-for-cimd", "cimd-stale-with-session", after, nil)
}

func survivingKeys(t *testing.T, s *Store, query string) []string {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), query)
	if err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	defer func() { _ = rows.Close() }()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			t.Fatalf("scan: %v", err)
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return keys
}

func TestDeleteExpired(t *testing.T) {
	s := newTestStore(t)
	now := baseTime.Add(1000 * time.Hour)
	seedExpiryBoundaries(t, s, now)

	deleted, err := s.DeleteExpired(t.Context(), now)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	const wantDeleted = 1 + 1 + 1 + 1 + 2 + 1
	if deleted != wantDeleted {
		t.Fatalf("deleted = %d, want %d", deleted, wantDeleted)
	}

	survivors := []struct {
		name  string
		query string
		want  []string
	}{
		{"pending_auths", `SELECT id FROM pending_auths ORDER BY 1`, []string{"pa-live"}},
		{"sessions", `SELECT id FROM sessions ORDER BY 1`, []string{"s-for-cimd", "s-live", "s-revoked-boundary", "s-revoked-recent"}},
		{"auth_codes", `SELECT hex(hash) FROM auth_codes ORDER BY 1`, []string{"02"}},
		{"access_tokens", `SELECT hex(hash) FROM access_tokens ORDER BY 1`, []string{"02"}},
		{"refresh_tokens", `SELECT hex(hash) FROM refresh_tokens ORDER BY 1`, []string{"02"}},
		{"clients", `SELECT id FROM clients ORDER BY 1`, []string{"cimd-boundary", "cimd-fresh", "cimd-stale-with-session", "dcr-old"}},
	}
	for _, tt := range survivors {
		t.Run(tt.name, func(t *testing.T) {
			if got := survivingKeys(t, s, tt.query); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("survivors = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeleteExpired_SessionRemovalCascades(t *testing.T) {
	s := newTestStore(t)
	session := testSession("s1")
	session.ExpiresAt = baseTime.Add(time.Hour)
	seedSession(t, s, session, "code-1")

	deleted, err := s.DeleteExpired(t.Context(), baseTime.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2 (expired code and session)", deleted)
	}
}

func TestDeleteExpired_NothingToDelete(t *testing.T) {
	s := newTestStore(t)
	deleted, err := s.DeleteExpired(t.Context(), baseTime)
	if err != nil || deleted != 0 {
		t.Fatalf("DeleteExpired = (%d, %v), want (0, nil)", deleted, err)
	}
}

func TestRepositories_ClosedStoreErrors(t *testing.T) {
	s := openTemp(t, t.TempDir())
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	tests := []struct {
		name string
		call func() error
	}{
		{"UpsertClient", func() error { return s.UpsertClient(t.Context(), Client{ID: "c"}) }},
		{"GetClient", func() error { _, err := s.GetClient(t.Context(), "c"); return err }},
		{"CreatePendingAuth", func() error { return s.CreatePendingAuth(t.Context(), PendingAuth{ID: "p"}) }},
		{"GetPendingAuth", func() error { _, err := s.GetPendingAuth(t.Context(), "p"); return err }},
		{"TakePendingAuthByUpstreamState", func() error {
			_, err := s.TakePendingAuthByUpstreamState(t.Context(), nil, baseTime)
			return err
		}},
		{"SetPendingAuthLogin", func() error { return s.SetPendingAuthLogin(t.Context(), "p", Login{}) }},
		{"DeletePendingAuth", func() error { return s.DeletePendingAuth(t.Context(), "p") }},
		{"CreateSessionWithCode", func() error { return s.CreateSessionWithCode(t.Context(), Session{}, AuthCode{}) }},
		{"GetSession", func() error { _, err := s.GetSession(t.Context(), "s"); return err }},
		{"UpdateSessionMealieToken", func() error {
			return s.UpdateSessionMealieToken(t.Context(), "s", nil, baseTime, time.Hour)
		}},
		{"TouchSession", func() error { return s.TouchSession(t.Context(), "s", baseTime) }},
		{"RevokeSession", func() error { return s.RevokeSession(t.Context(), "s", baseTime) }},
		{"ConsumeAuthCode", func() error { _, err := s.ConsumeAuthCode(t.Context(), nil, baseTime); return err }},
		{"IssueTokens", func() error { return s.IssueTokens(t.Context(), AccessToken{}, RefreshToken{}) }},
		{"GetAccessToken", func() error { _, err := s.GetAccessToken(t.Context(), nil); return err }},
		{"GetRefreshToken", func() error { _, err := s.GetRefreshToken(t.Context(), nil); return err }},
		{"RotateRefreshToken", func() error {
			_, err := s.RotateRefreshToken(t.Context(), nil, baseTime, AccessToken{}, RefreshToken{})
			return err
		}},
		{"HasConsent", func() error { _, err := s.HasConsent(t.Context(), "u", "c"); return err }},
		{"GrantConsent", func() error { return s.GrantConsent(t.Context(), "u", "c", baseTime) }},
		{"DeleteExpired", func() error { _, err := s.DeleteExpired(t.Context(), baseTime); return err }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); err == nil {
				t.Fatal("want error on closed store")
			}
		})
	}
}
