package build

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

var updateLedgerGolden = flag.Bool("update-ledger-golden", false, "rewrite testdata/ledger.golden from the current code")

// The executed-actions ledger terva writes for a transcript of shell and file
// calls, pinned byte for byte through terva's real tool registry: the `cd <cwd>`
// and `set +e` preamble elided, a `cd` elsewhere kept, a failed pipeline and a
// failed edit each with their own note, an unresolved call, and a read-only call
// left out. Captured before each tool rendered its own entry (TKT-01M35WJZK).
func TestTervasLedgerIsUnchanged(t *testing.T) {
	home, cwd := testsupport.TempDir(t), testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	t.Setenv("OPENAI_API_KEY", "test-key")
	r, err := Resolve(Args{CWD: cwd, Provider: "openai", Model: "gpt-5"}, true)
	if err != nil {
		t.Fatal(err)
	}
	ag := r.NewAgent(core.AllowAll)
	c := &summaryClient{}
	ag.SetClientAndModel(c, "", ag.Model())
	call := func(id, name, args string) provider.Message {
		return provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.ToolCallBlock{ID: id, Name: name, Arguments: json.RawMessage(args)}}}
	}
	result := func(id string, isErr bool) provider.Message {
		return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.ToolResultBlock{CallID: id, IsError: isErr, Content: []provider.Content{provider.TextBlock{Text: "out"}}}}}
	}
	cmd := func(s string) string { b, _ := json.Marshal(map[string]string{"command": s}); return string(b) }
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "fix the build"}}},
		call("1", "bash", cmd(fmt.Sprintf("cd %s && set +e && go test ./... && echo done", cwd))), result("1", false),
		call("2", "bash", cmd(fmt.Sprintf("set +e; cd '%s'\ngofmt -w a.go && go vet ./...", cwd))), result("2", true),
		call("3", "bash", cmd("cd /elsewhere && make")), result("3", false),
		call("4", "edit", `{"path":"a.go","oldText":"x","newText":"y"}`), result("4", true),
		call("5", "write", `{"path":"b.go","content":"package b"}`),
		call("6", "read", `{"path":"a.go"}`), result("6", false),
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "done"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "thanks"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "sure"}}},
	})
	if _, err := ag.Compact(context.Background(), 2, nil); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	var got string
	for _, cn := range ag.Messages()[0].Content {
		if tb, ok := cn.(provider.TextBlock); ok {
			got = tb.Text
		}
	}
	got = strings.ReplaceAll(got, cwd, "<CWD>")

	golden := filepath.Join("testdata", "ledger.golden")
	if *updateLedgerGolden {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	if got != string(want) {
		t.Fatalf("terva's ledger changed. Diff it against %s; rerun with -update-ledger-golden only if the change is meant:\n%s", golden, got)
	}
}
