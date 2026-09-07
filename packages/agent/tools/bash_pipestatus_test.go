package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// The guard exists because prose did not hold. Every "fires" case below is a
// shape that actually shipped a wrong answer, and every "silent" case is one
// the guard must never touch. The silent half is the load-bearing half: a lint
// that cries wolf gets worked around, and then it protects nothing.
func TestCheckPipeStatusRead(t *testing.T) {
	cases := []struct {
		name  string
		cmd   string
		fires bool
	}{
		// The three that cost real debugging time.
		{"tail then dollar-question", `git ticket status $id done | tail -3; echo "status exit=$?"`, true},
		{"sed then dollar-question", `git branch -d topic | sed 's/^/  /'; echo $?`, true},
		{"head then dollar-question", "just ci | head -20; echo $?", true},

		{"wc is a filter too", "cargo build | wc -l; echo $?", true},
		{"pipeline in the middle of a command", "echo start; make all | tail -1; echo $?", true},
		{"redirection before the pipe still fires", "just ci 2>&1 | tail -35; echo $?", true},
		{"status read inside a larger string", `make | tail -1; echo "build said $? today"`, true},

		// PIPESTATUS and pipefail both make the read correct.
		{"PIPESTATUS is the fix", `just ci 2>&1 | tail -35; echo "ci exit=${PIPESTATUS[0]}"`, false},
		{"pipefail is the other fix", "set -o pipefail; just ci | tail -5; echo $?", false},

		// The last stage's status is genuinely the subject.
		{"grep -q is a test not a filter", "git status --short | grep -q foo; echo $?", false},
		{"grep -c is a test not a filter", "cargo test | grep -c FAILED; echo $?", false},
		{"diff is real work", "cat a | diff - b; echo $?", false},

		// No status read at all.
		{"bare pipeline", "just ci 2>&1 | tail -35", false},
		{"pipeline then an unrelated statement", "make | tail -1; echo done", false},
		{"status read with no pipeline anywhere", "make; echo $?", false},
		{"filter with no pipe is not a pipeline", "tail -3 log.txt; echo $?", false},

		// The read must sit in the very next statement.
		{"a statement stands between the pipe and the read", "make | tail -1; ls; echo $?", false},

		// Quoting must not manufacture a pipeline.
		{"pipe inside quotes is not a pipeline", `grep -E 'a|b' file; echo $?`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkPipeStatusRead(c.cmd)
			if c.fires && err == nil {
				t.Fatalf("guard stayed silent on a command that hides a failure:\n  %s", c.cmd)
			}
			if !c.fires && err != nil {
				t.Fatalf("guard refused a legitimate command:\n  %s\n  %v", c.cmd, err)
			}
		})
	}
}

// A refusal that only names the problem leaves the reader to invent a fix, and
// the fix they invent is usually to delete the check. The message has to carry
// both escapes.
func TestPipeStatusRefusalNamesTheFix(t *testing.T) {
	err := checkPipeStatusRead(`make | tail -1; echo $?`)
	if err == nil {
		t.Fatal("no refusal on the canonical trap")
	}
	for _, want := range []string{"PIPESTATUS", "pipefail", "tail"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal does not mention %q:\n%s", want, err)
		}
	}
}

// Refusing has to happen before the process starts. A guard that runs the
// command and then complains has already had whatever effect the command has.
func TestBashRefusesPipeStatusBeforeRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	dir := testsupport.TempDir(t)
	tool := &BashTool{CWD: dir}
	_, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"command": `touch marker.txt | tail -1; echo "exit=$?"`,
	}), nil)
	if err == nil {
		t.Fatal("Execute ran a command that reads $? after a formatting pipe")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "marker.txt")); statErr == nil {
		t.Fatal("the command ran before the guard refused it: marker.txt exists")
	}
}

// The control for the test above. The same shape without the status read has to
// run and have its effect, which proves the previous test observes the guard
// and not a broken harness.
func TestBashRunsTheSamePipelineWithoutTheStatusRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	dir := testsupport.TempDir(t)
	tool := &BashTool{CWD: dir}
	if _, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "touch marker.txt | tail -1",
	}), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "marker.txt")); err != nil {
		t.Fatalf("the harness cannot run a plain pipeline, so the refusal test proves nothing: %v", err)
	}
}

// A `2>&1` joins two file descriptors. It does not start a background job, so
// it must not end the statement. Getting this wrong would split the trap shape
// into pieces and let the commonest form of it through.
func TestSplitShellStatementsKeepsRedirectionAmpersand(t *testing.T) {
	stmts := splitShellStatements("just ci 2>&1 | tail -35")
	if len(stmts) != 1 {
		t.Fatalf("got %d statements, want 1: %+v", len(stmts), stmts)
	}
	if len(stmts[0].stages) != 2 {
		t.Fatalf("got %d stages, want 2: %+v", len(stmts[0].stages), stmts[0].stages)
	}
	if got := commandWord(stmts[0].stages[0]); got != "just" {
		t.Fatalf("first stage command word = %q, want just", got)
	}
}

// A background `&` and an `&&` both do end a statement, so the scanner must
// keep telling them apart from the redirection case above.
func TestSplitShellStatementsSeparators(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want int
	}{
		{"semicolon", "a; b", 2},
		{"and-and", "a && b", 2},
		{"or-or", "a || b", 2},
		{"newline", "a\nb", 2},
		{"background", "a & b", 2},
		{"redirection is not a separator", "a 2>&1", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := len(splitShellStatements(c.cmd)); got != c.want {
				t.Fatalf("splitShellStatements(%q) gave %d statements, want %d", c.cmd, got, c.want)
			}
		})
	}
}
