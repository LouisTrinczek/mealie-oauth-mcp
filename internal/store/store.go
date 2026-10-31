package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

const (
	databaseFile = "mealie-mcp.db"
	lockFileName = ".lock"
	busyTimeout  = "_pragma=busy_timeout(5000)"
	dsnPragmas   = "?_pragma=journal_mode(WAL)&" + busyTimeout + "&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
)

var (
	ErrNotFound     = errors.New("not found")
	ErrAlreadyUsed  = errors.New("already used")
	ErrLocked       = errors.New("data directory is locked by another process")
	ErrSchemaTooNew = errors.New("database schema is newer than this binary supports")
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	db   *sql.DB
	lock *os.File
}

func Open(ctx context.Context, dataDir string, logger *slog.Logger) (*Store, error) {
	lock, err := lockFile(dataDir)
	if err != nil {
		return nil, err
	}
	db, _ := sql.Open("sqlite", "file:"+filepath.Join(dataDir, databaseFile)+dsnPragmas)
	db.SetMaxOpenConns(1)
	if err := migrate(ctx, db, migrationFiles, logger); err != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	return &Store{db: db, lock: lock}, nil
}

func (s *Store) Close() error {
	return errors.Join(s.db.Close(), s.lock.Close())
}

func (s *Store) Ping(ctx context.Context) error {
	return s.probeWriteLock(ctx, nil)
}

func (s *Store) probeWriteLock(ctx context.Context, whileLocked func()) (err error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquiring connection: %w", err)
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	_, beginErr := conn.ExecContext(ctx, "BEGIN IMMEDIATE")
	if beginErr == nil && whileLocked != nil {
		whileLocked()
	}
	_, rollbackErr := conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
	return writeLockProbeError(beginErr, rollbackErr)
}

func writeLockProbeError(beginErr, rollbackErr error) error {
	switch {
	case beginErr != nil:
		return fmt.Errorf("beginning write transaction: %w", beginErr)
	case rollbackErr != nil:
		return fmt.Errorf("rolling back write transaction: %w", rollbackErr)
	}
	return nil
}

func migrate(ctx context.Context, db *sql.DB, files fs.FS, logger *slog.Logger) error {
	migrations, _ := fs.Sub(files, "migrations")
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations, goose.WithSlog(logger), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		return fmt.Errorf("creating migration provider: %w", err)
	}
	if err := rejectNewerSchema(ctx, provider); err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	return nil
}

func rejectNewerSchema(ctx context.Context, provider *goose.Provider) error {
	current, err := provider.GetDBVersion(ctx)
	if err != nil {
		return fmt.Errorf("reading database version: %w", err)
	}
	sources := provider.ListSources()
	supported := sources[len(sources)-1].Version
	if current > supported {
		return fmt.Errorf("%w: database is at version %d, binary supports up to %d", ErrSchemaTooNew, current, supported)
	}
	return nil
}
