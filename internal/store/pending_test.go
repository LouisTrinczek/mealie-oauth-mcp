package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestPendingAuth_RoundTripWithLogin(t *testing.T) {
	s := newTestStore(t)
	want := testPendingAuth("p1", "state-1", baseTime.Add(10*time.Minute))
	if err := s.CreatePendingAuth(t.Context(), want); err != nil {
		t.Fatalf("CreatePendingAuth: %v", err)
	}
	got, err := s.GetPendingAuthByUpstreamState(t.Context(), want.UpstreamStateHash)
	if err != nil {
		t.Fatalf("GetPendingAuthByUpstreamState: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending = %+v, want %+v", got, want)
	}

	login := Login{
		MealieUserID:         "user-1",
		MealieUsername:       "alice",
		MealieTokenCT:        []byte("ct"),
		MealieTokenExpiresAt: baseTime.Add(48 * time.Hour),
		MealieTokenLifetime:  48 * time.Hour,
	}
	if err := s.SetPendingAuthLogin(t.Context(), "p1", login); err != nil {
		t.Fatalf("SetPendingAuthLogin: %v", err)
	}
	got, err = s.GetPendingAuth(t.Context(), "p1")
	if err != nil {
		t.Fatalf("GetPendingAuth: %v", err)
	}
	want.MealieUserID = login.MealieUserID
	want.MealieUsername = login.MealieUsername
	want.MealieTokenCT = login.MealieTokenCT
	want.MealieTokenExpiresAt = login.MealieTokenExpiresAt
	want.MealieTokenLifetime = login.MealieTokenLifetime
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending = %+v, want %+v", got, want)
	}
}

func TestClaimPendingAuth_SingleUseAndExpiry(t *testing.T) {
	s := newTestStore(t)
	expiresAt := baseTime.Add(10 * time.Minute)
	for _, p := range []PendingAuth{
		testPendingAuth("live", "state-live", expiresAt),
		testPendingAuth("gone", "state-gone", expiresAt),
	} {
		if err := s.CreatePendingAuth(t.Context(), p); err != nil {
			t.Fatalf("CreatePendingAuth: %v", err)
		}
	}

	tests := []struct {
		name    string
		id      string
		now     time.Time
		wantErr error
	}{
		{"first claim succeeds", "live", baseTime, nil},
		{"second claim is not found", "live", baseTime, ErrNotFound},
		{"unknown ID", "unknown", baseTime, ErrNotFound},
		{"expired at boundary", "gone", expiresAt, ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.ClaimPendingAuth(t.Context(), tt.id, tt.now)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}

	if _, err := s.GetPendingAuthByUpstreamState(t.Context(), []byte("state-live")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("claimed pending auth remains readable by state: %v", err)
	}
	if _, err := s.GetPendingAuth(t.Context(), "live"); err != nil {
		t.Fatalf("claimed pending auth must remain readable by ID: %v", err)
	}
}

func TestPendingAuth_NotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetPendingAuth(t.Context(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetPendingAuth err = %v, want ErrNotFound", err)
	}
	if err := s.SetPendingAuthLogin(t.Context(), "missing", Login{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetPendingAuthLogin err = %v, want ErrNotFound", err)
	}
}

func TestDeletePendingAuth(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreatePendingAuth(t.Context(), testPendingAuth("p1", "state-1", baseTime.Add(time.Minute))); err != nil {
		t.Fatalf("CreatePendingAuth: %v", err)
	}
	if err := s.DeletePendingAuth(t.Context(), "p1"); err != nil {
		t.Fatalf("DeletePendingAuth: %v", err)
	}
	if _, err := s.GetPendingAuth(t.Context(), "p1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCreatePendingAuth_DuplicateStateRejected(t *testing.T) {
	s := newTestStore(t)
	if err := s.CreatePendingAuth(t.Context(), testPendingAuth("p1", "same", baseTime.Add(time.Minute))); err != nil {
		t.Fatalf("CreatePendingAuth: %v", err)
	}
	if err := s.CreatePendingAuth(t.Context(), testPendingAuth("p2", "same", baseTime.Add(time.Minute))); err == nil {
		t.Fatal("want error for duplicate upstream state hash")
	}
}
