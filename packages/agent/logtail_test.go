package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLog(t *testing.T, home, file, content string) {
	t.Helper()
	dir := filepath.Join(home, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// lastLogReason returns the extension's own stderr (the actual error), not
// terva's "[terva] …" host annotations.
func TestLastLogReasonSkipsHostAnnotations(t *testing.T) {
	home := withTempHome(t)
	writeLog(t, home, "ext-foo.log",
		"starting up\nError: missing API key\n[terva] extension foo read loop exited at 2026-06-22T00:00:00Z\n")
	if got := lastLogReason("ext", "foo"); got != "Error: missing API key" {
		t.Errorf("reason = %q, want the extension's error line", got)
	}
}

func TestLastLogReasonFallsBackToLastLine(t *testing.T) {
	home := withTempHome(t)
	writeLog(t, home, "ext-bar.log", "[terva] one\n[terva] two\n")
	if got := lastLogReason("ext", "bar"); got != "[terva] two" {
		t.Errorf("all-annotation log should fall back to the last line, got %q", got)
	}
}

func TestLastLogReasonNoLog(t *testing.T) {
	withTempHome(t)
	if got := lastLogReason("ext", "missing"); got != "" {
		t.Errorf("no log should give empty reason, got %q", got)
	}
}

func TestLogTailLinesLastN(t *testing.T) {
	home := withTempHome(t)
	var b strings.Builder
	for i := 0; i < 100; i++ {
		b.WriteString("line\n")
	}
	writeLog(t, home, "mcp-x.log", b.String())
	lines := logTailLines(logPathFor("mcp", "x"), 10)
	if len(lines) != 10 {
		t.Errorf("tail = %d lines, want 10", len(lines))
	}
}

func TestCapLine(t *testing.T) {
	long := strings.Repeat("x", 500)
	if got := capLine(long); len([]rune(got)) > 200 {
		t.Errorf("capLine should bound length, got %d runes", len([]rune(got)))
	}
}
