package skills

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

var testFS = fstest.MapFS{
	"oku-perps/SKILL.md":          {Data: []byte("skill v1")},
	"oku-perps/reference/cmds.md": {Data: []byte("ref")},
	"not-a-skill/README.md":       {Data: []byte("x")},
}

func TestInstallNeverOverwritesWithoutForce(t *testing.T) {
	root := t.TempDir()
	if _, err := install(testFS, root, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "not-a-skill")); !os.IsNotExist(err) {
		t.Fatal("directory without SKILL.md installed")
	}
	// Idempotent reinstall.
	if _, err := install(testFS, root, false); err != nil {
		t.Fatalf("reinstall unchanged: %v", err)
	}
	user := filepath.Join(root, "oku-perps", "SKILL.md")
	_ = os.WriteFile(user, []byte("user edits"), 0o644)
	_ = os.Remove(filepath.Join(root, "oku-perps", "reference", "cmds.md"))
	if _, err := install(testFS, root, false); !errors.Is(err, ErrExists) {
		t.Fatalf("want ErrExists, got %v", err)
	}
	// All-or-nothing: the missing file must not have been written either.
	if _, err := os.Stat(filepath.Join(root, "oku-perps", "reference", "cmds.md")); !os.IsNotExist(err) {
		t.Fatal("partial install on conflict")
	}
	if b, _ := os.ReadFile(user); string(b) != "user edits" {
		t.Fatal("user file overwritten")
	}
	if _, err := uninstall(testFS, root, false); !errors.Is(err, ErrExists) {
		t.Fatal("uninstall removed modified file")
	}
	if _, err := install(testFS, root, true); err != nil {
		t.Fatal(err)
	}
	if _, err := uninstall(testFS, root, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "oku-perps")); !os.IsNotExist(err) {
		t.Fatal("skill dir left behind")
	}
}
