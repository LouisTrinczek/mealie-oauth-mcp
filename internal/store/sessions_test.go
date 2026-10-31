package store

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCreateSessionWithCode_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	want := testSession("s1")
	seedSession(t, s, want, "code-1")

	got, err := s.GetSession(t.Context(), "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session = %+v, want %+v", got, want)
	}
}

func TestCreateSessionWithCode_RollsBackSessionOnCodeFailure(t *testing.T) {
	s := newTestStore(t)
	seedSession(t, s, testSession("s1"), "code-1")

	err := s.CreateSessionWithCode(t.Context(), testSession("s2"), testAuthCode("code-1", "s2", baseTime.Add(time.Minute)))
	if err == nil {
		t.Fatal("want error for duplicate code hash")
	}
	if _, err := s.GetSession(t.Context(), "s2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSession err = %v, want ErrNotFound after rollback", err)
	}
}

func TestSessionMutations(t *testing.T) {
	s := newTestStore(t)
	seedSession(t, s, testSession("s1"), "code-1")

	newExpiry := baseTime.Add(96 * time.Hour)
	if err := s.UpdateSessionMealieToken(t.Context(), "s1", []byte("new-ct"), newExpiry, 96*time.Hour); err != nil {
		t.Fatalf("UpdateSessionMealieToken: %v", err)
	}
	touched := baseTime.Add(time.Hour)
	if err := s.TouchSession(t.Context(), "s1", touched); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}

	got, err := s.GetSession(t.Context(), "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	want := testSession("s1")
	want.MealieTokenCT = []byte("new-ct")
	want.MealieTokenExpiresAt = newExpiry
	want.MealieTokenLifetime = 96 * time.Hour
	want.LastUsedAt = touched
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session = %+v, want %+v", got, want)
	}
}

func TestSessionOperations_NotFound(t *testing.T) {
	s := newTestStore(t)
	tests := []struct {
		name string
		call func() error
	}{
		{"GetSession", func() error { _, err := s.GetSession(t.Context(), "x"); return err }},
		{"UpdateSessionMealieToken", func() error {
			return s.UpdateSessionMealieToken(t.Context(), "x", nil, baseTime, time.Hour)
		}},
		{"TouchSession", func() error { return s.TouchSession(t.Context(), "x", baseTime) }},
		{"RevokeSession", func() error { return s.RevokeSession(t.Context(), "x", baseTime) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestRevokeSession_DeletesTokens(t *testing.T) {
	s := newTestStore(t)
	seedSession(t, s, testSession("s1"), "code-1")
	seedSession(t, s, testSession("s2"), "code-2")
	for _, id := range []string{"s1", "s2"} {
		err := s.IssueTokens(t.Context(),
			AccessToken{Hash: []byte("at-" + id), SessionID: id, ExpiresAt: baseTime.Add(time.Hour)},
			RefreshToken{Hash: []byte("rt-" + id), SessionID: id, ExpiresAt: baseTime.Add(24 * time.Hour)})
		if err != nil {
			t.Fatalf("IssueTokens: %v", err)
		}
	}

	revokedAt := baseTime.Add(time.Minute)
	if err := s.RevokeSession(t.Context(), "s1", revokedAt); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if err := s.RevokeSession(t.Context(), "s1", revokedAt.Add(time.Hour)); err != nil {
		t.Fatalf("repeated RevokeSession: %v", err)
	}

	session, err := s.GetSession(t.Context(), "s1")
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if !session.RevokedAt.Equal(revokedAt) {
		t.Fatalf("RevokedAt = %v, want first revocation %v", session.RevokedAt, revokedAt)
	}
	if _, err := s.GetAccessToken(t.Context(), []byte("at-s1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked access token err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetRefreshToken(t.Context(), []byte("rt-s1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked refresh token err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetAccessToken(t.Context(), []byte("at-s2")); err != nil {
		t.Fatalf("other session access token: %v", err)
	}
	if _, err := s.GetRefreshToken(t.Context(), []byte("rt-s2")); err != nil {
		t.Fatalf("other session refresh token: %v", err)
	}
}

func TestCreateSessionWithCode_DuplicateSessionRejected(t *testing.T) {
	s := newTestStore(t)
	seedSession(t, s, testSession("s1"), "code-1")

	err := s.CreateSessionWithCode(t.Context(), testSession("s1"), testAuthCode("code-2", "s1", baseTime.Add(time.Minute)))
	if err == nil || !strings.Contains(err.Error(), "creating session") {
		t.Fatalf("err = %v, want a creating session error", err)
	}
}

func TestRevokeSession_WriteFailures(t *testing.T) {
	tests := []struct {
		event string
		want  string
	}{
		{"UPDATE sessions", "revoking session"},
		{"DELETE access_tokens", "deleting session access tokens"},
		{"DELETE refresh_tokens", "deleting session refresh tokens"},
	}
	for _, tt := range tests {
		t.Run(tt.event, func(t *testing.T) {
			s := newTestStore(t)
			seedSession(t, s, testSession("s1"), "code-1")
			err := s.IssueTokens(t.Context(),
				AccessToken{Hash: []byte("at-1"), SessionID: "s1", ExpiresAt: baseTime.Add(time.Hour)},
				RefreshToken{Hash: []byte("rt-1"), SessionID: "s1", ExpiresAt: baseTime.Add(24 * time.Hour)})
			if err != nil {
				t.Fatalf("IssueTokens: %v", err)
			}
			failWrites(t, s, tt.event)

			err = s.RevokeSession(t.Context(), "s1", baseTime)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			session, getErr := s.GetSession(t.Context(), "s1")
			if getErr != nil || !session.RevokedAt.IsZero() {
				t.Fatalf("session = %+v, %v; want the revocation rolled back", session, getErr)
			}
		})
	}
}

func TestCountActiveSessions(t *testing.T) {
	s := newTestStore(t)
	now := baseTime.Add(time.Hour)
	seedSession(t, s, testSession("active"), "code-1")
	expired := testSession("expired")
	expired.ExpiresAt = now
	seedSession(t, s, expired, "code-2")
	seedSession(t, s, testSession("revoked"), "code-3")
	idle := testSession("idle")
	idle.MealieTokenExpiresAt = now
	seedSession(t, s, idle, "code-4")
	if err := s.RevokeSession(t.Context(), "revoked", baseTime); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	count, err := s.CountActiveSessions(t.Context(), now)
	if err != nil {
		t.Fatalf("CountActiveSessions: %v", err)
	}
	if count != 1 {
		t.Fatalf("CountActiveSessions = %d, want 1", count)
	}
}
