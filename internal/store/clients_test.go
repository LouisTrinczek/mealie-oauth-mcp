package store

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRedirectURIsRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		uris []string
	}{
		{"one", []string{"https://client.example.com/cb"}},
		{"several with odd characters", []string{"http://127.0.0.1:8080/cb", "https://a.example.com/cb?x=1&y=\"2\"", "app://callback"}},
		{"none", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			want := Client{ID: "c", Source: "dcr", Name: "Claude", RedirectURIs: tt.uris, CreatedAt: baseTime}
			if err := s.UpsertClient(t.Context(), want); err != nil {
				t.Fatalf("UpsertClient: %v", err)
			}
			got, err := s.GetClient(t.Context(), "c")
			if err != nil {
				t.Fatalf("GetClient: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("client = %+v, want %+v", got, want)
			}
		})
	}
}

func TestUpsertClient_UpdatesExisting(t *testing.T) {
	s := newTestStore(t)
	first := Client{ID: "c", Source: "cimd", Name: "Old", RedirectURIs: []string{"https://a/cb"}, CreatedAt: baseTime, MetadataFetchedAt: baseTime}
	if err := s.UpsertClient(t.Context(), first); err != nil {
		t.Fatalf("UpsertClient: %v", err)
	}
	refreshed := Client{ID: "c", Source: "cimd", Name: "New", RedirectURIs: []string{"https://b/cb"}, CreatedAt: baseTime.Add(time.Hour), MetadataFetchedAt: baseTime.Add(time.Hour)}
	if err := s.UpsertClient(t.Context(), refreshed); err != nil {
		t.Fatalf("UpsertClient: %v", err)
	}

	got, err := s.GetClient(t.Context(), "c")
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	want := refreshed
	want.CreatedAt = first.CreatedAt
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("client = %+v, want %+v", got, want)
	}
}

func TestGetClient_NotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetClient(t.Context(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGetClient_CorruptRedirectURIs(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.ExecContext(t.Context(),
		`INSERT INTO clients (id, source, name, redirect_uris, created_at) VALUES ('c', 'dcr', 'n', 'not json', 1)`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.GetClient(t.Context(), "c"); err == nil {
		t.Fatal("want error for corrupt redirect URIs")
	}
}
