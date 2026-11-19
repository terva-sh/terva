package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// deepLines are command lines that ended the process in the shell parser, or
// in a walk of its tree, before the bounds. Each one is a line that bash can
// run.
func deepLines() map[string]string {
	return map[string]string{
		"groups inside groups": strings.Repeat("(", 250000) + "true" + strings.Repeat(")", 250000),
		"a long pipe":          strings.Repeat("true | ", 320000) + "true",
		"signs in a sum":       "echo $((" + strings.Repeat("-+", 350000) + "1))",
		"a long chain":         strings.Repeat("true && ", 320000) + "true",
		"a long line":          "echo " + strings.Repeat("a", MaxShellLine),
		// 🚨 The end of a line stands between two signs of one sum, so it
		// does not end a run of them.
		"signs on many lines":      "echo $((" + strings.Repeat(strings.Repeat("-+", 5000)+"\n", 4) + "1))",
		"signs behind backslashes": "echo $((" + strings.Repeat("-+\\\n", 30000) + "1))",
		// 🚨 Each kind of mark within the bound of one kind, and the sum of
		// them past it. A line can put each kind inside the next.
		"each kind inside the next": strings.Repeat("(", 6000) + strings.Repeat("{ ", 6000) + "echo $((" + strings.Repeat("!", 6000) + "1))",
		// A group that no mark closes. The parser is as deep in it as in
		// one that a mark closes, before it finds the error.
		"groups with no end": strings.Repeat("(", 60000) + "true",
	}
}

// 🚨 A line past the bounds is refused at each place that parses a command,
// and nothing parses it. Without the bounds this test ends the test process
// with a stack overflow, which no recover stops.
func TestALinePastTheBoundsIsRefusedAndNotParsed(t *testing.T) {
	dir := testsupport.TempDir(t)
	mark := filepath.Join(dir, "ran")
	sb := NewSandbox(dir)
	tool := &BashTool{CWD: dir, Sandbox: sb}
	for name, line := range deepLines() {
		if !ShellPastBounds(line) {
			t.Fatalf("%s: the line is within the bounds", name)
		}
		// The line also holds a command that makes a file, so a run is seen.
		line = "touch " + mark + "; " + line
		if got := DecomposeBashCommand(line); got != nil {
			t.Errorf("%s: DecomposeBashCommand gave %d commands, want none", name, len(got))
		}
		if got := BashGrantScopes(line); got != nil {
			t.Errorf("%s: BashGrantScopes gave %d grants, want none", name, len(got))
		}
		if err := sb.CheckCommand(line); err == nil || !strings.Contains(err.Error(), "this long or this deep") {
			t.Errorf("%s: the sandbox gave %v, want a refusal that names the bounds", name, err)
		}
		args, err := json.Marshal(map[string]string{"command": line})
		if err != nil {
			t.Fatal(err)
		}
		for what, tool := range map[string]*BashTool{"with a sandbox": tool, "with no sandbox": {CWD: dir}} {
			if _, err := tool.Execute(context.Background(), args, nil); err == nil || !strings.Contains(err.Error(), "this long or this deep") {
				t.Errorf("%s: the bash tool %s gave %v, want a refusal that names the bounds", name, what, err)
			}
		}
		if _, err := os.Stat(mark); err == nil {
			t.Fatalf("%s: the command ran", name)
		}
	}
}

// The bounds are not the limit of the parser. A line at the bound of each
// kind is parsed, and the process lives.
func TestALineAtTheBoundsIsParsed(t *testing.T) {
	// deep makes a line with as many levels as both bounds let pass: open
	// for each level, then mid, then shut for each level. Each open holds
	// one mark at most, and four marks are kept for head and mid.
	deep := func(head, open, mid, shut, tail string) string {
		levels := min(maxShellMarks-4, (MaxShellLine-len(head)-len(mid)-len(tail))/(len(open)+len(shut)))
		return head + strings.Repeat(open, levels) + mid + strings.Repeat(shut, levels) + tail
	}
	for name, line := range map[string]string{
		"groups inside groups":     deep("", "(", "true", ")", ""),
		"groups with no end":       deep("", "(", "true", "", ""),
		"substitutions":            deep("echo ", "$(", "true", ")", ""),
		"an index inside an index": deep("echo ${a", "[a", "", "]", "}"),
		"an index with no end":     deep("echo ${a", "[a", "", "", ""),
		"brace groups":             deep("", "{ ", "true", "", ""),
		"a long pipe":              deep("", "a|", "true", "", ""),
		"a long chain":             deep("", "a&", "true", "", ""),
		"negations in a sum":       deep("echo $((", "!", "1", "", "))"),
		"groups in a sum":          deep("echo $((", "(", "1", ")", "))"),
		"signs in a sum":           deep("echo $((", "-", "1", "", "))"),
		"signs on many lines":      deep("echo $((", "-\n", "1", "", "))"),
		// The levels that need no mark, as deep as the length lets them be.
		"values of values in a sum": deep("echo $((", "a=", "1", "", "))"),
		"powers in a sum":           deep("echo $((", "2**", "2", "", "))"),
		"choices in a sum":          deep("echo $((", "1?", "1", ":1", "))"),
		"a long sum":                deep("echo $((", "1+", "1", "", "))"),
		"time inside time":          deep("", "time ", "true", "", ""),
		"an if inside an if":        deep("", "if ", "true", "", ""),
		"a loop inside a loop":      deep("", "while ", "true", "", ""),
		"a long list":               deep("", "true;", "true", "", ""),
		// Each kind inside the next, with the sum of the marks at the bound.
		"each kind inside the next": strings.Repeat("(", maxShellMarks/4) + strings.Repeat("{ ", maxShellMarks/4) +
			"echo ${a" + strings.Repeat("[a", maxShellMarks/4) + "[$((" + strings.Repeat("!", maxShellMarks/4-4) + "1",
	} {
		if ShellPastBounds(line) {
			t.Errorf("%s: the line is past the bounds", name)
			continue
		}
		DecomposeBashCommand(line)
		BashGrantScopes(line)
	}
	levels := maxShellMarks - 4
	if got := DecomposeBashCommand(strings.Repeat("a|", levels) + "true"); len(got) != levels+1 {
		t.Errorf("a pipe at the bound gave %d commands, want %d", len(got), levels+1)
	}
	// A daily line is far inside them.
	if ShellPastBounds("git status && go test ./... | tail -n 5") {
		t.Error("a daily line is past the bounds")
	}
}

// 🚨 A line of commands, each one inside the last, is within the bounds of
// the parser, and its commands hold each other's text. Without the bound on
// that text the split of 128 KiB makes 1 GiB of text and takes half a minute.
func TestALineOfCommandsInsideCommandsIsRefused(t *testing.T) {
	dir := testsupport.TempDir(t)
	sb := NewSandbox(dir)
	levels := (MaxShellLine - 8) / len("echo $()")
	for name, line := range map[string]string{
		"a command in each substitution":    strings.Repeat("echo $(", levels) + "true" + strings.Repeat(")", levels),
		"a substitution as each command":    "echo " + strings.Repeat("$(", maxShellMarks-4) + "true" + strings.Repeat(")", maxShellMarks-4),
		"a command in each redirect's file": strings.Repeat("cat <(", levels) + "true" + strings.Repeat(")", levels),
	} {
		if ShellPastBounds(line) {
			t.Fatalf("%s: the line is past the bounds of the parser, so this test does not reach the split", name)
		}
		start := time.Now()
		if !ShellUnreadable(line) {
			t.Errorf("%s: terva splits the line", name)
		}
		if got := DecomposeBashCommand(line); got != nil {
			t.Errorf("%s: DecomposeBashCommand gave %d commands, want none", name, len(got))
		}
		if err := sb.CheckCommand(line); err == nil || !strings.Contains(err.Error(), "this long or this deep") {
			t.Errorf("%s: the sandbox gave %v, want a refusal that names the bounds", name, err)
		}
		args, err := json.Marshal(map[string]string{"command": line})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (&BashTool{CWD: dir}).Execute(context.Background(), args, nil); err == nil || !strings.Contains(err.Error(), "this long or this deep") {
			t.Errorf("%s: the bash tool gave %v, want a refusal that names the bounds", name, err)
		}
		if took := time.Since(start); took > 20*time.Second {
			t.Errorf("%s: the refusals took %s", name, took)
		}
	}
	// A daily line with a command inside a command is split, and each
	// command is one that a rule can read.
	daily := "echo $(cat $(ls)) && git log $(git merge-base HEAD main)..HEAD"
	if ShellUnreadable(daily) {
		t.Fatal("terva does not split a daily line")
	}
	want := []string{"echo $(cat $(ls))", "cat $(ls)", "ls", "git log $(git merge-base HEAD main)..HEAD", "git merge-base HEAD main"}
	if got := DecomposeBashCommand(daily); !slices.Equal(got, want) {
		t.Errorf("the daily line gave %q, want %q", got, want)
	}
}
