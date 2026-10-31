package store

import (
	"context"
	"database/sql"
	"errors"
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

	copied, err := sql.Open("sqlite", fileDSN(dest, "mode=ro"))
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
	content, err := root.ReadFile("b.db")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "keep me" {
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

	if err == nil || !strings.Contains(err.Error(), "creating backup file") {
		t.Fatalf("err = %v, want creating backup file", err)
	}
}

func TestOpenAndBackup_RelativeDataDir(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("data", 0o700); err != nil {
		t.Fatal(err)
	}
	source := openTemp(t, "data")
	t.Cleanup(func() { _ = source.Close() })
	seedSession(t, source, testSession("s1"), "hash-1")

	if err := Backup(t.Context(), "data", "b.db"); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	if _, err := os.Stat(filepath.Join("data", databaseFile)); err != nil {
		t.Errorf("database not created inside the relative directory: %v", err)
	}
}

func TestBackup_RefusesDanglingSymlink(t *testing.T) {
	dataDir := t.TempDir()
	seedDatabase(t, dataDir)
	destDir := t.TempDir()
	dest := filepath.Join(destDir, "b.db")
	if err := os.Symlink(filepath.Join(destDir, "nowhere"), dest); err != nil {
		t.Fatal(err)
	}

	err := Backup(t.Context(), dataDir, dest)

	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("err = %v, want already exists", err)
	}
}

func TestBackup_FileMode(t *testing.T) {
	dataDir := t.TempDir()
	seedDatabase(t, dataDir)
	dest := filepath.Join(t.TempDir(), "b.db")

	if err := Backup(t.Context(), dataDir, dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestBackup_DataDirIsAFile(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notADir, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	err := Backup(t.Context(), notADir, filepath.Join(t.TempDir(), "b.db"))

	if err == nil || strings.Contains(err.Error(), "no database at") || !strings.Contains(err.Error(), "checking database") {
		t.Fatalf("err = %v, want a checking database error", err)
	}
}

func TestBackup_OpenFails(t *testing.T) {
	dataDir := t.TempDir()
	seedDatabase(t, dataDir)
	boom := errors.New("boom")
	open := func(string, string) (*sql.DB, error) { return nil, boom }

	err := backup(t.Context(), open, dataDir, filepath.Join(t.TempDir(), "b.db"))

	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestOpenAndBackup_SpecialCharactersInDataDir(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "odd#dir?x%20y")
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := openTemp(t, dataDir)
	t.Cleanup(func() { _ = source.Close() })
	seedSession(t, source, testSession("s1"), "hash-1")
	dest := filepath.Join(t.TempDir(), "b.db")

	if err := Backup(t.Context(), dataDir, dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dataDir, databaseFile)); err != nil {
		t.Errorf("database not created inside the odd directory: %v", err)
	}
}

func TestBackup_CanceledContextLeavesNoFile(t *testing.T) {
	dataDir := t.TempDir()
	seedDatabase(t, dataDir)
	dest := filepath.Join(t.TempDir(), "b.db")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err := Backup(ctx, dataDir, dest)

	if err == nil || !strings.Contains(err.Error(), "writing backup") {
		t.Fatalf("err = %v, want writing backup", err)
	}
	if _, statErr := os.Lstat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("partial backup left behind: %v", statErr)
	}
}
