package store

import (
	"database/sql"
	"time"
)

type Client struct {
	ID                string
	Source            string
	Name              string
	RedirectURIs      []string
	CreatedAt         time.Time
	MetadataFetchedAt time.Time
}

type PendingAuth struct {
	ID                   string
	ClientID             string
	RedirectURI          string
	ClientState          string
	CodeChallenge        string
	Resource             string
	BrowserBindingHash   []byte
	UpstreamStateHash    []byte
	UpstreamVerifier     string
	UpstreamNonce        string
	MealieUserID         string
	MealieUsername       string
	MealieTokenCT        []byte
	MealieTokenExpiresAt time.Time
	MealieTokenLifetime  time.Duration
	CreatedAt            time.Time
	ExpiresAt            time.Time
}

type Login struct {
	MealieUserID         string
	MealieUsername       string
	MealieTokenCT        []byte
	MealieTokenExpiresAt time.Time
	MealieTokenLifetime  time.Duration
}

type Session struct {
	ID                   string
	ClientID             string
	MealieUserID         string
	MealieUsername       string
	MealieTokenCT        []byte
	MealieTokenExpiresAt time.Time
	MealieTokenLifetime  time.Duration
	Resource             string
	CreatedAt            time.Time
	ExpiresAt            time.Time
	LastUsedAt           time.Time
	RevokedAt            time.Time
}

type AuthCode struct {
	Hash          []byte
	SessionID     string
	ClientID      string
	RedirectURI   string
	CodeChallenge string
	Resource      string
	ExpiresAt     time.Time
	UsedAt        time.Time
}

type AccessToken struct {
	Hash      []byte
	SessionID string
	ExpiresAt time.Time
}

type RefreshToken struct {
	Hash      []byte
	SessionID string
	ExpiresAt time.Time
	UsedAt    time.Time
}

func unixSeconds(t time.Time) int64 {
	return t.Unix()
}

func nullableSeconds(t time.Time) sql.NullInt64 {
	if t.IsZero() {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: t.Unix(), Valid: true}
}

func timeFromSeconds(s int64) time.Time {
	return time.Unix(s, 0).UTC()
}

func timeFromNullable(n sql.NullInt64) time.Time {
	if !n.Valid {
		return time.Time{}
	}
	return timeFromSeconds(n.Int64)
}

func seconds(d time.Duration) int64 {
	return int64(d / time.Second)
}

func durationFromNullable(n sql.NullInt64) time.Duration {
	return time.Duration(n.Int64) * time.Second
}
