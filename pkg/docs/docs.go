// Package docs serves the documentation bundled in the binary so it is
// available offline.
package docs

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	oku "github.com/oku-trade/oku-cli"
)

// Doc describes a bundled document.
type Doc struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// Match is one search hit.
type Match struct {
	Name string `json:"name"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Store reads docs from a filesystem of top-level *.md files.
type Store struct{ FS fs.FS }

// Bundled returns the store embedded in this binary.
func Bundled() Store { return Store{FS: oku.Docs()} }

// List returns all documents sorted by name.
func (s Store) List() ([]Doc, error) {
	names, err := fs.Glob(s.FS, "*.md")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := make([]Doc, 0, len(names))
	for _, n := range names {
		b, err := fs.ReadFile(s.FS, n)
		if err != nil {
			return nil, err
		}
		out = append(out, Doc{Name: strings.TrimSuffix(n, ".md"), Title: title(b)})
	}
	return out, nil
}

// Show returns a document by name, with or without the .md suffix.
func (s Store) Show(name string) ([]byte, error) {
	name = strings.TrimSuffix(name, ".md")
	if name == "" || strings.ContainsAny(name, `/\`) || name != path.Clean(name) {
		return nil, fmt.Errorf("invalid document name %q", name)
	}
	return fs.ReadFile(s.FS, name+".md")
}

// Search returns case-insensitive line matches across all documents. Every
// whitespace-separated term must appear in the line.
func (s Store) Search(query string, limit int) ([]Match, error) {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil, fmt.Errorf("empty search query")
	}
	docs, err := s.List()
	if err != nil {
		return nil, err
	}
	out := []Match{}
	for _, d := range docs {
		b, err := s.Show(d.Name)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for n := 1; sc.Scan(); n++ {
			line := sc.Text()
			low := strings.ToLower(line)
			ok := true
			for _, t := range terms {
				ok = ok && strings.Contains(low, t)
			}
			if ok {
				out = append(out, Match{Name: d.Name, Line: n, Text: strings.TrimSpace(line)})
				if limit > 0 && len(out) >= limit {
					return out, nil
				}
			}
		}
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func title(b []byte) string {
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "# ") {
			return strings.TrimSpace(l[2:])
		}
	}
	return ""
}
