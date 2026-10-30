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

func TestPristine(t *testing.T) {
	root := t.TempDir()
	if inst, pr, err := pristineFS(testFS, root); err != nil || inst || pr {
		t.Fatalf("empty root: %v %v %v", inst, pr, err)
	}
	if _, err := install(testFS, root, false); err != nil {
		t.Fatal(err)
	}
	if inst, pr, err := pristineFS(testFS, root); err != nil || !inst || !pr {
		t.Fatalf("fresh install: %v %v %v", inst, pr, err)
	}
	_ = os.Remove(filepath.Join(root, "oku-perps", "reference", "cmds.md"))
	if _, pr, _ := pristineFS(testFS, root); !pr {
		t.Fatal("missing file should still be pristine")
	}
	_ = os.WriteFile(filepath.Join(root, "oku-perps", "SKILL.md"), []byte("edited"), 0o644)
	if inst, pr, _ := pristineFS(testFS, root); !inst || pr {
		t.Fatal("edited file must not be pristine")
	}

	old := t.TempDir()
	v0 := fstest.MapFS{"oku-perps/SKILL.md": {Data: []byte("skill v0")}}
	if _, err := install(v0, old, false); err != nil {
		t.Fatal(err)
	}
	if inst, pr, _ := pristineFS(testFS, old); !inst || !pr {
		t.Fatal("older oku install with manifest should be pristine")
	}
	_ = os.WriteFile(filepath.Join(old, "oku-perps", "SKILL.md"), []byte("edited"), 0o644)
	if _, pr, _ := pristineFS(testFS, old); pr {
		t.Fatal("edited older install must not be pristine")
	}
}

func TestBundledConsolidationSkillsInstall(t *testing.T) {
	listed, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"consolidate-agent-funds", "coinbase-deposit-targets"} {
		found := false
		for _, skill := range listed {
			found = found || skill.Name == name
		}
		if !found {
			t.Fatalf("missing bundled skill %s", name)
		}
	}
	root := t.TempDir()
	if _, err := Install(root, false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"consolidate-agent-funds", "coinbase-deposit-targets"} {
		bundled, err := Read(name, "SKILL.md")
		if err != nil {
			t.Fatal(err)
		}
		installed, err := os.ReadFile(filepath.Join(root, name, "SKILL.md"))
		if err != nil || string(installed) != string(bundled) {
			t.Fatalf("%s not installed from bundle: %v", name, err)
		}
	}
}
