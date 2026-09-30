// Command oku-docs generates the Markdown command reference from the executable command tree.
package main

import (
	"log"
	"os"

	"github.com/oku-trade/oku-cli/pkg/cli"
)

func main() {
	if err := cli.WriteCommandReference(os.Stdout); err != nil {
		log.Fatal(err)
	}
}
