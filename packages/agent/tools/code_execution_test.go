//go:build terva_scripting

package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// fakeHost records host calls and returns canned text per tool.
type fakeHost struct {
	calls []string // "tool json-args"
	text  map[string]string
	fail  bool
}

func (f *fakeHost) call(_ context.Context, tool string, args json.RawMessage) (core.ToolResult, error) {
	f.calls = append(f.calls, tool+" "+string(args))
	if f.fail {
		return core.ToolResult{
			Content: []provider.Content{provider.TextBlock{Text: "denied by test"}},
			IsError: true,
		}, nil
	}
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: f.text[tool]}},
	}, nil
}

func execTool(t *testing.T, host *fakeHost, args string) core.ToolResult {
	t.Helper()
	tool := &CodeExecutionTool{HostCall: host.call}
	res, err := tool.Execute(context.Background(), json.RawMessage(args), nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return res
}

func ceResultText(res core.ToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}

func TestCodeExecutionRunsScriptOverHostTools(t *testing.T) {
	host := &fakeHost{text: map[string]string{
		"glob": "a.go\nb.go\n",
		"grep": "a.go:1:match\n",
	}}
	res := execTool(t, host, `{"script":"const files = glob(\"*.go\").trim().split(\"\\n\"); const hits = grep(\"match\", files[0]); print(files.length, \"files; first hit:\", hits.trim())"}`)
	if res.IsError {
		t.Fatalf("unexpected error result: %s", ceResultText(res))
	}
	if want := "2 files; first hit: a.go:1:match\n"; ceResultText(res) != want {
		t.Fatalf("output = %q, want %q", ceResultText(res), want)
	}
	if len(host.calls) != 2 {
		t.Fatalf("host calls = %v, want glob then grep", host.calls)
	}
	if !strings.HasPrefix(host.calls[0], `glob {"pattern":"*.go"}`) {
		t.Errorf("glob args: %s", host.calls[0])
	}
	if !strings.Contains(host.calls[1], `"pattern":"match"`) || !strings.Contains(host.calls[1], `"path":"a.go"`) {
		t.Errorf("grep args: %s", host.calls[1])
	}
}

func TestCodeExecutionReadArgsMapPositionally(t *testing.T) {
	host := &fakeHost{text: map[string]string{"read": "line"}}
	execTool(t, host, `{"script":"print(read(\"f.txt\", 10, 5))"}`)
	if len(host.calls) != 1 {
		t.Fatalf("calls = %v", host.calls)
	}
	for _, want := range []string{`"path":"f.txt"`, `"offset":10`, `"limit":5`} {
		if !strings.Contains(host.calls[0], want) {
			t.Errorf("read args %s missing %s", host.calls[0], want)
		}
	}
}

func TestCodeExecutionHostErrorIsCatchable(t *testing.T) {
	host := &fakeHost{fail: true}
	res := execTool(t, host, `{"script":"try { read(\"x\") } catch (e) { print(\"blocked:\", String(e).includes(\"denied by test\")) }"}`)
	if res.IsError {
		t.Fatalf("caught error should not fail the run: %s", ceResultText(res))
	}
	if want := "blocked: true\n"; ceResultText(res) != want {
		t.Fatalf("output = %q, want %q", ceResultText(res), want)
	}
}

func TestCodeExecutionUnwiredFailsClosed(t *testing.T) {
	tool := &CodeExecutionTool{}
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"script":"print(1)"}`), nil)
	if err == nil || !strings.Contains(err.Error(), "not wired") {
		t.Fatalf("err = %v, want fail-closed not-wired error", err)
	}
}

func TestCodeExecutionNothingPrinted(t *testing.T) {
	host := &fakeHost{}
	res := execTool(t, host, `{"script":"const x = 1 + 1"}`)
	if !strings.Contains(ceResultText(res), "printed nothing") {
		t.Fatalf("output = %q, want printed-nothing hint", ceResultText(res))
	}
}

func TestCodeExecutionScriptErrorSurfacesPartialOutput(t *testing.T) {
	host := &fakeHost{}
	res := execTool(t, host, `{"script":"print(\"before\"); nosuchfn()"}`)
	if !res.IsError {
		t.Fatal("expected an error result")
	}
	txt := ceResultText(res)
	if !strings.Contains(txt, "before") {
		t.Fatalf("partial output lost: %q", txt)
	}
}

func TestCodeExecutionGroupAndName(t *testing.T) {
	tool := &CodeExecutionTool{}
	if tool.Name() != "code_execution" {
		t.Fatalf("name = %q", tool.Name())
	}
	if g := core.ToolGroup(tool); g != "scripting" {
		t.Fatalf("group = %q, want scripting", g)
	}
}
