package auth

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/oku-trade/oku-cli/pkg/config"
)

// LockProfile serializes credential read-modify-write operations across CLI
// processes. Callers must hold it from loading through saving/deleting a record.
// Atomic rename alone cannot prevent a stale writer from destroying a new key.
func LockProfile(profile string) (func(), error) {
	if !config.ValidName(profile) {
		return nil, fmt.Errorf("invalid credential profile")
	}
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, "credentials", ".locks")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, profile+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}
