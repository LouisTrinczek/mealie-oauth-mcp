package store

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func openTemp(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(context.Background(), dir, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func TestOpen_MigratesEmpty(t *testing.T) {
	s := openTemp(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })

	var version int
	if err := s.db.QueryRowContext(t.Context(), `SELECT MAX(version_id) FROM goose_db_version`).Scan(&version); err != nil {
		t.Fatalf("query version: %v", err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}
}

func TestOpen_SchemaGolden(t *testing.T) {
	s := openTemp(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })

	rows, err := s.db.QueryContext(t.Context(), `SELECT sql FROM sqlite_master WHERE type IN ('table','index') AND name NOT LIKE 'goose%' AND name NOT LIKE 'sqlite%' ORDER BY name`)
	if err != nil {
		t.Fatalf("query schema: %v", err)
	}
	defer func() { _ = rows.Close() }()
	var statements []string
	for rows.Next() {
		var statement string
		if err := rows.Scan(&statement); err != nil {
			t.Fatalf("scan: %v", err)
		}
		statements = append(statements, statement)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	got := strings.Join(statements, "\n\n") + "\n"

	testdata, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatalf("open testdata: %v", err)
	}
	defer func() { _ = testdata.Close() }()
	if *update {
		if err := testdata.WriteFile("schema.golden", []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := testdata.ReadFile("schema.golden")
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("schema differs from golden\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestOpen_SecondInstanceLocked(t *testing.T) {
	dir := t.TempDir()
	first := openTemp(t, dir)
	t.Cleanup(func() { _ = first.Close() })

	_, err := Open(context.Background(), dir, slog.New(slog.DiscardHandler))
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("err = %v, want ErrLocked", err)
	}
}

func TestOpen_SchemaTooNew(t *testing.T) {
	dir := t.TempDir()
	s := openTemp(t, dir)
	if _, err := s.db.ExecContext(t.Context(), `INSERT INTO goose_db_version (version_id, is_applied) VALUES (999, 1)`); err != nil {
		t.Fatalf("insert version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := Open(context.Background(), dir, slog.New(slog.DiscardHandler))
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("err = %v, want ErrSchemaTooNew", err)
	}
	for _, want := range []string{"999", "1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention version %s", err, want)
		}
	}
}

func TestOpen_Reopen(t *testing.T) {
	dir := t.TempDir()
	first := openTemp(t, dir)
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	second := openTemp(t, dir)
	if err := second.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpen_UnwritableDir(t *testing.T) {
	_, err := Open(context.Background(), filepath.Join(t.TempDir(), "missing"), slog.New(slog.DiscardHandler))
	if err == nil {
		t.Fatal("want error for missing directory")
	}
}

func TestPing(t *testing.T) {
	s := openTemp(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func TestPing_Closed(t *testing.T) {
	s := openTemp(t, t.TempDir())
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Ping(context.Background()); err == nil {
		t.Fatal("want error after Close")
	}
}

func TestOpen_AppliesPragmas(t *testing.T) {
	s := openTemp(t, t.TempDir())
	t.Cleanup(func() { _ = s.Close() })

	tests := []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
		{"synchronous", "1"},
	}
	for _, tt := range tests {
		var got string
		if err := s.db.QueryRowContext(t.Context(), "PRAGMA "+tt.pragma).Scan(&got); err != nil {
			t.Fatalf("PRAGMA %s: %v", tt.pragma, err)
		}
		if got != tt.want {
			t.Errorf("PRAGMA %s = %q, want %q", tt.pragma, got, tt.want)
		}
	}
}

func TestOpen_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Open(ctx, t.TempDir(), slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("want error for cancelled context")
	}
}

func TestOpen_ReleasesLockAfterFailure(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, dir, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("want error for cancelled context")
	}

	s := openTemp(t, dir)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpen_LockPathUnusable(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, lockFileName), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	if _, err := Open(context.Background(), dir, slog.New(slog.DiscardHandler)); err == nil {
		t.Fatal("want error when lock path is a directory")
	}
}

func TestPing_DatabaseWriteLocked(t *testing.T) {
	dir := t.TempDir()
	s := openTemp(t, dir)
	t.Cleanup(func() { _ = s.Close() })

	other, err := sql.Open("sqlite", "file:"+filepath.Join(dir, databaseFile))
	if err != nil {
		t.Fatalf("open second connection: %v", err)
	}
	t.Cleanup(func() { _ = other.Close() })
	tx, err := other.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, err := tx.ExecContext(t.Context(), `INSERT INTO consents (mealie_user_id, client_id, granted_at) VALUES ('u', 'c', 1)`); err != nil {
		t.Fatalf("take write lock: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := s.Ping(ctx); err == nil {
		t.Fatal("want error while another connection holds the write lock")
	}
}

func TestMigrate_Failures(t *testing.T) {
	tests := []struct {
		name       string
		migrations fstest.MapFS
	}{
		{"no migrations", fstest.MapFS{}},
		{"invalid sql", fstest.MapFS{"migrations/00001_bad.sql": {Data: []byte("-- +goose Up\nNOT SQL;\n")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), databaseFile))
			if err != nil {
				t.Fatalf("sql.Open: %v", err)
			}
			defer func() { _ = db.Close() }()

			if err := migrate(t.Context(), db, tt.migrations, slog.New(slog.DiscardHandler)); err == nil {
				t.Fatal("want error")
			}
		})
	}
}
