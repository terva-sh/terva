//go:build terva_scripting

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

func execMutating(t *testing.T, host *fakeHost, script string) (core.ToolResult, *fakeHost) {
	t.Helper()
	tool := &CodeExecutionMutatingTool{HostCall: host.call}
	args, err := json.Marshal(map[string]any{"script": script})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	res, err := tool.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res, host
}

// ToolResult.Details is `any`, so a test that reads it must say what it
// expects to find.
func mutDetails(t *testing.T, res core.ToolResult) map[string]any {
	t.Helper()
	d, ok := res.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details = %#v, want a map", res.Details)
	}
	return d
}

func scriptArgs(t *testing.T, script string) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"script": script})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return raw
}

// The accounted plan is the one thing an approval prompt for this tool should
// lead with. Preview computes it without running, and declines to the generic
// BuildPreview when the analysis cannot run.
func TestScriptToolPreviewShowsTheAccountedPlan(t *testing.T) {
	cases := []struct {
		name string
		tool interface {
			Preview(json.RawMessage, int) string
		}
		script string
		want   string
	}{
		{"mutating plan", &CodeExecutionMutatingTool{}, `read("a"); read("b"); write("c","d")`, "accounted for: read x2, write x1"},
		{"mutating edit", &CodeExecutionMutatingTool{}, `edit("f",[{oldText:"a",newText:"b"}])`, "accounted for: edit x1"},
		{"mutating none", &CodeExecutionMutatingTool{}, `print("x")`, "accounted for: no host calls"},
		{"read-only plan", &CodeExecutionTool{}, `read("a"); grep("p"); glob("g")`, "accounted for: read x1, grep x1, glob x1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.tool.Preview(scriptArgs(t, c.script), 120); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// An unaccountable script is exactly when the approver most needs to know, so
// the preview names what defeated the account rather than looking clean. Each
// tool's script mentions a name in its OWN binding set — a name outside the
// set is no different from an arbitrary global like Math, which the walker
// rightly ignores.
func TestScriptToolPreviewFlagsTheUnaccountable(t *testing.T) {
	cases := []struct {
		name string
		tool interface {
			Preview(json.RawMessage, int) string
		}
		script string
	}{
		{"mutating", &CodeExecutionMutatingTool{}, `const f = write; f("a","b")`},
		{"read-only", &CodeExecutionTool{}, `const f = read; f("a")`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.tool.Preview(scriptArgs(t, c.script), 120)
			if !strings.HasPrefix(got, "unaccountable (") {
				t.Errorf("got %q, want an unaccountable flag", got)
			}
		})
	}
}

// A script that does not parse declines to the generic preview rather than
// inventing an account.
func TestScriptToolPreviewFallsBackOnBadInput(t *testing.T) {
	tool := &CodeExecutionMutatingTool{}
	if got := tool.Preview(scriptArgs(t, `function {`), 120); got != "" {
		t.Errorf("unparseable script: got %q, want fallback", got)
	}
	if got := tool.Preview(json.RawMessage(`{not json`), 120); got != "" {
		t.Errorf("unparseable args: got %q, want fallback", got)
	}
}

func TestMutatingToolWritesAndEditsThroughTheGate(t *testing.T) {
	host := &fakeHost{text: map[string]string{
		"read":  "old body",
		"write": "wrote 8 bytes",
		"edit":  "applied 1 edit",
	}}
	res, host := execMutating(t, host, `
		const body = read("a.txt");
		write("b.txt", body + "!");
		edit("c.txt", [{oldText: "x", newText: "y", replaceAll: true}]);
		print("done");
	`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", ceResultText(res))
	}
	if got := ceResultText(res); !strings.Contains(got, "done") {
		t.Fatalf("output = %q", got)
	}
	if len(host.calls) != 3 {
		t.Fatalf("host calls = %#v, want 3", host.calls)
	}
	// Every binding call crossed as a real host tool call with the args
	// that tool's schema expects.
	if !strings.HasPrefix(host.calls[0], `read {"path":"a.txt"}`) {
		t.Fatalf("read call = %q", host.calls[0])
	}
	if !strings.Contains(host.calls[1], `"path":"b.txt"`) || !strings.Contains(host.calls[1], `"content":"old body!"`) {
		t.Fatalf("write call = %q", host.calls[1])
	}
	// The edits array survived as structure, which is the whole reason
	// this binding is typed rather than string-shaped.
	if !strings.Contains(host.calls[2], `"oldText":"x"`) ||
		!strings.Contains(host.calls[2], `"newText":"y"`) ||
		!strings.Contains(host.calls[2], `"replaceAll":true`) {
		t.Fatalf("edit call = %q", host.calls[2])
	}
}

// The load-bearing property: an unaccountable script is refused BEFORE any
// binding runs, so nothing reaches disk. A refusal after the first write
// would be no protection at all.
func TestMutatingToolRefusesUnaccountableScriptBeforeAnyCall(t *testing.T) {
	cases := []struct{ name, script, want string }{
		{"aliased binding", `const w = write; w("a.txt", "x")`, "referenced without being called"},
		{"eval", `write("a.txt", "x"); eval("1+1")`, "reaches the global object"},
		{"global reach", `globalThis["wr" + "ite"]("a.txt", "x")`, "reaches the global object"},
		{"shadowed", `const write = 1; print(write)`, "redeclared"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := &fakeHost{text: map[string]string{}}
			res, host := execMutating(t, host, tc.script)
			if !res.IsError {
				t.Fatal("want a refusal")
			}
			if len(host.calls) != 0 {
				t.Fatalf("host calls = %#v, want none — the script must not run at all", host.calls)
			}
			body := ceResultText(res)
			if !strings.Contains(body, "was not run") {
				t.Fatalf("message = %q, want a refusal", body)
			}
			if !strings.Contains(body, tc.want) {
				t.Fatalf("message = %q, want the reason %q", body, tc.want)
			}
			if acc, ok := mutDetails(t, res)["accounted"].(bool); !ok || acc {
				t.Fatalf("details.accounted = %v, want false", mutDetails(t, res)["accounted"])
			}
		})
	}
}

func TestMutatingToolReportsTheBindingPlan(t *testing.T) {
	host := &fakeHost{text: map[string]string{"read": "x", "write": "ok"}}
	res, _ := execMutating(t, host, `read("a"); read("b"); write("c", "d"); print("ok")`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", ceResultText(res))
	}
	details := mutDetails(t, res)
	if got := details["binding_plan"]; got != "read x2, write x1" {
		t.Fatalf("binding_plan = %v, want %q", got, "read x2, write x1")
	}
	if acc, ok := details["accounted"].(bool); !ok || !acc {
		t.Fatalf("details.accounted = %v, want true", details["accounted"])
	}
}

// bash is not in the binding set, and must stay out: a command string is
// exactly the authority the pre-check cannot read.
func TestMutatingToolHasNoBashBinding(t *testing.T) {
	tool := &CodeExecutionMutatingTool{HostCall: (&fakeHost{}).call}
	if _, found := tool.bindings()["bash"]; found {
		t.Fatal("bash is a string binding — it must not be in the set")
	}
	if _, found := tool.typedBindings()["bash"]; found {
		t.Fatal("bash is a typed binding — it must not be in the set")
	}
	for _, name := range mutatingScriptBindings {
		if name == "bash" {
			t.Fatal("bash is in the accounted binding list")
		}
	}
	host := &fakeHost{text: map[string]string{}}
	res, host := execMutating(t, host, `bash("ls")`)
	if !res.IsError {
		t.Fatal("want an error: bash is not defined")
	}
	if len(host.calls) != 0 {
		t.Fatalf("host calls = %#v, want none", host.calls)
	}
	if got := ceResultText(res); !strings.Contains(got, "bash is not defined") {
		t.Fatalf("message = %q, want a ReferenceError for bash", got)
	}
}

func TestMutatingToolEditValidationIsCatchable(t *testing.T) {
	cases := []struct{ name, script, want string }{
		{"not an array", `try { edit("a", "nope") } catch (e) { print(e.message) }`, "must be an array"},
		{"empty array", `try { edit("a", []) } catch (e) { print(e.message) }`, "at least one edit"},
		{"entry not an object", `try { edit("a", [1]) } catch (e) { print(e.message) }`, "entry 1 is not an object"},
		{"missing newText", `try { edit("a", [{oldText: "x"}]) } catch (e) { print(e.message) }`, "entry 1 needs newText"},
		{"bad replaceAll", `try { edit("a", [{oldText: "x", newText: "y", replaceAll: "yes"}]) } catch (e) { print(e.message) }`, "replaceAll as true or false"},
		{"missing content", `try { write("a") } catch (e) { print(e.message) }`, "needs content"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := &fakeHost{text: map[string]string{}}
			res, host := execMutating(t, host, tc.script)
			if res.IsError {
				t.Fatalf("the error should be catchable in-script: %s", ceResultText(res))
			}
			if got := ceResultText(res); !strings.Contains(got, tc.want) {
				t.Fatalf("message = %q, want %q", got, tc.want)
			}
			// A malformed call never reaches the host tool.
			if len(host.calls) != 0 {
				t.Fatalf("host calls = %#v, want none", host.calls)
			}
		})
	}
}

// A failed run may already have changed files. The model must be told, or
// it will assume a failure means nothing happened.
func TestMutatingToolFailureWarnsThatChangesMayBeOnDisk(t *testing.T) {
	host := &fakeHost{text: map[string]string{"write": "ok"}}
	res, _ := execMutating(t, host, `write("a.txt", "x"); throw new Error("boom")`)
	if !res.IsError {
		t.Fatal("want an error")
	}
	got := ceResultText(res)
	if !strings.Contains(got, "boom") {
		t.Fatalf("message = %q, want the script error", got)
	}
	if !strings.Contains(got, "may already be on disk") {
		t.Fatalf("message = %q, want the partial-change warning", got)
	}
}

func TestMutatingToolUnwiredFailsClosed(t *testing.T) {
	tool := &CodeExecutionMutatingTool{}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"script":"print(1)"}`), nil); err == nil {
		t.Fatal("want a failure when the approval gate is not wired")
	}
}

// The mutating tool must NOT share code_execution's group: activating
// read-only scripting would otherwise hand over a tool that writes files.
func TestMutatingToolNameAndOwnGroup(t *testing.T) {
	tool := &CodeExecutionMutatingTool{}
	if got := tool.Name(); got != "code_execution_mutating" {
		t.Fatalf("name = %q", got)
	}
	readOnly := (&CodeExecutionTool{}).ToolGroupName()
	if got := tool.ToolGroupName(); got == readOnly {
		t.Fatalf("group = %q, must differ from code_execution's %q", got, readOnly)
	}
}
