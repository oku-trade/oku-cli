package cli

import "github.com/oku-trade/oku-cli/pkg/buildinfo"

func buildCommit() string { return buildinfo.Commit }
