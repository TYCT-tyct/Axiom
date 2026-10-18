package gateway

import (
	"net/http"
	"testing"
)

// TestClientAppOf: the clients the log names are recognised by their User-Agent whatever its case, the rest are left
// unnamed rather than guessed.
func TestClientAppOf(t *testing.T) {
	t.Parallel()
	for agent, want := range map[string]string{
		"claude-cli/2.0.5 (external, cli)": "claude-code",
		"Claude Code/1.2":                  "claude-code",
		"codex_cli_rs/0.50.0":              "codex",
		"Codex":                            "codex",
		"python-requests/2.32":             "",
		"":                                 "",
	} {
		h := http.Header{}
		h.Set("User-Agent", agent)
		if got := clientAppOf(h); got != want {
			t.Errorf("User-Agent %q is %q, want %q", agent, got, want)
		}
	}
}
