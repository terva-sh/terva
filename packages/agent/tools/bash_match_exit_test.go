package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The rule that decides whether an exit 1 is an answer or a failure. Kept as a
// pure predicate because two of its four guards are unreachable end to end: a
// command killed by a timeout or a cancellation does not exit 1, so the only
// way to prove those guards hold is to ask them directly.
//
// See docs/decisions/0011-match-exit-is-not-a-tool-failure.md.
func TestMatchExitBenignBoundaries(t *testing.T) {
	const hint = "[hint] a pipeline's exit status is its LAST command's"

	cases := []struct {
		name     string
		hint     string
		output   string
		canceled bool
		timedOut bool
		want     bool
	}{
		{"found nothing, said so", hint, "0\n", false, false, true},
		// The hint is the whole gate on exit code and command name. No hint
		// means either a code other than 1, or a final stage that does real
		// work — a compile, a test run, anything whose 1 means what it says.
		{"no hint, output anyway", "", "building...\n", false, false, false},
		// Output is the evidence that the run got somewhere. Without it there
		// is nothing for the model to read instead of the exit code, so the
		// error flag is still the most honest thing we can say.
		{"hint, but nothing to show", hint, "", false, false, false},
		// Output here is the whole command's, not the last stage's, so a
		// stray blank line from an earlier statement could otherwise pass for
		// a result worth reading.
		{"hint, but only whitespace", hint, "\n  \t\n", false, false, false},
		// A killed process did not render a verdict. Its exit code belongs to
		// the harness, and softening it would hide the kill.
		{"canceled mid-run", hint, "0\n", true, false, false},
		{"timed out mid-run", hint, "0\n", false, true, false},
		{"canceled and timed out", hint, "0\n", true, true, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := matchExitBenign(c.hint, c.output, c.canceled, c.timedOut)
			if got != c.want {
				t.Fatalf("matchExitBenign(hint=%t, output=%q, canceled=%t, timedOut=%t) = %t, want %t",
					c.hint != "", c.output, c.canceled, c.timedOut, got, c.want)
			}
		})
	}
}

// End to end on the canonical shape: the probe succeeded, grep counted zero,
// and the pipeline reported grep's 1. Nothing is hidden — the raw exit code
// still reaches both the body and Details — but the result is not an error.
func TestBashMatchExitFoundNothingIsNotAnError(t *testing.T) {
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
	if res.IsError {
		t.Fatal("a green probe whose grep matched nothing was flagged as an error")
	}
	// Suppressing the flag must not suppress the fact. A reader that wants the
	// real status still has it, in both channels.
	if got := res.Content[0].(provider.TextBlock).Text; !strings.Contains(got, "[exit 1]") {
		t.Fatalf("the real exit code vanished from the body:\n%s", got)
	}
	if got := res.Details.(map[string]any)["exit_code"]; got != 1 {
		t.Fatalf("Details[exit_code] = %v, want 1 — the flag softens, the record must not", got)
	}
}

// The control that keeps the rule honest: output alone must never soften a
// result. Here the command produced plenty of it and still failed for real,
// because the last stage is not a match-style command.
func TestBashRealFailureWithOutputStaysAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	tool := &BashTool{CWD: testsupport.TempDir(t)}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "echo building...; exit 1",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a genuine exit 1 was softened because the command happened to print something")
	}
}

// A match-style command that found nothing AND printed nothing stays an error.
// There is no output to read instead of the exit code, so the flag is still the
// most informative thing the result can carry.
func TestBashSilentMatchExitStaysAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	tool := &BashTool{CWD: testsupport.TempDir(t)}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "echo all-good | grep FAILED",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("a silent no-match run was softened; with no output there is nothing to read instead")
	}
}

// The safety argument the whole decision rests on: grep and its relatives
// reserve exit 1 for "no match" and 2 for trouble. A genuine grep error must
// therefore never reach the softening branch.
func TestBashGrepErrorStaysAnError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shell only")
	}
	tool := &BashTool{CWD: testsupport.TempDir(t)}
	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"command": "grep pattern /nonexistent/path/for-a-test",
	}), nil)
	if err != nil {
		t.Fatal(err)
	}
	body := res.Content[0].(provider.TextBlock).Text
	if !res.IsError {
		t.Fatalf("a real grep failure was softened; exit-2-means-trouble is the whole safety argument:\n%s", body)
	}
	// Pin the premise too. If a platform ever returned 1 here, the assertion
	// above would still pass for the wrong reason, and the softening rule would
	// quietly start covering real errors.
	if got := res.Details.(map[string]any)["exit_code"]; got == 1 {
		t.Fatalf("grep reported a missing file as exit 1, not 2 — the narrowness argument does not hold on this platform:\n%s", body)
	}
}
