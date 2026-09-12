package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// writeCacheDiagnosticFixture lays down a transcript carrying the two rows that
// diagnose a cache collapse, in the on-disk shape core writes them: a "prefix"
// row from AppendPrefixDivergence and the open/close pair from AppendCacheCliff.
//
// Raw JSONL rather than a real Session, because the point is to pin the READER
// against the bytes a released terva already wrote. A fixture built through the
// writer would pass even if both sides drifted together, which is exactly the
// failure this is here to catch.
func writeCacheDiagnosticFixture(t *testing.T, path, cwd string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	cwdJSON, err := json.Marshal(cwd)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"meta","meta":{"id":"x","cwd":%s,"format_version":2}}`+"\n", cwdJSON)
	b.WriteString(`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"go"}]}}` + "\n")
	b.WriteString(`{"type":"prefix","prefix":{"rung":12,"label":"message 12","messages":340,"prev_messages":339,"cached_tokens":120000}}` + "\n")
	b.WriteString(`{"type":"cliff","cliff":{"ongoing":true,"dispatches":2,"reread_tokens":240000}}` + "\n")
	b.WriteString(`{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}` + "\n")
	b.WriteString(`{"type":"cliff","cliff":{"dispatches":18,"reread_tokens":1900000}}` + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The third limit the ticket names, and the one that survives fixing the other
// two: terva records its own cache behaviour and then had no route back to the
// measurement. recordPrefix and recordCliff wrote "prefix" and "cliff" rows
// that no reader accepted, so they were write-only, and the evidence that named
// activate_tools as the floor-stepper came from exactly these rows.
func TestSessionInspectReadsCacheDiagnosticRows(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	transcript := filepath.Join(testsupport.TempDir(t), "cache.jsonl")
	writeCacheDiagnosticFixture(t, transcript, cwd)

	tool := &SessionInspectTool{TervaHome: home, CWD: cwd, Sandbox: NewSandbox(cwd)}
	res := inspectByPath(t, tool, `{"path":`+jsonStr(transcript)+`,"event_kinds":["prefix","cliff"]}`)
	if res.IsError {
		t.Fatalf("the cache rows must be readable, got: %q", inspectText(t, res))
	}
	got := inspectText(t, res)

	// The rung is the field worth surfacing: it says WHERE the prefix broke,
	// and the two message counts give that break its scale.
	for _, want := range []string{"rung 12", "message 12", "340 messages", "339 before", "120000"} {
		if !strings.Contains(got, want) {
			t.Errorf("prefix row should report %q, got: %q", want, got)
		}
	}
	// Both halves of the run: the row that opens it and the row that closes it
	// with the totals it reached.
	for _, want := range []string{"opened", "2 dispatches", "closed", "18 dispatches", "1900000"} {
		if !strings.Contains(got, want) {
			t.Errorf("cliff rows should report %q, got: %q", want, got)
		}
	}
	// Three rows matched, and the conversation messages did not.
	details, _ := res.Details.(map[string]any)
	if total, _ := details["total"].(int); total != 3 {
		t.Errorf("total = %v, want the 3 cache rows only (the filter must exclude messages)", details["total"])
	}
	if strings.Contains(got, "done") {
		t.Errorf("event_kinds should have excluded the messages, got: %q", got)
	}
}

// The kinds are filterable like every other kind, and they do not leak into an
// unfiltered listing as some other kind. A reader asking for messages must not
// receive a cache row wearing the message label.
func TestSessionInspectCacheRowsCarryTheirOwnKind(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	transcript := filepath.Join(testsupport.TempDir(t), "cache.jsonl")
	writeCacheDiagnosticFixture(t, transcript, cwd)

	tool := &SessionInspectTool{TervaHome: home, CWD: cwd, Sandbox: NewSandbox(cwd)}

	res := inspectByPath(t, tool, `{"path":`+jsonStr(transcript)+`,"event_kinds":["message"]}`)
	if got := inspectText(t, res); strings.Contains(got, "rung") || strings.Contains(got, "collapse run") {
		t.Errorf("a message filter must not return cache rows, got: %q", got)
	}

	// Unfiltered, every row is present and each is labelled for what it is.
	res = inspectByPath(t, tool, `{"path":`+jsonStr(transcript)+`}`)
	got := inspectText(t, res)
	for _, want := range []string{"prefix", "cliff", "message"} {
		if !strings.Contains(got, want) {
			t.Errorf("an unfiltered listing should label a %s row, got: %q", want, got)
		}
	}
}
