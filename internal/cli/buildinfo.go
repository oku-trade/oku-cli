package cli

import "github.com/oku-trade/oku-cli/internal/buildinfo"

func buildCommit() string { return buildinfo.Commit }
