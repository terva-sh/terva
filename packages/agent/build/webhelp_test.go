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
	// Drain the read end concurrently: PrintWebHelp writes several KB, which
	// overflows a small pipe buffer (Windows' is ~4KB) and would deadlock the
	// write if we only read after it returns. Copy while it writes.
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	PrintWebHelp()
	_ = w.Close()
	os.Stderr = old
	return <-done
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

// captureServeHelp is captureWebHelp for the supervisor screen.
func captureServeHelp(t *testing.T) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	PrintServeHelp()
	_ = w.Close()
	os.Stderr = old
	return <-done
}

// TestServeHelpDocumentsEverySupervisorFlag is the web census, one screen over.
//
// It exists because the --web-* census would NOT have caught --tenant-root: the
// gap it guards is "a flag the parser accepts that no help screen mentions",
// and that gap does not care what the flag is called. It has already had to
// grow once, when --containment arrived under a third prefix — so the pattern
// below matches every prefix the supervisor screen owns, and adding a fourth
// means adding it here too.
func TestServeHelpDocumentsEverySupervisorFlag(t *testing.T) {
	help := captureServeHelp(t)

	src, err := os.ReadFile("args.go")
	if err != nil {
		t.Fatal(err)
	}
	caseLine := regexp.MustCompile(`(?m)^\s*case .*`)
	tenantFlag := regexp.MustCompile(`"(--(?:tenant|containment)[a-z-]*)"`)
	seen := map[string]bool{}
	found := 0
	for _, line := range caseLine.FindAllString(string(src), -1) {
		for _, m := range tenantFlag.FindAllStringSubmatch(line, -1) {
			flag := m[1]
			if seen[flag] {
				continue
			}
			seen[flag] = true
			found++
			if !strings.Contains(help, flag) {
				t.Errorf("terva serve --help does not document %s (accepted by the arg parser) — add it to PrintServeHelp", flag)
			}
		}
	}
	// A census that scanned nothing would pass in silence, which is how a
	// renamed flag prefix turns this guard off without anyone noticing.
	if found == 0 {
		t.Fatal("the scan found no supervisor flags in args.go — this census is no longer looking at anything")
	}
}
