package config

import (
	"errors"
	"os"
	"path/filepath"
)

// ErrLocked reports that TryLockPath found the lock held by another process.
var ErrLocked = errors.New("lock held by another process")

// LockPath takes an exclusive, blocking, cross-process lock on path, creating
// it with mode 0600. The returned function releases it. It uses flock on Unix
// and LockFileEx on Windows; both are released automatically if the process dies.
func LockPath(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	if err := lockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = unlockFile(f); _ = f.Close() }, nil
}

// TryLockPath is LockPath without waiting: it returns ErrLocked when another
// process holds the lock.
func TryLockPath(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	if err := tryLockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = unlockFile(f); _ = f.Close() }, nil
}
