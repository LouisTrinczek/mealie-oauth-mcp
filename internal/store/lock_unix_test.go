//go:build unix

package store

import (
	"errors"
	"syscall"
	"testing"
)

func TestFlockError(t *testing.T) {
	if err := flockError(syscall.EWOULDBLOCK); !errors.Is(err, ErrLocked) {
		t.Fatalf("EWOULDBLOCK: err = %v, want ErrLocked", err)
	}
	err := flockError(syscall.ENOLCK)
	if errors.Is(err, ErrLocked) || !errors.Is(err, syscall.ENOLCK) {
		t.Fatalf("ENOLCK: err = %v, want the wrapped errno and not ErrLocked", err)
	}
}
