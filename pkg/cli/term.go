package cli

import (
	"fmt"
	"os"

	"golang.org/x/term"
)

func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

func readSecretTTY(prompt string) ([]byte, error) {
	if !isTerminal(os.Stdin) {
		return nil, errNoTTY
	}
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return b, err
}
