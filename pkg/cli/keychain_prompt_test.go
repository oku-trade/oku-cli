package cli

import (
	"testing"

	"github.com/oku-trade/oku-cli/pkg/output"
)

func TestKeychainCodesExitAsAuth(t *testing.T) {
	for _, code := range []output.Code{output.CodeKeychainLocked, output.CodeKeychainAccessDenied, output.CodeKeychainCanceled} {
		if got := code.ExitCode(); got != 3 {
			t.Errorf("%s exit = %d, want 3", code, got)
		}
	}
}

func TestKeychainPromptPolicy(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		stdin, stderr, json, service bool
		ssh                          string
		prompt, gui                  bool
	}{
		{"terminal", true, true, false, false, "", true, true},
		{"stdin redirected", false, true, false, false, "", false, true},
		{"stderr redirected", true, false, false, false, "", false, true},
		{"json", true, true, true, false, "", false, true},
		{"service", true, true, false, true, "", false, true},
		{"ssh terminal", true, true, false, false, "remote", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &env{app: &App{StdinIsTTY: tc.stdin, StderrIsTTY: tc.stderr, Getenv: func(k string) string {
				if k == "SSH_CONNECTION" {
					return tc.ssh
				}
				return ""
			}}, g: globals{json: tc.json, inProcess: tc.service}}
			if got := e.mayPromptForKeychain(); got != tc.prompt {
				t.Errorf("prompt = %v, want %v", got, tc.prompt)
			}
			if got := e.guiSession(); got != tc.gui {
				t.Errorf("GUI = %v, want %v", got, tc.gui)
			}
		})
	}
}

func TestCredentialLoadErrorPreservesStructured(t *testing.T) {
	want := output.Errorf(output.CodeKeychainLocked, "locked")
	if got := credentialLoadError(want); got != error(want) {
		t.Fatalf("structured error rewrapped: %v", got)
	}
}
