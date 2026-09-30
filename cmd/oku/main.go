// Command oku is the Oku perps CLI.
package main

import (
	"os"

	"github.com/oku-trade/oku-cli/pkg/buildinfo"
	"github.com/oku-trade/oku-cli/pkg/cli"
)

// version may be set with -ldflags "-X main.version=v0.1.0".
var version = ""

func main() {
	v := buildinfo.Version
	if version != "" {
		v = version
	}
	os.Exit(cli.NewApp(v).Run(os.Args[1:]))
}
