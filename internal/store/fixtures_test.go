package store

import (
	"testing"
	"time"
)

var baseTime = time.Unix(1_800_000_000, 0).UTC()

func newTestStore(t *testing.T) *Store {
	t.Helper()
	s := openTemp(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func testSession(id string) Session {
	return Session{
		ID:                   id,
		ClientID:             "client-1",
		MealieUserID:         "user-1",
		MealieUsername:       "alice",
		MealieTokenCT:        []byte("ciphertext"),
		MealieTokenExpiresAt: baseTime.Add(48 * time.Hour),
		MealieTokenLifetime:  48 * time.Hour,
		Resource:             "https://mcp.example.com/mcp",
		CreatedAt:            baseTime,
		ExpiresAt:            baseTime.Add(720 * time.Hour),
		LastUsedAt:           baseTime,
	}
}

func testAuthCode(hash, sessionID string, expiresAt time.Time) AuthCode {
	return AuthCode{
		Hash:          []byte(hash),
		SessionID:     sessionID,
		ClientID:      "client-1",
		RedirectURI:   "https://client.example.com/cb",
		CodeChallenge: "challenge",
		Resource:      "https://mcp.example.com/mcp",
		ExpiresAt:     expiresAt,
	}
}

func seedSession(t *testing.T, s *Store, session Session, codeHash string) {
	t.Helper()
	code := testAuthCode(codeHash, session.ID, baseTime.Add(time.Minute))
	if err := s.CreateSessionWithCode(t.Context(), session, code); err != nil {
		t.Fatalf("CreateSessionWithCode: %v", err)
	}
}

func testPendingAuth(id, stateHash string, expiresAt time.Time) PendingAuth {
	return PendingAuth{
		ID:                 id,
		ClientID:           "client-1",
		RedirectURI:        "https://client.example.com/cb",
		ClientState:        "client-state",
		CodeChallenge:      "challenge",
		Resource:           "https://mcp.example.com/mcp",
		BrowserBindingHash: []byte("binding"),
		UpstreamStateHash:  []byte(stateHash),
		UpstreamVerifier:   "verifier",
		UpstreamNonce:      "nonce",
		CreatedAt:          baseTime,
		ExpiresAt:          expiresAt,
	}
}
