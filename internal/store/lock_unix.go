//go:build unix

package store

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func lockFile(dataDir string) (*os.File, error) {
	dir, err := os.OpenRoot(dataDir)
	if err != nil {
		return nil, fmt.Errorf("opening data directory: %w", err)
	}
	defer func() { _ = dir.Close() }()
	file, err := dir.OpenFile(lockFileName, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, flockError(err)
	}
	return file, nil
}

func flockError(err error) error {
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrLocked
	}
	return fmt.Errorf("locking data directory: %w", err)
}
