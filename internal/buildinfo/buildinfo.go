// Package buildinfo holds release metadata injected with -ldflags -X.
package buildinfo

// Set with -ldflags "-X github.com/oku-trade/oku-cli/internal/buildinfo.Version=v0.1.0".
// main.version is also honored by cmd/oku.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)
