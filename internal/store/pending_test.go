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
	got, err := s.GetPendingAuth(t.Context(), "p1")
	if err != nil {
		t.Fatalf("GetPendingAuth: %v", err)
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

func TestTakePendingAuthByUpstreamState_SingleUseAndExpiry(t *testing.T) {
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
		state   string
		now     time.Time
		wantID  string
		wantErr error
	}{
		{"first take succeeds", "state-live", baseTime, "live", nil},
		{"second take is not found", "state-live", baseTime, "", ErrNotFound},
		{"unknown state", "state-unknown", baseTime, "", ErrNotFound},
		{"expired at boundary", "state-gone", expiresAt, "", ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.TakePendingAuthByUpstreamState(t.Context(), []byte(tt.state), tt.now)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got.ID != tt.wantID {
				t.Fatalf("ID = %q, want %q", got.ID, tt.wantID)
			}
		})
	}

	if _, err := s.GetPendingAuth(t.Context(), "live"); err != nil {
		t.Fatalf("taken pending auth must remain readable by ID: %v", err)
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
