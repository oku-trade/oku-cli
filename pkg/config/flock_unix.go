//go:build !windows

package config

import (
	"errors"
	"os"
	"syscall"
)

func lockFile(f *os.File) error   { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }
func unlockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
func tryLockFile(f *os.File) error {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrLocked
	}
	return err
}
