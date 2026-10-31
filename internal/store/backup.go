package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const backupFileMode = 0o600

func Backup(ctx context.Context, dataDir, dest string) error {
	return backup(ctx, sql.Open, dataDir, dest)
}

func backup(ctx context.Context, open func(driver, dsn string) (*sql.DB, error), dataDir, dest string) error {
	source := filepath.Join(dataDir, databaseFile)
	if _, err := os.Stat(source); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no database at %s", source)
	} else if err != nil {
		return fmt.Errorf("checking database at %s: %w", source, err)
	}
	db, err := open("sqlite", fileDSN(source, "mode=ro&"+busyTimeout))
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer func() { _ = db.Close() }()
	if err := reserve(dest); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		_ = os.Remove(dest)
		return fmt.Errorf("writing backup: %w", err)
	}
	return nil
}

func reserve(dest string) error {
	file, err := os.OpenFile(filepath.Clean(dest), os.O_CREATE|os.O_EXCL|os.O_WRONLY, backupFileMode)
	if errors.Is(err, fs.ErrExist) {
		return fmt.Errorf("backup file %s already exists; choose a new name", dest)
	}
	if err != nil {
		return fmt.Errorf("creating backup file: %w", err)
	}
	return file.Close()
}
