package auth

import (
	"fmt"
	"path/filepath"

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
	return config.LockPath(filepath.Join(dir, "credentials", ".locks", profile+".lock"))
}
