package store

import "testing"

func TestConsent(t *testing.T) {
	s := newTestStore(t)
	if err := s.GrantConsent(t.Context(), "u1", "c1", baseTime); err != nil {
		t.Fatalf("GrantConsent: %v", err)
	}
	if err := s.GrantConsent(t.Context(), "u1", "c1", baseTime.Add(1)); err != nil {
		t.Fatalf("repeated GrantConsent: %v", err)
	}

	tests := []struct {
		name           string
		userID, client string
		want           bool
	}{
		{"granted", "u1", "c1", true},
		{"other client", "u1", "c2", false},
		{"other user", "u2", "c1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.HasConsent(t.Context(), tt.userID, tt.client)
			if err != nil {
				t.Fatalf("HasConsent: %v", err)
			}
			if got != tt.want {
				t.Fatalf("HasConsent = %v, want %v", got, tt.want)
			}
		})
	}
}
