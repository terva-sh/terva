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

// writeRichSessionFixture writes a transcript carrying the block kinds the
// extension bridge drops — tool calls with arguments, and tool results — which
// is the whole reason this tool exists.
func writeRichSessionFixture(t *testing.T, path, cwd string, rows ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	q := func(s string) string {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"meta","meta":{"id":"x","cwd":%s,"format_version":2}}`+"\n", q(cwd))
	for _, r := range rows {
		b.WriteString(r + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func textRow(role, text string) string {
	b, _ := json.Marshal(text)
	return fmt.Sprintf(`{"type":"message","message":{"role":%q,"content":[{"type":"text","text":%s}]}}`, role, b)
}

func callRow(id, name, args string) string {
	return fmt.Sprintf(`{"type":"message","message":{"role":"assistant","content":[{"type":"tool_call","id":%q,"name":%q,"arguments":%s}]}}`, id, name, args)
}

func resultRow(id, text string, isErr bool) string {
	b, _ := json.Marshal(text)
	return fmt.Sprintf(`{"type":"message","message":{"role":"tool","content":[{"type":"tool_result","call_id":%q,"is_error":%t,"content":[{"type":"text","text":%s}]}]}}`, id, isErr, b)
}

func runSearch(t *testing.T, home, cwd string, args string) string {
	t.Helper()
	tool := &SessionSearchTool{TervaHome: home, CWD: cwd}
	res, err := tool.Execute(context.Background(), json.RawMessage(args), func(string) {})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return toolResultText(t, res)
}

func toolResultText(t *testing.T, res core.ToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		if tb, ok := c.(interface{ isContent() }); ok {
			_ = tb
		}
	}
	// Content blocks are provider.TextBlock; render via JSON to avoid importing
	// the type assertion dance into every assertion.
	raw, err := json.Marshal(res.Content)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		t.Fatal(err)
	}
	for _, bl := range blocks {
		b.WriteString(bl.Text)
	}
	return b.String()
}

// THE test. A file path that appears ONLY inside a tool call's arguments is
// exactly what the protocol-3 bridge discards, and "which past session touched
// this file" is the central question of cross-session recall. On the session
// that motivated this, 106 distinct paths were reachable only this way.
func TestSessionSearchFindsAPathNamedOnlyInToolArguments(t *testing.T) {
	home, cwd := testsupport.TempDir(t), testsupport.TempDir(t)
	writeRichSessionFixture(t, filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl"), cwd,
		textRow("user", "carry on with the milestone"),
		callRow("c1", "read", `{"path":"internal/sim/engine/candidate.go"}`),
		resultRow("c1", "package engine", false),
	)

	out := runSearch(t, home, cwd, `{"query":"candidate.go"}`)
	if !strings.Contains(out, "candidate.go") {
		t.Fatalf("search missed a path present only in tool_call arguments — the exact gap this tool exists to close.\n%s", out)
	}
	if !strings.Contains(out, "tool_call:read") {
		t.Errorf("hit should name the kind and tool so a follow-up knows where to look:\n%s", out)
	}
	if !strings.Contains(out, "sess1") {
		t.Errorf("hit should name its session id for a session_inspect follow-up:\n%s", out)
	}
}

// Tool RESULT text is the other half the bridge drops — command output and file
// contents, where an error message from a previous session lives.
func TestSessionSearchFindsTextInsideAToolResult(t *testing.T) {
	home, cwd := testsupport.TempDir(t), testsupport.TempDir(t)
	writeRichSessionFixture(t, filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl"), cwd,
		callRow("c1", "bash", `{"command":"go test ./..."}`),
		resultRow("c1", "--- FAIL: TestCandidateOrder (0.00s)", true),
	)

	out := runSearch(t, home, cwd, `{"query":"TestCandidateOrder"}`)
	if !strings.Contains(out, "TestCandidateOrder") {
		t.Fatalf("search missed text inside a tool result:\n%s", out)
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("a failed result should be marked so, so the model can tell an error from an echo:\n%s", out)
	}
}

// Secrets must not leak through a snippet. The transcript keeps them; every
// rendering path redacts.
func TestSessionSearchRedactsSnippets(t *testing.T) {
	home, cwd := testsupport.TempDir(t), testsupport.TempDir(t)
	secret := "Authorization: Bearer sk-ant-abcdefgh1234567890"
	writeRichSessionFixture(t, filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl"), cwd,
		callRow("c1", "bash", `{"command":"curl example.test"}`),
		resultRow("c1", "boom "+secret, true),
	)

	out := runSearch(t, home, cwd, `{"query":"boom"}`)
	if strings.Contains(out, "sk-ant-abcdefgh1234567890") {
		t.Fatalf("a snippet leaked a secret:\n%s", out)
	}
}

// Project scoping is structural — there is no path argument — so a session
// belonging to another cwd must be invisible even by exact query.
func TestSessionSearchNeverReadsAnotherProject(t *testing.T) {
	home := testsupport.TempDir(t)
	mine, theirs := testsupport.TempDir(t), testsupport.TempDir(t)
	writeRichSessionFixture(t, filepath.Join(core.SessionsDir(home, mine), "mine.jsonl"), mine,
		textRow("user", "my own distinctive marker"))
	writeRichSessionFixture(t, filepath.Join(core.SessionsDir(home, theirs), "theirs.jsonl"), theirs,
		textRow("user", "someone elses private marker"))

	// The no-match line echoes the query, so asserting on the query text would
	// match the tool's own output. Assert on what a leak would actually show:
	// the other project's session id, and a nonzero match count.
	out := runSearch(t, home, mine, `{"query":"private marker"}`)
	if strings.Contains(out, "theirs") {
		t.Fatalf("search named another project's session:\n%s", out)
	}
	if !strings.Contains(out, "no match") {
		t.Errorf("want an explicit no-match answer, got:\n%s", out)
	}
	if !strings.Contains(out, "across 1 session(s)") {
		t.Errorf("should have scanned only this project's single session, got:\n%s", out)
	}
}

// The kind filter refuses an unknown value rather than matching nothing: a
// silently-empty filter is indistinguishable from "this project never discussed
// it", which is the one answer a search tool must never fake.
func TestSessionSearchRejectsAnUnknownKind(t *testing.T) {
	home, cwd := testsupport.TempDir(t), testsupport.TempDir(t)
	writeRichSessionFixture(t, filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl"), cwd,
		textRow("user", "hello"))

	tool := &SessionSearchTool{TervaHome: home, CWD: cwd}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"query":"hello","kinds":["messages"]}`), func(string) {})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !res.IsError {
		t.Fatal("an unknown kind should be refused, not silently ignored")
	}
	if txt := toolResultText(t, res); !strings.Contains(txt, "message, tool_call, tool_result") {
		t.Errorf("the refusal should name the valid kinds: %s", txt)
	}
}

func TestSessionSearchFiltersByKind(t *testing.T) {
	home, cwd := testsupport.TempDir(t), testsupport.TempDir(t)
	writeRichSessionFixture(t, filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl"), cwd,
		textRow("user", "look at widget.go please"),
		callRow("c1", "read", `{"path":"widget.go"}`),
	)

	if out := runSearch(t, home, cwd, `{"query":"widget.go","kinds":["tool_call"]}`); strings.Contains(out, "message ") {
		t.Errorf("kinds=[tool_call] returned a message event:\n%s", out)
	}
	if out := runSearch(t, home, cwd, `{"query":"widget.go","kinds":["message"]}`); strings.Contains(out, "tool_call") {
		t.Errorf("kinds=[message] returned a tool_call event:\n%s", out)
	}
}

// An empty query is a mistake worth naming, not an invitation to return the
// whole corpus.
func TestSessionSearchRefusesAnEmptyQuery(t *testing.T) {
	home, cwd := testsupport.TempDir(t), testsupport.TempDir(t)
	tool := &SessionSearchTool{TervaHome: home, CWD: cwd}
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{"query":"   "}`), func(string) {})
	if !res.IsError {
		t.Fatal("an empty query should be refused")
	}
}

// A no-match answer must be distinguishable from a project with nothing to
// search, because they call for different next steps.
func TestSessionSearchDistinguishesNoSessionsFromNoMatch(t *testing.T) {
	home, empty := testsupport.TempDir(t), testsupport.TempDir(t)
	if out := runSearch(t, home, empty, `{"query":"anything"}`); !strings.Contains(out, "no recorded sessions") {
		t.Errorf("a project with no sessions should say so, got:\n%s", out)
	}

	cwd := testsupport.TempDir(t)
	writeRichSessionFixture(t, filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl"), cwd,
		textRow("user", "hello"))
	if out := runSearch(t, home, cwd, `{"query":"absent"}`); !strings.Contains(out, "no match") {
		t.Errorf("a searched-but-empty result should say no match, got:\n%s", out)
	}
}

// The snippet must contain the match. Returning the head of a 40KB tool result
// would report a hit the model cannot see.
func TestSessionSearchSnippetCentresOnTheMatch(t *testing.T) {
	home, cwd := testsupport.TempDir(t), testsupport.TempDir(t)
	long := strings.Repeat("padding ", 400) + "NEEDLE_HERE" + strings.Repeat(" trailing", 400)
	writeRichSessionFixture(t, filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl"), cwd,
		callRow("c1", "bash", `{"command":"cat big"}`),
		resultRow("c1", long, false),
	)

	out := runSearch(t, home, cwd, `{"query":"NEEDLE_HERE"}`)
	if !strings.Contains(out, "NEEDLE_HERE") {
		t.Fatalf("snippet did not contain the match it reported:\n%s", out)
	}
}

// writeSubAgentFixture plants a swarm child's transcript plus the spawn record
// that says which project owns it. Ownership lives in the meta's `origin`, not
// the child's dir, because a leased worktree hashes to a different project.
func writeSubAgentFixture(t *testing.T, home, id, origin string, rows ...string) {
	t.Helper()
	dir := filepath.Join(home, "swarm", "agents", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := fmt.Sprintf(`{"id":%q,"dir":%q,"origin":%q,"started":"2026-07-31T00:00:00Z"}`, id, origin, origin)
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(meta), 0o600); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	q, _ := json.Marshal(origin)
	fmt.Fprintf(&b, `{"type":"meta","meta":{"id":%q,"cwd":%s,"format_version":2}}`+"\n", id, string(q))
	for _, r := range rows {
		b.WriteString(r + "\n")
	}
	if err := os.WriteFile(filepath.Join(dir, "session.json"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A coordinator's most expensive findings end up in delegated work — one
// measured run spent $24.49 through sub-agents against $5.36 of its own turns.
// A recall tool blind to sub-agents misses the answer exactly when producing it
// was costly. This is also the half no out-of-tree extension can reach: a
// child's transcript lives under the swarm root, which the protocol-3 bridge
// never lists.
func TestSessionSearchCoversSubAgentsOfThisProject(t *testing.T) {
	home, cwd := testsupport.TempDir(t), testsupport.TempDir(t)
	writeRichSessionFixture(t, filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl"), cwd,
		textRow("user", "delegate the review"))
	writeSubAgentFixture(t, home, "agent-1", cwd,
		callRow("c1", "read", `{"path":"internal/world/spec/spec.go"}`),
		textRow("assistant", "the obligation contract is underspecified"),
	)

	out := runSearch(t, home, cwd, `{"query":"obligation contract"}`)
	if !strings.Contains(out, "obligation contract") {
		t.Fatalf("search missed a sub-agent's finding:\n%s", out)
	}
	if !strings.Contains(out, "[sub-agent]") {
		t.Errorf("a sub-agent hit must be labelled — its provenance changes what the text means:\n%s", out)
	}
	if !strings.Contains(out, "agent-1") {
		t.Errorf("hit should name the agent id for a session_inspect follow-up:\n%s", out)
	}

	// And the full-fidelity property holds inside a child too.
	if out := runSearch(t, home, cwd, `{"query":"spec.go"}`); !strings.Contains(out, "spec.go") {
		t.Errorf("a path in a sub-agent's tool_call arguments was not searchable:\n%s", out)
	}
}

// Confinement is by SPAWN RECORD. A child of another project must be invisible
// even though every child shares one swarm root.
func TestSessionSearchExcludesSubAgentsOfOtherProjects(t *testing.T) {
	home := testsupport.TempDir(t)
	mine, theirs := testsupport.TempDir(t), testsupport.TempDir(t)
	writeRichSessionFixture(t, filepath.Join(core.SessionsDir(home, mine), "mine.jsonl"), mine,
		textRow("user", "hello"))
	writeSubAgentFixture(t, home, "agent-theirs", theirs,
		textRow("assistant", "another projects delegated secret"))

	out := runSearch(t, home, mine, `{"query":"delegated secret"}`)
	if strings.Contains(out, "agent-theirs") || strings.Contains(out, "another projects") {
		t.Fatalf("search reached another project's sub-agent:\n%s", out)
	}
	if !strings.Contains(out, "no match") {
		t.Errorf("want an explicit no-match answer, got:\n%s", out)
	}
}

// An agent whose meta is missing or malformed has no provable owner. It must be
// skipped, not treated as belonging to whoever asked.
func TestSessionSearchSkipsSubAgentsWithNoProvableOwner(t *testing.T) {
	home, cwd := testsupport.TempDir(t), testsupport.TempDir(t)
	writeRichSessionFixture(t, filepath.Join(core.SessionsDir(home, cwd), "mine.jsonl"), cwd,
		textRow("user", "hello"))
	dir := filepath.Join(home, "swarm", "agents", "orphan")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// No meta.json at all — origin is unknowable.
	if err := os.WriteFile(filepath.Join(dir, "session.json"),
		[]byte(`{"type":"meta","meta":{"id":"orphan","format_version":2}}`+"\n"+textRow("assistant", "orphaned finding")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Assert on the scanned COUNT, not on the word "orphan": the no-match line
	// echoes the query back, so a substring check would match the tool's own
	// output rather than a leak. (Second time this bit — the echo is deliberate,
	// it tells the model what was actually searched for.)
	out := runSearch(t, home, cwd, `{"query":"orphaned finding"}`)
	if !strings.Contains(out, "across 1 session(s)") {
		t.Fatalf("an agent with no spawn record was searched anyway — ownership must fail closed:\n%s", out)
	}
	if !strings.Contains(out, "no match") {
		t.Errorf("want an explicit no-match answer, got:\n%s", out)
	}
}
