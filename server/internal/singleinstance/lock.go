// Package singleinstance provides process-lifetime ownership of an application's
// SQLite data location.
package singleinstance

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

// ErrAlreadyOwned indicates that another process holds the database lock.
var ErrAlreadyOwned = errors.New("database is already owned by another process")

// Lock holds an advisory OS lock until Close or process exit. The lock file is
// intentionally retained after Close; deleting it could let two processes lock
// different inodes during a handoff.
type Lock struct {
	mu   sync.Mutex
	file *os.File
}

// Acquire claims exclusive ownership associated with databasePath. It uses
// flock rather than PID-file contents, so a crash cannot leave stale ownership.
func Acquire(databasePath string) (*Lock, error) {
	lockPath, err := canonicalLockPath(databasePath)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create database directory %q: %w", filepath.Dir(lockPath), err)
	}
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open instance lock %q: %w", lockPath, err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyOwned, databasePath)
		}
		return nil, fmt.Errorf("lock database %q: %w", databasePath, err)
	}
	return &Lock{file: file}, nil
}

// Close releases ownership. It is safe to call more than once.
func (l *Lock) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}

func canonicalLockPath(databasePath string) (string, error) {
	if databasePath == "" {
		return "", errors.New("database path is empty")
	}
	absPath, err := filepath.Abs(databasePath)
	if err != nil {
		return "", fmt.Errorf("resolve database path %q: %w", databasePath, err)
	}
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err == nil {
		absPath = resolvedPath
	} else if errors.Is(err, os.ErrNotExist) {
		// Resolve the deepest existing ancestor, then append all missing path
		// components. This preserves alias protection through existing symlinks
		// while allowing first boot when DATA_DIR has not been created yet.
		existing := absPath
		missing := make([]string, 0, 2)
		for {
			if _, statErr := os.Lstat(existing); statErr == nil {
				break
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return "", fmt.Errorf("inspect database ancestor %q: %w", existing, statErr)
			}
			parent := filepath.Dir(existing)
			if parent == existing {
				return "", fmt.Errorf("resolve database path %q: no existing ancestor", databasePath)
			}
			missing = append(missing, filepath.Base(existing))
			existing = parent
		}
		resolvedAncestor, ancestorErr := filepath.EvalSymlinks(existing)
		if ancestorErr != nil {
			return "", fmt.Errorf("resolve database ancestor %q: %w", existing, ancestorErr)
		}
		absPath = resolvedAncestor
		for index := len(missing) - 1; index >= 0; index-- {
			absPath = filepath.Join(absPath, missing[index])
		}
	} else {
		return "", fmt.Errorf("resolve database path %q: %w", databasePath, err)
	}
	return absPath + ".lock", nil
}
