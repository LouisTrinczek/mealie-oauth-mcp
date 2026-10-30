//go:build !unix

package store

import (
	"errors"
	"os"
)

func lockFile(string) (*os.File, error) {
	return nil, errors.New("data directory locking is unsupported on this platform")
}
