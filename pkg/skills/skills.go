// Package skills bundles agent skill assets into the binary and installs
// them without silently overwriting user files.
//
// Integration contract: skill content lives at assets/<skill-name>/SKILL.md
// (plus optional reference files) and is owned by the docs worker. Every
// top-level directory under assets containing SKILL.md is a skill.
package skills

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed all:assets
var assets embed.FS

// Skill describes a bundled skill.
type Skill struct {
	Name  string   `json:"name"`
	Files []string `json:"files"`
}

// Targets maps supported agents to their user skill directories relative to
// the home directory. Sources: jcode crates/jcode-base/src/skill.rs
// (~/.jcode/skills); Codex docs "Where Codex loads local skills"
// ($HOME/.agents/skills); Claude Code user skills (~/.claude/skills).
var Targets = map[string]string{
	"jcode":  ".jcode/skills",
	"claude": ".claude/skills",
	"codex":  ".agents/skills",
}

// List returns bundled skills.
func List() ([]Skill, error) { return list(assets) }

func list(fsys fs.FS) ([]Skill, error) {
	entries, err := fs.ReadDir(fsys, "assets")
	if err != nil {
		return nil, err
	}
	out := []Skill{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		root := path.Join("assets", e.Name())
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
			data, err := fs.ReadFile(fsys, path.Join("assets", s.Name, f))
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
	for _, o := range ops {
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
	return results, nil
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
			data, _ := fs.ReadFile(fsys, path.Join("assets", s.Name, f))
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
