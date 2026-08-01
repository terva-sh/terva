package tools

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The tool is named bash, and a model writes bash for it. Under /bin/sh on a
// Debian-family host that is dash, where the two constructs a model reaches
// for — a pipeline's real exit code and a diff of two command outputs — are
// syntax errors. Six turns died that way in one reviewed session.
func TestBashRunsBashDialectWhenBashExists(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash on this host; the /bin/sh fallback is the tested path elsewhere")
	}
	tool := &BashTool{CWD: testsupport.TempDir(t)}

	// ${PIPESTATUS[@]} is the canonical "Bad substitution" under dash.
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"command": `true | true; echo "status: ${PIPESTATUS[0]}"`,
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Content[0].(provider.TextBlock).Text
	if strings.Contains(got, "Bad substitution") {
		t.Fatalf("PIPESTATUS not supported — the tool is not running bash:\n%s", got)
	}
	if !strings.Contains(got, "status: 0") {
		t.Fatalf("want PIPESTATUS to expand, got %q", got)
	}

	// Process substitution is the other one, and it is what a model writes to
	// compare two runs of the same binary.
	res, err = tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"command": `diff <(echo same) <(echo same) && echo IDENTICAL`,
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	got = res.Content[0].(provider.TextBlock).Text
	if strings.Contains(got, "Syntax error") {
		t.Fatalf("process substitution unsupported — the tool is not running bash:\n%s", got)
	}
	if !strings.Contains(got, "IDENTICAL") {
		t.Fatalf("want the diff to run, got %q", got)
	}
}

// The fallback must stay reachable: busybox images ship no bash, and the tool
// has to keep working there rather than failing to start.
func TestResolveShellFallsBackToSh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	t.Setenv("PATH", testsupport.TempDir(t)) // no bash anywhere on it
	if got := resolveShell(); got != "/bin/sh" {
		t.Fatalf("resolveShell() = %q, want /bin/sh when bash is absent", got)
	}
}

// The description has to NAME the shell. A model told only "run a shell
// command" assumes bash; one told which shell it has writes for that shell.
func TestBashDescriptionNamesTheShell(t *testing.T) {
	tool := &BashTool{CWD: testsupport.TempDir(t)}
	desc := tool.Description()
	if !strings.Contains(desc, "under "+shellName()) {
		t.Fatalf("description does not name the resolved shell %q:\n%s", shellName(), desc)
	}
}

func TestLastPipelineCommand(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{"bare", "grep -c FAILED out.txt", "grep"},
		{"pipeline", "cargo test 2>&1 | grep -c 'test result: FAILED'", "grep"},
		{"last of several statements", "echo a; cargo build | grep -E '^error'", "grep"},
		{"newline separated", "echo one\ncargo test | grep -c FAILED", "grep"},
		{"pipe inside quotes does not split", `grep -E 'a|b' file`, "grep"},
		{"semicolon inside quotes does not split", `awk '{print}; END{}' f | rg thing`, "rg"},
		{"env assignment skipped", "FOO=bar grep x f", "grep"},
		{"absolute path reduced to base", "/usr/bin/grep x f", "grep"},
		{"real work last stage is not a matcher", "grep -l x . | xargs rm", "xargs"},
		{"plain build", "cargo build --workspace", "cargo"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lastPipelineCommand(c.cmd); got != c.want {
				t.Fatalf("lastPipelineCommand(%q) = %q, want %q", c.cmd, got, c.want)
			}
		})
	}
}

// The exact shape that cost 13 turns and ~293 seconds of re-run test suites in
// one session: a verification pipeline that exits 1 precisely because the build
// is green.
func TestMatchExitHintFiresOnGreenVerification(t *testing.T) {
	cmd := "cargo test --workspace 2>&1 | grep -c 'test result: FAILED'"
	hint := matchExitHint(1, cmd)
	if hint == "" {
		t.Fatal("no hint on the canonical green-verification pipeline")
	}
	if !strings.Contains(hint, "grep") {
		t.Fatalf("hint does not name the command that exited 1: %q", hint)
	}
	if !strings.Contains(hint, "LAST") {
		t.Fatalf("hint does not explain pipeline exit status: %q", hint)
	}
}

// Narrow by construction: a hint that fires on genuine failures would teach the
// model to discount real exit codes, which is worse than the problem it solves.
func TestMatchExitHintStaysNarrow(t *testing.T) {
	cases := []struct {
		name string
		code int
		cmd  string
	}{
		{"success", 0, "cargo test | grep -c FAILED"},
		{"non-one failure", 2, "cargo test | grep -c FAILED"},
		{"compile failure", 1, "cargo build --workspace"},
		{"last stage is real work", 1, "grep -l x . | xargs false"},
		{"missing binary", 127, "definitely-not-a-command | grep x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if hint := matchExitHint(c.code, c.cmd); hint != "" {
				t.Fatalf("unwanted hint for exit %d on %q: %s", c.code, c.cmd, hint)
			}
		})
	}
}

// End to end: the hint has to reach the model through the result body, not just
// exist as a helper.
func TestBashFooterCarriesTheHint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	tool := &BashTool{CWD: testsupport.TempDir(t)}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "echo all-good | grep -c FAILED",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := res.Content[0].(provider.TextBlock).Text
	if !strings.Contains(got, "[exit 1]") {
		t.Fatalf("want the failing exit recorded, got %q", got)
	}
	if !strings.Contains(got, "[hint]") {
		t.Fatalf("hint missing from the result body:\n%s", got)
	}
	// Still an error — the exit code is real and the flag must not be softened.
	if !res.IsError {
		t.Fatal("the hint must explain the error, not suppress it")
	}
}
