//go:build !windows

package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func TryLock(storeDir string) (*FileLock, error) {
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}
	p := filepath.Join(storeDir, ".lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("%w (if no other whatsapp-mcp is running, remove %s)", ErrLocked, p)
		}
		return nil, fmt.Errorf("flock: %w", err)
	}
	return &FileLock{path: p, f: f, state: lockStateLocked}, nil
}

func (l *FileLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
	l.state = lockStateUnlocked
}
