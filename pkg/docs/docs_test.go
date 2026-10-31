package docs

import (
	"testing"
	"testing/fstest"
)

var store = Store{FS: fstest.MapFS{
	"usage.md":    {Data: []byte("# Usage\n\nRun oku orders prepare first.\nthen submit\n")},
	"security.md": {Data: []byte("# Security\n\nRevoke the agent key separately.\n")},
}}

func TestListShowSearch(t *testing.T) {
	l, err := store.List()
	if err != nil || len(l) != 2 || l[0].Name != "security" || l[1].Title != "Usage" {
		t.Fatalf("list %+v %v", l, err)
	}
	if b, err := store.Show("usage.md"); err != nil || len(b) == 0 {
		t.Fatalf("show: %v", err)
	}
	for _, bad := range []string{"../x", "a/b", "", "missing"} {
		if _, err := store.Show(bad); err == nil {
			t.Errorf("show %q succeeded", bad)
		}
	}
	m, err := store.Search("ORDERS prepare", 0)
	if err != nil || len(m) != 1 || m[0].Name != "usage" || m[0].Line != 3 {
		t.Fatalf("search %+v %v", m, err)
	}
	if _, err := store.Search("  ", 0); err == nil {
		t.Fatal("empty query accepted")
	}
}

func TestBundledDocsPresent(t *testing.T) {
	l, err := Bundled().List()
	if err != nil || len(l) == 0 {
		t.Fatalf("no bundled docs: %v", err)
	}
	if _, err := Bundled().Show("commands"); err != nil {
		t.Fatal(err)
	}
}

func TestBundledDocsExcludeInternalNotes(t *testing.T) {
	if _, err := Bundled().Show("releasing"); err == nil {
		t.Fatal("maintainer release notes are embedded in the binary")
	}
	l, _ := Bundled().List()
	if len(l) != 7 {
		t.Fatalf("unexpected bundled docs %+v", l)
	}
}
