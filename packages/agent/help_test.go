package agent

import (
	"io"
	"os"
	"strings"
	"testing"
)

// capturePrintHelp runs PrintHelp (which writes to stderr) and returns its text.
// It reads concurrently so the capture never deadlocks on a full pipe buffer.
func capturePrintHelp(t *testing.T) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	PrintHelp("test")
	_ = w.Close()
	os.Stderr = old
	return <-done
}

// TestHelpListsEverySubcommandMode guards the gap that hid --replay and --acp: a
// run mode reachable as `terva <sub>` (routed by the shims in cli.go) that never
// appears in `terva --help`, so a user can't discover it. Every shim must show
// up somewhere in the help screen.
func TestHelpListsEverySubcommandMode(t *testing.T) {
	help := capturePrintHelp(t)
	for _, sub := range []string{"rpc", "acp", "web", "attach", "replay"} {
		if !strings.Contains(help, "terva "+sub) {
			t.Errorf("terva --help does not list the %q subcommand mode", sub)
		}
	}
	// The footer should route to the CLI reference where the modes are detailed.
	if !strings.Contains(help, "docs/cli.md") {
		t.Error("help footer should point at docs/cli.md")
	}
}
