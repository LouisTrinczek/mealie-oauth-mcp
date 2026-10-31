package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func Backup(ctx context.Context, dataDir, dest string) error {
	if _, err := os.Lstat(dest); err == nil {
		return fmt.Errorf("backup file %s already exists; choose a new name", dest)
	}
	source := filepath.Join(dataDir, databaseFile)
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf("no database at %s", source)
	}
	db, _ := sql.Open("sqlite", "file:"+source+"?mode=ro&"+busyTimeout)
	_, err := db.ExecContext(ctx, "VACUUM INTO ?", dest)
	if err != nil {
		err = fmt.Errorf("writing backup: %w", err)
	}
	return errors.Join(err, db.Close())
}
