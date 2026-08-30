//go:build terva_scripting

package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// realToolHost dispatches a script binding to the host's ACTUAL tools,
// where fakeHost returns canned text. The read-dedup lives inside ReadTool,
// so a fake can never exercise it: the bug this file guards was in the
// composition of the two, and only a real ReadTool on the far side of a
// real script binding puts that composition under test.
//
// It mirrors build.scriptHostDispatcher's tail — target.Execute(ctx, …) with
// the dispatch context passed through — minus the approval gate, which is
// tested elsewhere and would only add a confirmer to stand up here.
func realToolHost(reg map[string]core.Tool) hostCallFn {
	return func(ctx context.Context, name string, args json.RawMessage) (core.ToolResult, error) {
		tool, ok := reg[name]
		if !ok {
			return core.ToolResult{}, fmt.Errorf("no such host tool %q", name)
		}
		return tool.Execute(ctx, args, nil)
	}
}

// scriptOverRealRead builds the pairing under test: a code_execution tool
// whose read binding reaches a dedup-enabled ReadTool rooted at dir.
func scriptOverRealRead(dir string) (*CodeExecutionTool, *ReadTool) {
	rt := &ReadTool{CWD: dir, Epoch: &fakeEpoch{}}
	return &CodeExecutionTool{HostCall: realToolHost(map[string]core.Tool{"read": rt})}, rt
}

func runScript(t *testing.T, tool *CodeExecutionTool, script string) string {
	t.Helper()
	args, err := json.Marshal(codeExecArgs{Script: script})
	if err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("script failed: %s", ceResultText(res))
	}
	return ceResultText(res)
}

// TestCodeExecutionReadIsNeverDedupStubbed is the end-to-end guard for F1
// (docs/reviews/2026-08-29-local-model-harness-friction-review.md).
//
// A script that reads the same file twice — the ordinary shape when a
// program iterates on a parse — used to get the file on the first call and
// this on the second:
//
//	./page.html — unchanged since you read it earlier this session; the copy
//	above is still current, so it was not re-sent …
//
// There is no "above" for a script. The sentence arrived as the return value
// of read(), and the program parsed it instead of the file: indexOf("<script>")
// returned -1, and JSON.parse of a stubbed .json file threw "invalid character
// '/' looking for beginning of value".
func TestCodeExecutionReadIsNeverDedupStubbed(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "page.html"),
		[]byte("<html>\n<script>\nlet x = 1;\n</script>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool, _ := scriptOverRealRead(dir)

	out := runScript(t, tool, `
		const first = read("page.html");
		const second = read("page.html");
		print("first:" + (first.indexOf("<script>") >= 0));
		print("second:" + (second.indexOf("<script>") >= 0));
		print("samelen:" + (first.length === second.length));
	`)

	for _, want := range []string{"first:true", "second:true", "samelen:true"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in script output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "unchanged since you read it") {
		t.Errorf("the dedup stub reached the script as data:\n%s", out)
	}
}

// TestCodeExecutionReadAfterModelReadReturnsTheFile is the exact recorded sequence:
// the model reads a file conversationally (seq 105 of the reviewed session),
// then the very next code_execution reads the same path (seq 107) and got 231
// bytes of prose instead of 22,800 bytes of HTML.
func TestCodeExecutionReadAfterModelReadReturnsTheFile(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "page.html"),
		[]byte("<html>\n<script>\nlet x = 1;\n</script>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool, rt := scriptOverRealRead(dir)

	// The model's own read, through the same tool with a plain context.
	// This is the copy the stub would later claim is "still current".
	modelRead := dedupRead(t, rt, map[string]any{"path": "page.html"})
	if isDedup(modelRead) {
		t.Fatal("precondition: the model's first read should be full content")
	}

	out := runScript(t, tool, `
		const html = read("page.html");
		print("hasScript:" + (html.indexOf("<script>") >= 0));
	`)
	if !strings.Contains(out, "hasScript:true") {
		t.Errorf("the script was stubbed against the model's transcript:\n%s", out)
	}
}

// TestCodeExecutionReadDoesNotPrimeTheModelStub guards the other half of the
// exemption. A script's reads never enter the transcript, so they must not
// make a later model-issued read stub against bytes the model never saw —
// which is the same defect with the roles swapped.
func TestCodeExecutionReadDoesNotPrimeTheModelStub(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("only copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool, rt := scriptOverRealRead(dir)

	runScript(t, tool, `read("f.txt"); print("ok");`)

	res := dedupRead(t, rt, map[string]any{"path": "f.txt"})
	if isDedup(res) {
		t.Fatal("the model's FIRST read was stubbed against a script's read")
	}
	if !strings.Contains(bodyText(t, res), "only copy") {
		t.Fatal("the model's first read missing content")
	}
}

// TestCodeExecutionModelReadStillDedups pins the blast radius: the
// exemption is scoped to the script dispatch and does not switch the
// feature off for the model that shares the tool.
func TestCodeExecutionModelReadStillDedups(t *testing.T) {
	dir := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("body here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool, rt := scriptOverRealRead(dir)

	_ = dedupRead(t, rt, map[string]any{"path": "f.txt"})
	if !isDedup(dedupRead(t, rt, map[string]any{"path": "f.txt"})) {
		t.Fatal("the model's re-read should still dedup")
	}
	// A script in between changes nothing about that.
	runScript(t, tool, `read("f.txt"); print("ok");`)
	if !isDedup(dedupRead(t, rt, map[string]any{"path": "f.txt"})) {
		t.Fatal("a script's read should not disturb the model's dedup state")
	}
}
