package build

import (
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
)

// captureWebHelp runs PrintWebHelp (which writes to stderr) and returns its text.
func captureWebHelp(t *testing.T) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	PrintWebHelp()
	_ = w.Close()
	os.Stderr = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestWebHelpDocumentsEveryWebFlag guards the gap that hid --web-stage (and
// --web-allow-login): a --web-* flag added to the arg parser but never to the
// `terva web --help` text. Every --web-* flag the parser accepts must appear in
// the rendered help, so a new one cannot ship undocumented.
func TestWebHelpDocumentsEveryWebFlag(t *testing.T) {
	help := captureWebHelp(t)

	src, err := os.ReadFile("args.go")
	if err != nil {
		t.Fatal(err)
	}
	// Pull --web-* flags from the parser's case labels only (not comments), so the
	// set is exactly what the CLI accepts.
	caseLine := regexp.MustCompile(`(?m)^\s*case .*`)
	webFlag := regexp.MustCompile(`"(--web-[a-z-]+)"`)
	seen := map[string]bool{}
	for _, line := range caseLine.FindAllString(string(src), -1) {
		for _, m := range webFlag.FindAllStringSubmatch(line, -1) {
			flag := m[1]
			if seen[flag] {
				continue
			}
			seen[flag] = true
			if !strings.Contains(help, flag) {
				t.Errorf("terva web --help does not document %s (accepted by the arg parser) — add it to PrintWebHelp", flag)
			}
		}
	}
	if len(seen) == 0 {
		t.Fatal("found no --web-* flags in args.go case labels; the extraction regex is stale")
	}
	// The two this test was written for, checked directly.
	for _, f := range []string{"--web-stage", "--web-allow-login"} {
		if !strings.Contains(help, f) {
			t.Errorf("web help missing %s", f)
		}
	}
}
