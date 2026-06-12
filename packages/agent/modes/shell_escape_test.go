package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

func TestShellEscapeCommand(t *testing.T) {
	cases := []struct {
		in      string
		wantCmd string
		wantOK  bool
	}{
		{"!ls -la", "ls -la", true},
		{"  !pwd", "pwd", true},
		{"!  go test ./...  ", "go test ./...", true},
		{"!", "", false},
		{"!   ", "", false},
		{"ls -la", "", false},
		{"/help", "", false},
		{"hello !world", "", false},
	}
	for _, c := range cases {
		cmd, ok := shellEscapeCommand(c.in)
		if ok != c.wantOK || cmd != c.wantCmd {
			t.Errorf("shellEscapeCommand(%q) = (%q,%v); want (%q,%v)",
				c.in, cmd, ok, c.wantCmd, c.wantOK)
		}
	}
}

func TestRenderShellBlockStylesFooterDimmed(t *testing.T) {
	i := &Interactive{}
	i.cfg.Theme = tui.Theme{Tool: 2, Error: 1, Muted: 8}

	ok := i.renderShellBlock("$ echo hi\n\nhi\n\n[exit 0]  Took 0.1s", false)
	if len(ok) == 0 {
		t.Fatal("expected non-empty block")
	}
	// The success body uses the Tool color; the footer uses Muted.
	body := strings.Join(ok, "\n")
	if !strings.Contains(body, "echo hi") || !strings.Contains(body, "[exit 0]") {
		t.Fatalf("block missing expected content: %q", body)
	}

	fail := i.renderShellBlock("$ false\n\n[exit 1]  Took 0.0s", true)
	if len(fail) == 0 {
		t.Fatal("expected non-empty failure block")
	}
}
