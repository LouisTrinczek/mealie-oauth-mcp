package store

import (
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestConsumeAuthCode_SingleUse(t *testing.T) {
	s := newTestStore(t)
	seedSession(t, s, testSession("s1"), "code-1")
	expiresAt := baseTime.Add(time.Minute)

	first, err := s.ConsumeAuthCode(t.Context(), []byte("code-1"), baseTime)
	if err != nil {
		t.Fatalf("first ConsumeAuthCode: %v", err)
	}
	want := testAuthCode("code-1", "s1", expiresAt)
	want.UsedAt = baseTime
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("code = %+v, want %+v", first, want)
	}

	second, err := s.ConsumeAuthCode(t.Context(), []byte("code-1"), baseTime.Add(time.Second))
	if !errors.Is(err, ErrAlreadyUsed) {
		t.Fatalf("second err = %v, want ErrAlreadyUsed", err)
	}
	if second.SessionID != "s1" {
		t.Fatalf("reused code SessionID = %q, want s1", second.SessionID)
	}

	reusedAfterExpiry, err := s.ConsumeAuthCode(t.Context(), []byte("code-1"), expiresAt.Add(time.Hour))
	if !errors.Is(err, ErrAlreadyUsed) || reusedAfterExpiry.SessionID != "s1" {
		t.Fatalf("reuse after expiry = (%q, %v), want (s1, ErrAlreadyUsed)", reusedAfterExpiry.SessionID, err)
	}
}

func TestConsumeAuthCode_NotFound(t *testing.T) {
	s := newTestStore(t)
	seedSession(t, s, testSession("s1"), "code-1")
	expiresAt := baseTime.Add(time.Minute)

	tests := []struct {
		name string
		hash string
		now  time.Time
	}{
		{"unknown", "other", baseTime},
		{"expired at boundary", "code-1", expiresAt},
		{"expired", "code-1", expiresAt.Add(time.Second)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.ConsumeAuthCode(t.Context(), []byte(tt.hash), tt.now); !errors.Is(err, ErrNotFound) {
				t.Fatalf("err = %v, want ErrNotFound", err)
			}
		})
	}
}

func TestConsumeAuthCode_Concurrent(t *testing.T) {
	s := newTestStore(t)
	seedSession(t, s, testSession("s1"), "code-1")

	const attempts = 20
	var succeeded, reused atomic.Int32
	var wg sync.WaitGroup
	for range attempts {
		wg.Go(func() {
			_, err := s.ConsumeAuthCode(t.Context(), []byte("code-1"), baseTime)
			switch {
			case err == nil:
				succeeded.Add(1)
			case errors.Is(err, ErrAlreadyUsed):
				reused.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	wg.Wait()

	if succeeded.Load() != 1 || reused.Load() != attempts-1 {
		t.Fatalf("succeeded = %d, reused = %d, want 1 and %d", succeeded.Load(), reused.Load(), attempts-1)
	}
}

func TestIssueTokens_RoundTripAndRollback(t *testing.T) {
	s := newTestStore(t)
	seedSession(t, s, testSession("s1"), "code-1")
	access := AccessToken{Hash: []byte("at"), SessionID: "s1", ExpiresAt: baseTime.Add(time.Hour)}
	refresh := RefreshToken{Hash: []byte("rt"), SessionID: "s1", ExpiresAt: baseTime.Add(24 * time.Hour)}
	if err := s.IssueTokens(t.Context(), access, refresh); err != nil {
		t.Fatalf("IssueTokens: %v", err)
	}

	gotAccess, err := s.GetAccessToken(t.Context(), []byte("at"))
	if err != nil || !reflect.DeepEqual(gotAccess, access) {
		t.Fatalf("access = %+v, %v; want %+v", gotAccess, err, access)
	}
	gotRefresh, err := s.GetRefreshToken(t.Context(), []byte("rt"))
	if err != nil || !reflect.DeepEqual(gotRefresh, refresh) {
		t.Fatalf("refresh = %+v, %v; want %+v", gotRefresh, err, refresh)
	}

	duplicateRefresh := RefreshToken{Hash: []byte("rt"), SessionID: "s1", ExpiresAt: baseTime}
	err = s.IssueTokens(t.Context(), AccessToken{Hash: []byte("at-2"), SessionID: "s1", ExpiresAt: baseTime}, duplicateRefresh)
	if err == nil {
		t.Fatal("want error for duplicate refresh hash")
	}
	if _, err := s.GetAccessToken(t.Context(), []byte("at-2")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("access token of failed issue err = %v, want ErrNotFound", err)
	}
}

func TestIssueTokens_UnknownSessionRejected(t *testing.T) {
	s := newTestStore(t)
	err := s.IssueTokens(t.Context(),
		AccessToken{Hash: []byte("at"), SessionID: "ghost", ExpiresAt: baseTime},
		RefreshToken{Hash: []byte("rt"), SessionID: "ghost", ExpiresAt: baseTime})
	if err == nil {
		t.Fatal("want error for unknown session")
	}
}

func TestTokenLookups_NotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetAccessToken(t.Context(), []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetAccessToken err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetRefreshToken(t.Context(), []byte("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRefreshToken err = %v, want ErrNotFound", err)
	}
}

func issueRotatableToken(t *testing.T, s *Store, refreshExpiry time.Time) {
	t.Helper()
	seedSession(t, s, testSession("s1"), "code-1")
	err := s.IssueTokens(t.Context(),
		AccessToken{Hash: []byte("at-1"), SessionID: "s1", ExpiresAt: baseTime.Add(time.Hour)},
		RefreshToken{Hash: []byte("rt-1"), SessionID: "s1", ExpiresAt: refreshExpiry})
	if err != nil {
		t.Fatalf("IssueTokens: %v", err)
	}
}

func nextTokens() (AccessToken, RefreshToken) {
	return AccessToken{Hash: []byte("at-2"), SessionID: "s1", ExpiresAt: baseTime.Add(2 * time.Hour)},
		RefreshToken{Hash: []byte("rt-2"), SessionID: "s1", ExpiresAt: baseTime.Add(48 * time.Hour)}
}

func TestRotateRefreshToken_Success(t *testing.T) {
	s := newTestStore(t)
	issueRotatableToken(t, s, baseTime.Add(24*time.Hour))
	access, refresh := nextTokens()

	sessionID, err := s.RotateRefreshToken(t.Context(), []byte("rt-1"), baseTime.Add(time.Minute), access, refresh)
	if err != nil || sessionID != "s1" {
		t.Fatalf("RotateRefreshToken = (%q, %v), want (s1, nil)", sessionID, err)
	}

	old, err := s.GetRefreshToken(t.Context(), []byte("rt-1"))
	if err != nil || !old.UsedAt.Equal(baseTime.Add(time.Minute)) {
		t.Fatalf("old token = %+v, %v; want used at rotation time", old, err)
	}
	if _, err := s.GetAccessToken(t.Context(), []byte("at-2")); err != nil {
		t.Fatalf("new access token: %v", err)
	}
	if _, err := s.GetRefreshToken(t.Context(), []byte("rt-2")); err != nil {
		t.Fatalf("new refresh token: %v", err)
	}
}

func TestRotateRefreshToken_ReuseDetected(t *testing.T) {
	s := newTestStore(t)
	issueRotatableToken(t, s, baseTime.Add(24*time.Hour))
	access, refresh := nextTokens()
	if _, err := s.RotateRefreshToken(t.Context(), []byte("rt-1"), baseTime, access, refresh); err != nil {
		t.Fatalf("first rotation: %v", err)
	}

	again := AccessToken{Hash: []byte("at-3"), SessionID: "s1", ExpiresAt: baseTime.Add(time.Hour)}
	againRefresh := RefreshToken{Hash: []byte("rt-3"), SessionID: "s1", ExpiresAt: baseTime.Add(time.Hour)}
	sessionID, err := s.RotateRefreshToken(t.Context(), []byte("rt-1"), baseTime.Add(time.Second), again, againRefresh)
	if !errors.Is(err, ErrAlreadyUsed) {
		t.Fatalf("err = %v, want ErrAlreadyUsed", err)
	}
	if sessionID != "s1" {
		t.Fatalf("sessionID = %q, want s1", sessionID)
	}
	if _, err := s.GetAccessToken(t.Context(), []byte("at-3")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("tokens of rejected rotation must not be stored, err = %v", err)
	}
}

func TestRotateRefreshToken_NotFound(t *testing.T) {
	s := newTestStore(t)
	expiry := baseTime.Add(24 * time.Hour)
	issueRotatableToken(t, s, expiry)
	access, refresh := nextTokens()

	tests := []struct {
		name string
		hash string
		now  time.Time
	}{
		{"unknown", "rt-x", baseTime},
		{"expired at boundary", "rt-1", expiry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessionID, err := s.RotateRefreshToken(t.Context(), []byte(tt.hash), tt.now, access, refresh)
			if !errors.Is(err, ErrNotFound) || sessionID != "" {
				t.Fatalf("RotateRefreshToken = (%q, %v), want (\"\", ErrNotFound)", sessionID, err)
			}
		})
	}
}
