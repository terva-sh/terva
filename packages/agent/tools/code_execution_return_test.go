//go:build terva_scripting

package tools

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

// execScript runs a script through the real read-only tool and hands back the
// result WITHOUT failing on IsError, because several of these tests are about
// what the failure says.
func execScript(t *testing.T, script string) core.ToolResult {
	t.Helper()
	tool := &CodeExecutionTool{HostCall: (&fakeHost{text: map[string]string{}}).call}
	res, err := tool.Execute(context.Background(), scriptArgs(t, script), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

// The recorded failure, twice over: seq 164 and seq 328 both wrote the natural
// early exit and both got "SyntaxError: Illegal return statement". Both times
// the model spent the next turn wrapping its own program in an IIFE.
func TestCodeExecutionTopLevelReturnParses(t *testing.T) {
	res := execScript(t, `
		const m = null;
		if (!m) { print('NO_SCRIPT'); return; }
		print('unreachable');
	`)
	if res.IsError {
		t.Fatalf("a top-level return must not be a parse error: %s", ceResultText(res))
	}
	out := ceResultText(res)
	if !strings.Contains(out, "NO_SCRIPT") {
		t.Errorf("the script should have run and printed: %s", out)
	}
	if strings.Contains(out, "Illegal return statement") {
		t.Errorf("the recorded SyntaxError came back: %s", out)
	}
}

// return must be a real early exit, not merely tolerated.
func TestCodeExecutionTopLevelReturnStopsEarly(t *testing.T) {
	out := ceResultText(execScript(t, `
		print('before');
		return;
		print('after');
	`))
	if !strings.Contains(out, "before") {
		t.Errorf("work before the return should still run: %s", out)
	}
	if strings.Contains(out, "after") {
		t.Errorf("return did not stop the program: %s", out)
	}
}

// The wrap sits ON line 1 so a diagnostic still names the line the model
// wrote. A "\n" after the prefix would shift every reported line by one and
// trade one bad error for a subtler one.
func TestCodeExecutionSyntaxErrorKeepsTheLineNumber(t *testing.T) {
	// Line 1 is empty (the literal opens with a newline), so the bad token
	// below sits on line 3.
	res := execScript(t, "\nprint('a');\nthis is not javascript\n")
	if !res.IsError {
		t.Fatal("expected a parse failure")
	}
	out := ceResultText(res)
	if !strings.Contains(out, "Line 3") {
		t.Errorf("the wrap shifted the reported line; want Line 3 in: %s", out)
	}
}

// A script whose last line ends in a line comment must not swallow the closing
// "})()" — which is why the suffix starts with a newline.
func TestCodeExecutionTrailingLineCommentStillRuns(t *testing.T) {
	res := execScript(t, "print('done') // trailing comment")
	if res.IsError {
		t.Fatalf("a trailing line comment broke the wrap: %s", ceResultText(res))
	}
	if !strings.Contains(ceResultText(res), "done") {
		t.Errorf("script did not run: %s", ceResultText(res))
	}
}

// The wrap must not buy convenience with authority. The binding pre-check
// feeds the approval prompt, so a script it cannot account for still has to be
// refused after the change.
func TestCodeExecutionWrapKeepsTheBindingAccount(t *testing.T) {
	res := execScript(t, `
		const n = 're' + 'ad';
		globalThis[n]('x');
	`)
	if !res.IsError {
		t.Fatal("an unaccountable script must still be refused")
	}
	if !strings.Contains(ceResultText(res), "globalThis") {
		t.Errorf("the refusal should still name what defeated the account: %s", ceResultText(res))
	}
}

// The mutating twin shares the wrapper, and its own pre-check runs first, so
// prove the shape reaches it too.
func TestCodeExecutionMutatingTopLevelReturnParses(t *testing.T) {
	host := &fakeHost{text: map[string]string{}}
	res, _ := execMutating(t, host, `
		const todo = [];
		if (todo.length === 0) { print('NOTHING_TO_DO'); return; }
		write('x', 'y');
	`)
	if res.IsError {
		t.Fatalf("a top-level return must not be a parse error: %s", ceResultText(res))
	}
	if !strings.Contains(ceResultText(res), "NOTHING_TO_DO") {
		t.Errorf("the script should have run and printed: %s", ceResultText(res))
	}
}

// The two details in the wrapper string that the tests above depend on.
func TestCodeExecutionProgramWrapShape(t *testing.T) {
	got := scriptProgram("print(1)")
	if !strings.HasPrefix(got, "(function(){print(1)") {
		t.Errorf("the prefix must stay on line 1 with the script: %q", got)
	}
	if !strings.HasSuffix(got, "\n})()") {
		t.Errorf("the suffix must start with a newline: %q", got)
	}
	if strings.HasPrefix(got, "(function(){\n") {
		t.Errorf("a newline after the prefix would shift every reported line: %q", got)
	}
}
