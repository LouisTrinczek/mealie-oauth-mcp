package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seedDatabase(t *testing.T, dataDir string) {
	t.Helper()
	if err := openTemp(t, dataDir).Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestBackup_WhileStoreHoldsLock(t *testing.T) {
	dataDir := t.TempDir()
	source := openTemp(t, dataDir)
	t.Cleanup(func() { _ = source.Close() })
	seedSession(t, source, testSession("s1"), "hash-1")
	dest := filepath.Join(t.TempDir(), "b.db")

	if err := Backup(t.Context(), dataDir, dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	copied, err := sql.Open("sqlite", "file:"+dest+"?mode=ro")
	if err != nil {
		t.Fatalf("opening backup: %v", err)
	}
	t.Cleanup(func() { _ = copied.Close() })
	var sessions int
	if err := copied.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatalf("counting sessions: %v", err)
	}
	if sessions != 1 {
		t.Errorf("sessions in backup = %d, want 1", sessions)
	}
	var copiedVersion int64
	if err := copied.QueryRowContext(t.Context(), `SELECT MAX(version_id) FROM goose_db_version`).Scan(&copiedVersion); err != nil {
		t.Fatalf("reading backup goose version: %v", err)
	}
	var sourceVersion int64
	if err := source.db.QueryRowContext(t.Context(), `SELECT MAX(version_id) FROM goose_db_version`).Scan(&sourceVersion); err != nil {
		t.Fatalf("reading source goose version: %v", err)
	}
	if copiedVersion != sourceVersion {
		t.Errorf("backup goose version = %d, want %d", copiedVersion, sourceVersion)
	}
}

func TestBackup_RefusesExistingFile(t *testing.T) {
	dataDir := t.TempDir()
	seedDatabase(t, dataDir)
	destDir := t.TempDir()
	dest := filepath.Join(destDir, "b.db")
	if err := os.WriteFile(dest, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := Backup(t.Context(), dataDir, dest)

	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want already exists", err)
	}
	root, err := os.OpenRoot(destDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if content, _ := root.ReadFile("b.db"); string(content) != "keep me" {
		t.Errorf("existing file was modified: %q", content)
	}
}

func TestBackup_MissingDatabase(t *testing.T) {
	err := Backup(t.Context(), t.TempDir(), filepath.Join(t.TempDir(), "b.db"))

	if err == nil || !strings.Contains(err.Error(), "no database at") {
		t.Fatalf("err = %v, want no database at", err)
	}
}

func TestBackup_UnwritableDestination(t *testing.T) {
	dataDir := t.TempDir()
	seedDatabase(t, dataDir)

	err := Backup(t.Context(), dataDir, filepath.Join(t.TempDir(), "missing-dir", "b.db"))

	if err == nil || !strings.Contains(err.Error(), "writing backup") {
		t.Fatalf("err = %v, want writing backup", err)
	}
}
