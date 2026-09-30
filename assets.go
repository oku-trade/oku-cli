// Package oku embeds the canonical documentation and agent skill assets that
// live at the repository root so installed binaries can serve them offline.
package oku

import (
	"embed"
	"io/fs"
)

// Runtime documentation shipped in binaries. This is an explicit allowlist:
// internal notes (for example docs/validation.md, which holds local
// validation evidence) must never be embedded by a wildcard.
//
//go:embed docs/architecture.md docs/commands.md docs/deployment.md docs/local-testing.md docs/releasing.md docs/security.md docs/usage.md
var docsFS embed.FS

//go:embed all:skills
var skillsFS embed.FS

// Docs returns the bundled docs/ directory as a filesystem rooted at docs/.
func Docs() fs.FS { return mustSub(docsFS, "docs") }

// Skills returns the bundled skills/ directory rooted at skills/, one
// subdirectory per skill containing SKILL.md.
func Skills() fs.FS { return mustSub(skillsFS, "skills") }

func mustSub(f fs.FS, dir string) fs.FS {
	s, err := fs.Sub(f, dir)
	if err != nil {
		panic(err)
	}
	return s
}
