// Package skills bundles agent skill assets into the binary and installs
// them without silently overwriting user files.
//
// Canonical skill content lives in the repository root skills/<name>/SKILL.md
// (plus optional reference files), embedded by the root oku package. Every
// top-level directory containing SKILL.md is a skill.
package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	oku "github.com/oku-trade/oku-cli"
)

var assets = oku.Skills()

const manifestName = ".oku-skills.json"

func hashOf(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func readManifest(root string) map[string]string {
	m := map[string]string{}
	if b, err := os.ReadFile(filepath.Join(root, manifestName)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// Skill describes a bundled skill.
type Skill struct {
	Name  string   `json:"name"`
	Files []string `json:"files"`
}

// Targets maps supported agents to their user skill directories relative to
// the home directory.
var Targets = map[string]string{
	"jcode":  ".jcode/skills",
	"claude": ".claude/skills",
	"codex":  ".agents/skills",
}

// List returns bundled skills.
func List() ([]Skill, error) { return list(assets) }

func list(fsys fs.FS) ([]Skill, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	out := []Skill{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		root := e.Name()
		if _, err := fs.Stat(fsys, path.Join(root, "SKILL.md")); err != nil {
			continue
		}
		s := Skill{Name: e.Name(), Files: []string{}}
		err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			s.Files = append(s.Files, strings.TrimPrefix(p, root+"/"))
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Strings(s.Files)
		out = append(out, s)
	}
	return out, nil
}

// ResolveDir returns the install root for a target or explicit dir.
func ResolveDir(target, dir string) (string, error) {
	if dir != "" {
		return filepath.Abs(dir)
	}
	rel, ok := Targets[target]
	if !ok {
		return "", fmt.Errorf("unknown target %q (supported: jcode, claude, codex, or --dir)", target)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rel), nil
}

// ErrExists is returned when a destination file differs and force is false.
var ErrExists = errors.New("destination exists with different content")

// FileResult reports a single file operation.
type FileResult struct {
	Path   string `json:"path"`
	Action string `json:"action"` // created | unchanged | overwritten | conflict | removed | missing
}

// Install copies all bundled skills under root/<name>/. It first plans every
// file; if any differs and force is false, nothing is written.
func Install(root string, force bool) ([]FileResult, error) { return install(assets, root, force) }

func install(fsys fs.FS, root string, force bool) ([]FileResult, error) {
	sk, err := list(fsys)
	if err != nil {
		return nil, err
	}
	if len(sk) == 0 {
		return nil, errors.New("no skills are bundled in this build")
	}
	type op struct {
		dst  string
		data []byte
		res  FileResult
	}
	ops := []op{}
	conflict := false
	for _, s := range sk {
		for _, f := range s.Files {
			data, err := fs.ReadFile(fsys, path.Join(s.Name, f))
			if err != nil {
				return nil, err
			}
			dst := filepath.Join(root, s.Name, filepath.FromSlash(f))
			res := FileResult{Path: dst, Action: "created"}
			existing, err := os.ReadFile(dst)
			switch {
			case err == nil && bytes.Equal(existing, data):
				res.Action = "unchanged"
			case err == nil && force:
				res.Action = "overwritten"
			case err == nil:
				res.Action = "conflict"
				conflict = true
			case !errors.Is(err, os.ErrNotExist):
				return nil, err
			}
			ops = append(ops, op{dst: dst, data: data, res: res})
		}
	}
	results := make([]FileResult, 0, len(ops))
	for _, o := range ops {
		results = append(results, o.res)
	}
	if conflict {
		return results, ErrExists
	}
	manifest := map[string]string{}
	for _, o := range ops {
		rel, _ := filepath.Rel(root, o.dst)
		manifest[filepath.ToSlash(rel)] = hashOf(o.data)
		if o.res.Action == "unchanged" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(o.dst), 0o755); err != nil {
			return results, err
		}
		if err := os.WriteFile(o.dst, o.data, 0o644); err != nil {
			return results, err
		}
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return results, err
	}
	return results, os.WriteFile(filepath.Join(root, manifestName), append(b, '\n'), 0o644)
}

// Pristine reports whether any bundled skill is installed under root and
// whether every installed bundled file still matches this build's content or
// the hash recorded when oku installed it (no user edits). Auto-update only refreshes pristine roots.
func Pristine(root string) (installed, pristine bool, err error) { return pristineFS(assets, root) }

func pristineFS(fsys fs.FS, root string) (bool, bool, error) {
	sk, err := list(fsys)
	if err != nil {
		return false, false, err
	}
	installed, pristine := false, true
	manifest := readManifest(root)
	for _, s := range sk {
		for _, f := range s.Files {
			data, err := fs.ReadFile(fsys, path.Join(s.Name, f))
			if err != nil {
				return false, false, err
			}
			existing, err := os.ReadFile(filepath.Join(root, s.Name, filepath.FromSlash(f)))
			switch {
			case errors.Is(err, os.ErrNotExist):
				continue
			case err != nil:
				return false, false, err
			}
			installed = true
			if !bytes.Equal(existing, data) && manifest[s.Name+"/"+f] != hashOf(existing) {
				pristine = false
			}
		}
	}
	return installed, installed && pristine, nil
}

// Read returns a bundled skill file, e.g. Read("learn-oku", "SKILL.md").
func Read(name, file string) ([]byte, error) {
	if strings.Contains(name, "/") || name == "" || name == "." || name == ".." {
		return nil, fmt.Errorf("invalid skill name %q", name)
	}
	return fs.ReadFile(assets, path.Join(name, path.Clean("/" + file)[1:]))
}

// Uninstall removes only files whose content matches the bundled version,
// unless force is set. Directories are removed only when empty.
func Uninstall(root string, force bool) ([]FileResult, error) { return uninstall(assets, root, force) }

func uninstall(fsys fs.FS, root string, force bool) ([]FileResult, error) {
	sk, err := list(fsys)
	if err != nil {
		return nil, err
	}
	results := []FileResult{}
	conflict := false
	for _, s := range sk {
		for _, f := range s.Files {
			data, _ := fs.ReadFile(fsys, path.Join(s.Name, f))
			dst := filepath.Join(root, s.Name, filepath.FromSlash(f))
			existing, err := os.ReadFile(dst)
			switch {
			case errors.Is(err, os.ErrNotExist):
				results = append(results, FileResult{Path: dst, Action: "missing"})
			case err != nil:
				return results, err
			case !bytes.Equal(existing, data) && !force:
				results = append(results, FileResult{Path: dst, Action: "conflict"})
				conflict = true
			default:
				results = append(results, FileResult{Path: dst, Action: "removed"})
			}
		}
	}
	if conflict {
		return results, ErrExists
	}
	for _, r := range results {
		if r.Action == "removed" {
			if err := os.Remove(r.Path); err != nil {
				return results, err
			}
		}
	}
	_ = os.Remove(filepath.Join(root, manifestName))
	for _, s := range sk {
		removeEmptyDirs(filepath.Join(root, s.Name))
	}
	return results, nil
}

func removeEmptyDirs(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			removeEmptyDirs(filepath.Join(dir, e.Name()))
		}
	}
	_ = os.Remove(dir) // fails harmlessly when not empty
}
