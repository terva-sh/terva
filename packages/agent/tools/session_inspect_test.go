package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func inspectMessages() []provider.Message {
	msg := func(role provider.Role, blocks ...provider.Content) provider.Message {
		return provider.Message{Role: role, Content: blocks}
	}
	return []provider.Message{
		msg(provider.RoleAssistant, provider.ToolCallBlock{ID: "c1", Name: "bash", Arguments: json.RawMessage(`{"command":"ls"}`)}),
		msg(provider.RoleTool, provider.ToolResultBlock{CallID: "c1", Content: []provider.Content{provider.TextBlock{Text: "file1\nfile2"}}}),
		msg(provider.RoleAssistant, provider.ToolCallBlock{ID: "c2", Name: "read", Arguments: json.RawMessage(`{"path":"x"}`)}),
		msg(provider.RoleTool, provider.ToolResultBlock{CallID: "c2", IsError: true, Content: []provider.Content{provider.TextBlock{Text: "boom Authorization: Bearer sk-ant-abcdefgh1234567890"}}}),
	}
}

func scanInspectMessages(a sessionInspectArgs) *sessScan {
	s := newSessScan(a)
	for i, m := range inspectMessages() {
		s.addMessage(i, m)
	}
	return s
}

func TestSessScanExtractsAndFilters(t *testing.T) {
	s := scanInspectMessages(sessionInspectArgs{})
	events, _, _ := s.window()
	if s.total != 4 || len(events) != 4 {
		t.Fatalf("want 4 events, got total=%d window=%d: %+v", s.total, len(events), events)
	}
	// Result correlates back to its call's tool name via call id, and carries is_error.
	if events[1].Kind != "tool_result" || events[1].Tool != "bash" || events[1].IsError {
		t.Errorf("event[1] = %+v, want a passing bash tool_result", events[1])
	}
	if events[3].Kind != "tool_result" || events[3].Tool != "read" || !events[3].IsError {
		t.Errorf("event[3] = %+v, want a failing read tool_result", events[3])
	}
	// Matched results consume their correlation entries (bounded map).
	if len(s.callName) != 0 {
		t.Errorf("correlation map should be empty after all results matched: %v", s.callName)
	}

	if s := scanInspectMessages(sessionInspectArgs{FailuresOnly: true}); s.total != 1 {
		t.Errorf("failures_only total = %d, want only the failed read result", s.total)
	} else if ev, _, _ := s.window(); ev[0].Tool != "read" {
		t.Errorf("failures_only = %+v, want the read result", ev)
	}
	if s := scanInspectMessages(sessionInspectArgs{ToolName: "bash"}); s.total != 2 {
		t.Errorf("tool_name=bash total = %d, want 2 (call + result)", s.total)
	}
	if s := scanInspectMessages(sessionInspectArgs{EventKinds: []string{"tool_call"}}); s.total != 2 {
		t.Errorf("event_kinds=[tool_call] total = %d, want 2", s.total)
	}
}

// TestSessScanBoundsRetention pins the R5.3 contract: the scan retains at most
// one page of clipped snippets (default mode: a ring of the latest N), never
// the whole transcript — and a cursor page keeps exactly its slice.
func TestSessScanBoundsRetention(t *testing.T) {
	long := strings.Repeat("x", 4096)
	s := newSessScan(sessionInspectArgs{Limit: 3})
	for i := 0; i < 500; i++ {
		s.addMessage(i, provider.Message{Role: provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("event %d %s", i, long)}}})
	}
	if s.total != 500 {
		t.Fatalf("total = %d, want 500", s.total)
	}
	if len(s.ring) != 3 {
		t.Fatalf("retained %d events, want the 3-event ring", len(s.ring))
	}
	events, start, end := s.window()
	if start != 497 || end != 500 || len(events) != 3 {
		t.Fatalf("window = [%d,%d) len %d, want [497,500) len 3", start, end, len(events))
	}
	for i, e := range events {
		if want := fmt.Sprintf("event %d", 497+i); !strings.HasPrefix(e.Text, want) {
			t.Errorf("window[%d] = %q…, want prefix %q (ring order broken)", i, e.Text[:20], want)
		}
		if len(e.Text) > siSnippetSourceMax {
			t.Errorf("retained text is %dB, want ≤ %d (snippet source clip)", len(e.Text), siSnippetSourceMax)
		}
		if e.Bytes <= siSnippetSourceMax {
			t.Errorf("Bytes = %d must report the TRUE event size, not the clipped one", e.Bytes)
		}
	}

	// Cursor mode keeps exactly the requested page, in order. Wire cursor 11 is
	// scan offset 10 — the args are 1-based, the scan is 0-based, and this is a
	// white-box test of the scan side.
	s = newSessScan(sessionInspectArgs{Limit: 5, Cursor: 11})
	for i := 0; i < 100; i++ {
		s.addMessage(i, provider.Message{Role: provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("event %d", i)}}})
	}
	events, start, end = s.window()
	if start != 10 || end != 15 || len(events) != 5 || events[0].Text != "event 10" {
		t.Fatalf("cursor window = [%d,%d) len %d first %q, want [10,15) len 5 %q", start, end, len(events), events[0].Text, "event 10")
	}

	// Expand mode retains only the target, at full length. Wire expand 43 is
	// scan index 42.
	s = newSessScan(sessionInspectArgs{Expand: 43})
	for i := 0; i < 100; i++ {
		s.addMessage(i, provider.Message{Role: provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("event %d %s", i, long)}}})
	}
	if s.ring != nil {
		t.Errorf("expand mode must not retain a listing window")
	}
	if s.expandEv == nil || !strings.HasPrefix(s.expandEv.Text, "event 42") || len(s.expandEv.Text) < 4096 {
		t.Fatalf("expand retention = %+v, want full text of event 42", s.expandEv)
	}

	// A negative expand keeps a full-text tail ring and resolves from the end
	// once the matches are counted. Negatives are NOT rebased by the 1-based
	// wire indices: -1 is still the most recent match.
	s = newSessScan(sessionInspectArgs{Expand: -2})
	for i := 0; i < 100; i++ {
		s.addMessage(i, provider.Message{Role: provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: fmt.Sprintf("event %d %s", i, long)}}})
	}
	if len(s.expandRing) != 2 {
		t.Errorf("tail ring retained %d events, want 2", len(s.expandRing))
	}
	ev, idx := s.resolveExpand()
	if ev == nil || idx != 98 || !strings.HasPrefix(ev.Text, "event 98") || len(ev.Text) < 4096 {
		t.Fatalf("resolveExpand(-2) = (%+v, %d), want full text of event 98 at #98", ev, idx)
	}
}

func TestEventSnippetRedactsSecrets(t *testing.T) {
	got := eventSnippet("boom Authorization: Bearer sk-ant-abcdefgh1234567890")
	if strings.Contains(got, "sk-ant-abcdefgh") {
		t.Errorf("snippet leaked the token: %q", got)
	}
	if !strings.Contains(got, "REDACTED") {
		t.Errorf("snippet should mark the redaction: %q", got)
	}
}

func TestResolvePathRefusesEscapes(t *testing.T) {
	tool := &SessionInspectTool{TervaHome: testsupport.TempDir(t), CWD: testsupport.TempDir(t)}
	for _, id := range []string{"../secret", "a/b", `..\win`, "nope-does-not-exist"} {
		if _, _, _, err := tool.resolvePath(context.Background(), id); err == nil {
			t.Errorf("resolvePath(%q) should error (escape or missing), got nil", id)
		}
	}
}

// writeSessionFixture writes a minimal replay-format transcript: a meta row
// recording cwd (what the swarm-child project confinement checks), then
// alternating user/assistant text messages.
func writeSessionFixture(t *testing.T, path, cwd string, msgs ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	quote := func(s string) string {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"meta","meta":{"id":"x","cwd":%s,"format_version":2}}`+"\n", quote(cwd))
	for i, m := range msgs {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		fmt.Fprintf(&b, `{"type":"message","message":{"role":%q,"content":[{"type":"text","text":%s}]}}`+"\n", role, quote(m))
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeSwarmChildFixture lays down a swarm sub-agent the way a real spawn does:
// its transcript AND the meta.json spawn record that says which project owns
// it. origin is the spawning swarm's RepoRoot; dir is where the child actually
// runs, which differs from origin exactly when the child was leased a worktree.
// Ownership is read from origin, so passing a lease path as dir must not change
// who the child belongs to.
func writeSwarmChildFixture(t *testing.T, home, id, origin, dir string, msgs ...string) {
	t.Helper()
	root := swarm.DefaultRoot(home)
	writeSessionFixture(t, swarm.AgentSessionPath(root, id), dir, msgs...)
	meta := map[string]any{
		"id": id, "task": "task text", "dir": dir, "origin": origin,
		"session_path": swarm.AgentSessionPath(root, id),
	}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents", id, "meta.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeLegacySwarmChildFixture is a spawn record from before `origin` existed:
// dir only. Ownership must still resolve for the unleased case, which is all
// such a record can honestly claim.
func writeLegacySwarmChildFixture(t *testing.T, home, id, dir string, msgs ...string) {
	t.Helper()
	root := swarm.DefaultRoot(home)
	writeSessionFixture(t, swarm.AgentSessionPath(root, id), dir, msgs...)
	b, err := json.Marshal(map[string]any{"id": id, "task": "task text", "dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "agents", id, "meta.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func inspectText(t *testing.T, r core.ToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range r.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			b.WriteString(tb.Text)
		}
	}
	return b.String()
}

// TestSessionInspectResolvesSwarmChild pins the S1 fix: a swarm sub-agent id
// (what swarm_spawn and the [auto-swarm update] recap print) resolves to the
// child's transcript under the swarm root — but only for children spawned
// from THIS project's cwd; anything else stays jailed out.
func TestSessionInspectResolvesSwarmChild(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}
	run := func(args string) core.ToolResult {
		t.Helper()
		res, err := tool.Execute(context.Background(), json.RawMessage(args), func(string) {})
		if err != nil {
			t.Fatalf("Execute(%s): %v", args, err)
		}
		return res
	}

	// A child spawned from this project's cwd is inspectable by its agent id.
	writeSwarmChildFixture(t, home, "review-x-123000", cwd, cwd, "task text", "the full findings report")
	res := run(`{"session_id":"review-x-123000"}`)
	if res.IsError {
		t.Fatalf("swarm child id should resolve, got error: %q", inspectText(t, res))
	}
	if got := inspectText(t, res); !strings.Contains(got, "the full findings report") {
		t.Errorf("listing should show the child's events, got: %q", got)
	}

	// A child of ANOTHER project is refused (fails closed on cwd mismatch).
	other := testsupport.TempDir(t)
	writeSwarmChildFixture(t, home, "other-999000", other, other, "task", "secret findings")
	res = run(`{"session_id":"other-999000"}`)
	if !res.IsError || !strings.Contains(inspectText(t, res), "not spawned from this project") {
		t.Errorf("cross-project child must be refused, got (err=%v): %q", res.IsError, inspectText(t, res))
	}

	// An id matching neither store names both id kinds in the error.
	res = run(`{"session_id":"missing-000"}`)
	if !res.IsError || !strings.Contains(inspectText(t, res), "no such session or swarm sub-agent") {
		t.Errorf("unknown id should name both id kinds, got (err=%v): %q", res.IsError, inspectText(t, res))
	}
}

// Under --swarm-worktrees every sub-agent is leased its own directory, so its
// cwd is a worktree path that hashes to a DIFFERENT project bucket than its
// parent's. The ownership guard used to read that cwd, which meant turning on
// the flag that isolates sub-agents silently disabled the tool that watches
// them: in one reviewed session two of three session_inspect calls were refused
// for a child the model had spawned itself, seconds earlier, from this project.
func TestSessionInspectResolvesLeasedSwarmChild(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	// A lease: a real per-agent worktree directory, nothing like the repo root.
	lease := filepath.Join(testsupport.TempDir(t), "worktrees", "migrate-two-613549")
	if err := os.MkdirAll(lease, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSwarmChildFixture(t, home, "migrate-two-613549", cwd, lease, "task text", "the migration report")

	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"session_id":"migrate-two-613549"}`), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("a leased child of THIS project must be inspectable, got: %q", inspectText(t, res))
	}
	if got := inspectText(t, res); !strings.Contains(got, "the migration report") {
		t.Errorf("listing should show the leased child's events, got: %q", got)
	}

	// The lease must not become a back door either: a child leased by ANOTHER
	// project stays refused, even though its worktree is equally foreign to both.
	otherLease := filepath.Join(testsupport.TempDir(t), "worktrees", "foreign-1")
	if err := os.MkdirAll(otherLease, 0o755); err != nil {
		t.Fatal(err)
	}
	writeSwarmChildFixture(t, home, "foreign-1", testsupport.TempDir(t), otherLease, "task", "secret findings")
	res, err = tool.Execute(context.Background(), json.RawMessage(`{"session_id":"foreign-1"}`), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(inspectText(t, res), "not spawned from this project") {
		t.Errorf("another project's leased child must stay refused, got (err=%v): %q", res.IsError, inspectText(t, res))
	}
}

// A spawn record written before `origin` existed carries only `dir`. For the
// unleased children those records describe, dir IS the parent's repo root, so
// ownership must still resolve rather than failing every pre-upgrade agent.
func TestSessionInspectAcceptsLegacySpawnRecord(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	writeLegacySwarmChildFixture(t, home, "legacy-1", cwd, "task text", "older findings")

	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"session_id":"legacy-1"}`), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("a legacy unleased child must still resolve, got: %q", inspectText(t, res))
	}
}

// No spawn record at all means no claim of ownership — fail closed rather than
// falling back to the transcript's own cwd, which an attacker-ish caller could
// have written.
func TestSessionInspectFailsClosedWithoutSpawnRecord(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	writeSessionFixture(t, swarm.AgentSessionPath(swarm.DefaultRoot(home), "orphan-1"), cwd, "task", "findings")

	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"session_id":"orphan-1"}`), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(inspectText(t, res), "not spawned from this project") {
		t.Errorf("a child with no spawn record must be refused, got (err=%v): %q", res.IsError, inspectText(t, res))
	}
}

// TestSessionInspectExpandPagesFullText pins expand mode: one matching event's
// full text, newlines preserved, paged at siExpandMax with an exact
// continuation offset — the recovery path for a sub-agent's complete report.
func TestSessionInspectExpandPagesFullText(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	long := strings.Repeat("report line\n", 2000) // 24000B, > siExpandMax
	writeSessionFixture(t, filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl"), cwd, "hi", long)
	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}
	run := func(args string) core.ToolResult {
		t.Helper()
		res, err := tool.Execute(context.Background(), json.RawMessage(args), func(string) {})
		if err != nil {
			t.Fatalf("Execute(%s): %v", args, err)
		}
		return res
	}

	// The listing advertises the #n coordinates expand consumes.
	if got := inspectText(t, run(`{"session_id":"sess1"}`)); !strings.Contains(got, "[#1 row") {
		t.Errorf("listing should carry #n indexes, got: %q", got)
	}

	res := run(`{"session_id":"sess1","event_kinds":["message"],"expand":2}`)
	body := inspectText(t, res)
	if !strings.Contains(body, "report line\nreport line") {
		t.Errorf("expand must preserve newlines, got: %q", body[:120])
	}
	det, ok := res.Details.(map[string]any)
	if !ok {
		t.Fatalf("details should be a map, got %T", res.Details)
	}
	next, ok := det["next_text_offset"].(int)
	if !ok {
		t.Fatalf("first page should truncate with a continuation offset, details: %+v", det)
	}
	if next != siExpandMax {
		t.Errorf("ASCII text should cut exactly at siExpandMax, got %d", next)
	}
	if !strings.Contains(body, fmt.Sprintf("text_offset %d", next)) {
		t.Errorf("truncation notice should name the continuation offset, got tail: %q", body[len(body)-120:])
	}

	res = run(fmt.Sprintf(`{"session_id":"sess1","event_kinds":["message"],"expand":2,"text_offset":%d}`, next))
	det, ok = res.Details.(map[string]any)
	if !ok {
		t.Fatalf("details should be a map, got %T", res.Details)
	}
	if _, again := det["next_text_offset"]; again {
		t.Errorf("second page should reach the end, details: %+v", det)
	}
	if got, want := det["text_total"], len(long); got != want {
		t.Errorf("text_total = %v, want %d", got, want)
	}

	// Out-of-range expand errors instead of guessing.
	res = run(`{"session_id":"sess1","expand":99}`)
	if !res.IsError || !strings.Contains(inspectText(t, res), "out of range") {
		t.Errorf("out-of-range expand must error, got (err=%v): %q", res.IsError, inspectText(t, res))
	}

	// Negative expand resolves from the end — the "latest matching event"
	// idiom — and renders under its absolute #n so the paging hint names a
	// coordinate that stays stable as the session grows.
	res = run(`{"session_id":"sess1","event_kinds":["message"],"expand":-1}`)
	body = inspectText(t, res)
	if res.IsError || !strings.Contains(body, "event #2") || !strings.Contains(body, "report line") {
		t.Errorf("expand -1 should render the last message as its absolute #2, got (err=%v): %q", res.IsError, body[:200])
	}
	if !strings.Contains(body, fmt.Sprintf("expand 2 with text_offset %d", siExpandMax)) {
		t.Errorf("continuation hint should carry the absolute index, got tail: %q", body[len(body)-120:])
	}

	// Too far negative is still out of range; beyond the tail cap says so
	// before any scan.
	res = run(`{"session_id":"sess1","expand":-99}`)
	if !res.IsError || !strings.Contains(inspectText(t, res), "out of range") {
		t.Errorf("over-negative expand must error, got (err=%v): %q", res.IsError, inspectText(t, res))
	}
	res = run(`{"session_id":"sess1","expand":-201}`)
	if !res.IsError || !strings.Contains(inspectText(t, res), "too far back") {
		t.Errorf("beyond the tail cap must error, got (err=%v): %q", res.IsError, inspectText(t, res))
	}
}

// TestSessionInspectScanCeilingNotice pins the DoS guard: a transcript past
// the scan ceiling still answers, but says the totals cover only the scanned
// prefix.
func TestSessionInspectScanCeilingNotice(t *testing.T) {
	old := siScanCeiling
	siScanCeiling = 1024
	defer func() { siScanCeiling = old }()

	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	var msgs []string
	for i := 0; i < 40; i++ {
		msgs = append(msgs, strings.Repeat("padding ", 16))
	}
	writeSessionFixture(t, filepath.Join(core.SessionsDir(home, cwd), "big.jsonl"), cwd, msgs...)
	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"session_id":"big"}`), func(string) {})
	if err != nil || res.IsError {
		t.Fatalf("Execute: err=%v isError=%v %q", err, res.IsError, inspectText(t, res))
	}
	if got := inspectText(t, res); !strings.Contains(got, "scan ceiling") {
		t.Errorf("output should carry the ceiling notice, got: %q", got)
	}
	det, _ := res.Details.(map[string]any)
	if trunc, _ := det["scan_truncated"].(bool); !trunc {
		t.Errorf("details should flag scan_truncated, got %+v", det)
	}
}

// TestSessScanBoundsOutstandingCalls pins Gap 2: a damaged transcript (many
// calls, no results) keeps retained correlations O(cap), not O(call count), and
// still correlates the most recent calls (a bounded recent window).
func TestSessScanBoundsOutstandingCalls(t *testing.T) {
	s := newSessScan(sessionInspectArgs{})
	n := 50 * siMaxOutstandingCalls
	for i := 0; i < n; i++ {
		s.addMessage(i, provider.Message{Role: provider.RoleAssistant,
			Content: []provider.Content{provider.ToolCallBlock{ID: fmt.Sprintf("c%d", i), Name: "bash"}}})
	}
	if len(s.callName) > siMaxOutstandingCalls {
		t.Errorf("callName grew to %d, want <= %d (bounded correlations)", len(s.callName), siMaxOutstandingCalls)
	}
	if len(s.callOrder) > 2*siMaxOutstandingCalls {
		t.Errorf("callOrder grew to %d, want <= %d (compacted)", len(s.callOrder), 2*siMaxOutstandingCalls)
	}
	if last := fmt.Sprintf("c%d", n-1); s.callName[last] != "bash" {
		t.Errorf("most recent call %s lost its name; recent window must retain the newest", last)
	}
}

// TestSessScanCallOrderCompactsHealthy pins the tombstone-compaction: a healthy
// stream of matched call+result pairs must not let callOrder grow with the pair
// count even though the cap is never reached.
func TestSessScanCallOrderCompactsHealthy(t *testing.T) {
	s := newSessScan(sessionInspectArgs{})
	for i := 0; i < 50*siMaxOutstandingCalls; i++ {
		id := fmt.Sprintf("c%d", i)
		s.addMessage(2*i, provider.Message{Role: provider.RoleAssistant,
			Content: []provider.Content{provider.ToolCallBlock{ID: id, Name: "bash"}}})
		s.addMessage(2*i+1, provider.Message{Role: provider.RoleTool,
			Content: []provider.Content{provider.ToolResultBlock{CallID: id}}})
	}
	if len(s.callName) != 0 {
		t.Errorf("all results matched, callName should be empty, got %d", len(s.callName))
	}
	if len(s.callOrder) > 2*siMaxOutstandingCalls {
		t.Errorf("callOrder retained %d tombstones, want compacted <= %d", len(s.callOrder), 2*siMaxOutstandingCalls)
	}
}

// TestSessionInspectAuthorizesSwarmChildBeforeScan pins Gap 3: a cross-project
// swarm child is rejected on its meta alone, BEFORE any transcript scan runs.
func TestSessionInspectAuthorizesSwarmChildBeforeScan(t *testing.T) {
	scanned := false
	old := streamReplay
	streamReplay = func(ctx context.Context, path string, maxBytes int64, fn func(int, core.ReplayRow)) (core.SessionMeta, bool, error) {
		scanned = true
		return old(ctx, path, maxBytes, fn)
	}
	defer func() { streamReplay = old }()

	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	// A child whose meta records a FOREIGN cwd, with a marker in the body.
	foreign := testsupport.TempDir(t)
	writeSwarmChildFixture(t, home, "foreign-123", foreign, foreign, "SHOULD_NOT_BE_SCANNED")

	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"session_id":"foreign-123"}`), func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || !strings.Contains(inspectText(t, res), "not spawned from this project") {
		t.Fatalf("cross-project child must be rejected, got (err=%v): %q", res.IsError, inspectText(t, res))
	}
	if scanned {
		t.Error("transcript was scanned before the cross-project authorization gate ran")
	}
}

// TestSessionInspectDiagnosesRunningChild pins the fix for the loop this
// message used to cause. A sub-agent now streams its transcript as it works,
// so an empty one means only that it has not completed its first message —
// a narrow timing window rather than the whole task. Reporting that as "no
// events match these filters" reads as a filter problem the caller can fix by
// re-filtering, which it cannot; the result must name the real state instead.
func TestSessionInspectDiagnosesRunningChild(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}
	run := func(args string) core.ToolResult {
		t.Helper()
		res, err := tool.Execute(context.Background(), json.RawMessage(args), func(string) {})
		if err != nil {
			t.Fatalf("Execute(%s): %v", args, err)
		}
		return res
	}

	// Meta row only: exactly what a child that has not finished looks like.
	writeSwarmChildFixture(t, home, "working-1", cwd, cwd)
	// A live event log is the signal that it is mid-task rather than done.
	if err := os.WriteFile(swarm.AgentEventLogPath(swarm.DefaultRoot(home), "working-1"),
		[]byte(`{"type":"turn_start"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Every filter shape the caller might reach for must give the same answer,
	// because varying them is precisely the dead end being closed.
	for _, args := range []string{
		`{"session_id":"working-1","event_kinds":["message"],"expand":-1}`,
		`{"session_id":"working-1","event_kinds":["tool_call","tool_result","message"],"expand":0}`,
		`{"session_id":"working-1","event_kinds":[],"limit":200}`,
		`{"session_id":"working-1"}`,
	} {
		got := inspectText(t, run(args))
		if strings.Contains(got, "no events match these filters") {
			t.Fatalf("%s still blames the filters: %q", args, got)
		}
		if !strings.Contains(got, "is running but has not completed its first message") {
			t.Errorf("%s should report the child as running, got: %q", args, got)
		}
		if !strings.Contains(got, "streams its transcript as it works") {
			t.Errorf("%s should say the transcript is live, got: %q", args, got)
		}
		if !strings.Contains(got, "auto-swarm update") {
			t.Errorf("%s should tell the caller the recap is pushed, got: %q", args, got)
		}
		// The push only happens for swarm_spawn children (auto-swarm wires
		// the tracker through OnSpawned), so the promise must stay
		// conditional — a /swarm-spawned child would otherwise be told to
		// wait for an update that never comes.
		if !strings.Contains(got, "If you spawned it with swarm_spawn") {
			t.Errorf("%s states the recap unconditionally, got: %q", args, got)
		}
	}

	// No event log yet — still diagnosed, just without the liveness claim.
	writeSwarmChildFixture(t, home, "cold-1", cwd, cwd)
	got := inspectText(t, run(`{"session_id":"cold-1","expand":-1}`))
	if !strings.Contains(got, "may have failed before its first turn") {
		t.Errorf("child with no event log should not claim to be running, got: %q", got)
	}

	// A FINISHED child whose filters genuinely exclude everything must keep the
	// filter message — the diagnosis is about an empty transcript, not an empty
	// match, and conflating them would hide a real filter mistake.
	writeSwarmChildFixture(t, home, "done-1", cwd, cwd, "task text", "the findings")
	got = inspectText(t, run(`{"session_id":"done-1","tool_name":"nonexistent","expand":1}`))
	if !strings.Contains(got, "no events match these filters") {
		t.Errorf("a real filter miss on a finished child must still say so, got: %q", got)
	}
	// …and in list mode too, where the same miss used to render as the nonsense
	// range "showing 1–0".
	got = inspectText(t, run(`{"session_id":"done-1","tool_name":"nonexistent"}`))
	if !strings.Contains(got, "no events match these filters") || strings.Contains(got, "showing") {
		t.Errorf("an empty listing must say so, not print an empty range, got: %q", got)
	}
}

// TestSessionInspectRejectsMixedModes covers the mode conflict that survives
// TW-031: a call naming a REAL expand target and a REAL cursor/limit is
// contradictory and is rejected with both corrected forms, rather than silently
// dropping one mode's intent.
//
// What no longer belongs here is the padded call. TW-013 F2 made {"expand":0,
// "cursor":0} an error because expand's presence chose the mode; TW-031 makes 0
// mean "unset" on both fields, so that same call is now simply the default
// listing — see TestSessionInspectPaddedZeroArgsList. Runs before any
// transcript I/O, so it needs no seeded session.
func TestSessionInspectRejectsMixedModes(t *testing.T) {
	tool := &SessionInspectTool{TervaHome: testsupport.TempDir(t), CWD: testsupport.TempDir(t)}
	for _, args := range []string{
		`{"expand":2,"cursor":5}`,
		`{"expand":2,"limit":40}`,
		`{"expand":-1,"cursor":5}`,
	} {
		res, err := tool.Execute(context.Background(), json.RawMessage(args), func(string) {})
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		if !res.IsError {
			t.Errorf("%s: expected a rejection, got: %q", args, inspectText(t, res))
			continue
		}
		if txt := inspectText(t, res); !strings.Contains(txt, "LIST MODE") || !strings.Contains(txt, "EXPAND MODE") {
			t.Errorf("%s: rejection should show both corrected forms:\n%s", args, txt)
		}
	}
	// A bare expand (no listing fields) is a legitimate EXPAND MODE call and must
	// NOT trip the guard — it may fail later for other reasons, but not with the
	// combine-them message.
	res, _ := tool.Execute(context.Background(), json.RawMessage(`{"expand":2}`), func(string) {})
	if res.IsError && strings.Contains(inspectText(t, res), "do not combine them") {
		t.Errorf("a bare expand must not trip the mixed-mode guard:\n%s", inspectText(t, res))
	}
}

// TestSessionInspectPaddedZeroArgsList is TW-031's acceptance case, and it uses
// the exact argument object from the session that produced the note: a model
// asked to find its own failures filled in every optional key with its zero
// value, and got four rejections in a row instead of a listing.
//
// Each zero must now be inert. expand 0 lists rather than expanding; cursor 0
// means the most recent window rather than the oldest, which was the silent
// half of the same defect — a caller asking for recent failures was handed the
// beginning of the session with nothing in the output saying so.
func TestSessionInspectPaddedZeroArgsList(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	// More messages than the window, so "oldest" and "most recent" differ and
	// the cursor half of the defect is actually observable.
	msgs := make([]string, 60)
	for i := range msgs {
		msgs[i] = fmt.Sprintf("message %02d", i)
	}
	writeSessionFixture(t, filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl"), cwd, msgs...)
	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}

	padded := `{"session_id":"sess1","cursor":0,"event_kinds":["message"],"expand":0,"limit":40,"text_offset":0}`
	res, err := tool.Execute(context.Background(), json.RawMessage(padded), func(string) {})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("a fully padded call must reach list mode, got: %q", inspectText(t, res))
	}
	got := inspectText(t, res)
	// The MOST RECENT window: the last message is present, the first is not.
	if !strings.Contains(got, "message 59") {
		t.Errorf("padded call should return the most recent window, got: %q", got)
	}
	if strings.Contains(got, "message 00") {
		t.Errorf("padded cursor 0 returned the OLDEST window — the silent half of TW-031:\n%s", got)
	}
	// Indices are 1-based, so the first match is #1 and there is no #0 to
	// collide with the unset value.
	if strings.Contains(got, "[#0 ") {
		t.Errorf("listing must be 1-based — #0 is the unset value, got: %q", got)
	}

	// Paging still works off the coordinates the listing prints: cursor 1 is the
	// oldest page, and it must be a DIFFERENT window from the padded default.
	res, _ = tool.Execute(context.Background(), json.RawMessage(`{"session_id":"sess1","event_kinds":["message"],"cursor":1,"limit":40}`), func(string) {})
	oldest := inspectText(t, res)
	if !strings.Contains(oldest, "message 00") || !strings.Contains(oldest, "[#1 ") {
		t.Errorf("cursor 1 should page from the oldest match at #1, got: %q", oldest)
	}
	if oldest == got {
		t.Errorf("cursor 1 and cursor 0 must not return the same window")
	}
}

// writeUsageSessionFixture writes a transcript that interleaves messages with
// the usage rows a real session records, so the scan sees both kinds.
func writeUsageSessionFixture(t *testing.T, path, cwd string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `{"type":"meta","meta":{"id":"x","cwd":%q,"format_version":2}}`+"\n", cwd)
	b.WriteString(`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}` + "\n")
	// A cheap turn that mostly hit the prefix cache.
	b.WriteString(`{"type":"usage","usage":{"input_tokens":2178,"output_tokens":77,"cache_read_tokens":225792,"cache_write_tokens":0,"cost_usd":0.1261},"cumulative":{"input_tokens":2178,"output_tokens":77,"cache_read_tokens":225792,"cache_write_tokens":0,"cost_usd":0.1261}}` + "\n")
	b.WriteString(`{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"world"}]}}` + "\n")
	// The expensive twin: same context, almost none of it cached.
	b.WriteString(`{"type":"usage","usage":{"input_tokens":218844,"output_tokens":236,"cache_read_tokens":8704,"cache_write_tokens":0,"cost_usd":1.1057},"cumulative":{"input_tokens":221022,"output_tokens":313,"cache_read_tokens":234496,"cache_write_tokens":0,"cost_usd":1.2318}}` + "\n")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSessionInspectSurfacesUsage pins D1 of the 2026-07-30 session-harness
// review: what a turn COST is recorded in the transcript but was unreachable
// from inside terva, because event_kinds had no "usage". A session could be
// described in full except for the one axis that explains its bill.
func TestSessionInspectSurfacesUsage(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	writeUsageSessionFixture(t, filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl"), cwd)
	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"session_id":"sess1","event_kinds":["usage"]}`), func(string) {})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("usage listing errored: %s", inspectText(t, res))
	}
	got := inspectText(t, res)
	if !strings.Contains(got, "2 matching event(s)") {
		t.Errorf("want both usage rows, got:\n%s", got)
	}
	// Uncached input and cache reads must stay SEPARATE — the ratio is the
	// signal, and a summed "input" figure hides the expensive turn entirely.
	if !strings.Contains(got, "in 218844") || !strings.Contains(got, "cached 8704") {
		t.Errorf("usage line must report uncached input and cache reads separately, got:\n%s", got)
	}
	if !strings.Contains(got, "$1.1057") {
		t.Errorf("usage line must carry the turn cost, got:\n%s", got)
	}
	// A usage event has no tool or byte count; it must not render the text
	// columns and print a "0B" that reads as a defect.
	if strings.Contains(got, "0B") {
		t.Errorf("usage line must not render the text-event byte column, got:\n%s", got)
	}

	// Expand gives the full breakdown, including the cache hit rate the
	// one-line form leaves out.
	res, err = tool.Execute(context.Background(), json.RawMessage(`{"session_id":"sess1","event_kinds":["usage"],"expand":2}`), func(string) {})
	if err != nil {
		t.Fatalf("Execute expand: %v", err)
	}
	if res.IsError {
		t.Fatalf("usage expand errored: %s", inspectText(t, res))
	}
	det := inspectText(t, res)
	for _, want := range []string{"cache_read_tokens  8704", "cache_write_tokens 0", "cost_usd           1.105700", "cache hit rate", "session cumulative"} {
		if !strings.Contains(det, want) {
			t.Errorf("expanded usage missing %q, got:\n%s", want, det)
		}
	}

	// failures_only is about failed TOOL RESULTS; a usage row is not a failure
	// and must not be swept in by it.
	res, _ = tool.Execute(context.Background(), json.RawMessage(`{"session_id":"sess1","failures_only":true}`), func(string) {})
	if fo := inspectText(t, res); strings.Contains(fo, "usage") {
		t.Errorf("failures_only must exclude usage events, got:\n%s", fo)
	}
}

// writeErrorSidecar writes the .errors.jsonl companion for a transcript.
func writeErrorSidecar(t *testing.T, transcript string, rows ...string) {
	t.Helper()
	if err := os.WriteFile(core.ErrorLogPathFor(transcript), []byte(strings.Join(rows, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSessionInspectUsesUsageStamps pins what the `at` field on usage rows buys.
//
// Both halves failed before the stamp existed, and both were needed to diagnose
// the 2026-08-01 codex cache collapse by hand:
//
//   - the GAP between dispatches, because a prefix cache ages out on idle time,
//     so "did this miss because the prefix expired" is a question about the
//     interval and nothing else in the transcript records it;
//   - a sidecar error placed against the DISPATCH it killed rather than the next
//     message, which in a tool-heavy turn can be many calls later — the ordering
//     is what says whether the collapse began before the overload or after it.
func TestSessionInspectUsesUsageStamps(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	path := filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Two dispatches 7 minutes apart, with the overload landing between them.
	body := `{"type":"meta","meta":{"id":"x","cwd":"` + cwd + `","format_version":2}}
{"type":"message","message":{"role":"user","content":[{"type":"text","text":"go"}],"time":"2026-08-01T12:00:00Z"}}
{"type":"usage","usage":{"input_tokens":10,"output_tokens":2,"cache_read_tokens":9728,"cache_write_tokens":0,"cost_usd":0.1},"cumulative":{"input_tokens":10,"output_tokens":2,"cache_read_tokens":9728,"cache_write_tokens":0,"cost_usd":0.1},"at":"2026-08-01T12:00:30Z"}
{"type":"usage","usage":{"input_tokens":200000,"output_tokens":5,"cache_read_tokens":9728,"cache_write_tokens":0,"cost_usd":1.0},"cumulative":{"input_tokens":200010,"output_tokens":7,"cache_read_tokens":19456,"cache_write_tokens":0,"cost_usd":1.1},"at":"2026-08-01T12:07:30Z"}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	writeErrorSidecar(t, path,
		`{"time":"2026-08-01T12:01:00Z","error":"openai-codex: Our servers are currently overloaded. Please try again later.","provider":"openai-codex","model":"gpt-5.6-sol"}`)

	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"session_id":"sess1"}`), func(string) {})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := inspectText(t, res)

	// The error postdates the first dispatch and predates the second, so it must
	// sit between them. Before the stamp it could only be placed against a
	// message, and here there is no later message to hang it on at all.
	firstDispatch := strings.Index(got, "in 10 ")
	overload := strings.Index(got, "overloaded")
	second := strings.Index(got, "in 200000")
	if firstDispatch < 0 || overload < 0 || second < 0 {
		t.Fatalf("missing an expected event (dispatch=%d overload=%d second=%d):\n%s", firstDispatch, overload, second, got)
	}
	if !(firstDispatch < overload && overload < second) {
		t.Errorf("overload must sit between the two dispatches, got order %d/%d/%d:\n%s", firstDispatch, overload, second, got)
	}

	// The gap rides expand mode, where the full token breakdown already lives.
	res, err = tool.Execute(context.Background(), json.RawMessage(`{"session_id":"sess1","event_kinds":["usage"],"expand":-1}`), func(string) {})
	if err != nil {
		t.Fatalf("Execute expand: %v", err)
	}
	detail := inspectText(t, res)
	if !strings.Contains(detail, "2026-08-01T12:07:30Z") {
		t.Errorf("expanded usage must report when it was billed, got:\n%s", detail)
	}
	if !strings.Contains(detail, "7m0s after the previous dispatch") {
		t.Errorf("expanded usage must report the gap from the previous dispatch, got:\n%s", detail)
	}
}

// TestSessionInspectToleratesUnstampedUsage guards the legacy path: every
// session worth analyzing today predates the stamp. An unstamped row must not
// drain the pending sidecar errors (drainErrors reads a zero cutoff as "drain
// everything"), or every later error would hang off the wrong turn.
func TestSessionInspectToleratesUnstampedUsage(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	path := filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"meta","meta":{"id":"x","cwd":"` + cwd + `","format_version":2}}
{"type":"usage","usage":{"input_tokens":10,"output_tokens":2,"cache_read_tokens":0,"cache_write_tokens":0,"cost_usd":0.1},"cumulative":{"input_tokens":10,"output_tokens":2,"cache_read_tokens":0,"cache_write_tokens":0,"cost_usd":0.1}}
{"type":"message","message":{"role":"user","content":[{"type":"text","text":"later"}],"time":"2026-08-01T12:30:00Z"}}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	writeErrorSidecar(t, path,
		`{"time":"2026-08-01T12:20:00Z","error":"openai-codex: overloaded","provider":"openai-codex","model":"gpt-5.6-sol"}`)

	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"session_id":"sess1"}`), func(string) {})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := inspectText(t, res)
	// The error is newer than the unstamped usage row and older than the
	// message, so the message is what must place it.
	usage := strings.Index(got, "in 10 ")
	overload := strings.Index(got, "overloaded")
	msg := strings.Index(got, "later")
	if usage < 0 || overload < 0 || msg < 0 {
		t.Fatalf("missing an expected event (usage=%d overload=%d msg=%d):\n%s", usage, overload, msg, got)
	}
	if !(usage < overload && overload < msg) {
		t.Errorf("an unstamped usage row must not claim the error; want usage<overload<message, got %d/%d/%d:\n%s", usage, overload, msg, got)
	}
}

// TestSessionInspectPlacesSidecarErrors pins D3 of the 2026-07-30
// session-harness review. Provider failures — auth, overload, rate limit — are
// recorded in a sidecar file that no tool could reach, so a turn that died on
// an overload left the transcript looking merely quiet. Correlating the two by
// hand is what turned "the session felt flaky" into a diagnosis.
func TestSessionInspectPlacesSidecarErrors(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	path := filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	// Two user turns with the same text: the overload between them is the
	// reason the second exists.
	body := `{"type":"meta","meta":{"id":"x","cwd":"` + cwd + `","format_version":2}}
{"type":"message","message":{"role":"user","content":[{"type":"text","text":"push now"}],"time":"2026-07-30T12:46:04-05:00"}}
{"type":"message","message":{"role":"user","content":[{"type":"text","text":"push now"}],"time":"2026-07-30T12:55:03-05:00"}}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	writeErrorSidecar(t, path,
		`{"time":"2026-07-30T17:46:06Z","error":"openai-codex: Our servers are currently overloaded. Please try again later.","provider":"openai-codex","model":"gpt-5.6-sol"}`)

	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"session_id":"sess1"}`), func(string) {})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := inspectText(t, res)

	if !strings.Contains(got, "overloaded") {
		t.Fatalf("sidecar error never reached the listing:\n%s", got)
	}
	// Placed BETWEEN the two turns, not bolted onto either end — that ordering
	// is the whole point, and it is what makes the retype legible.
	first := strings.Index(got, "push now")
	errAt := strings.Index(got, "overloaded")
	last := strings.LastIndex(got, "push now")
	if !(first < errAt && errAt < last) {
		t.Errorf("error should sit between the two turns (%d < %d < %d):\n%s", first, errAt, last, got)
	}
	// The provider prefix is not doubled: the client already stamps its name
	// into the message text.
	if strings.Contains(got, "openai-codex: openai-codex:") {
		t.Errorf("provider prefix doubled:\n%s", got)
	}
	// A sidecar row has no transcript row, and must not claim one.
	if strings.Contains(got, "row -1") {
		t.Errorf("sidecar event rendered a fake row number:\n%s", got)
	}

	// Filterable on its own.
	res, _ = tool.Execute(context.Background(), json.RawMessage(`{"session_id":"sess1","event_kinds":["error"]}`), func(string) {})
	if only := inspectText(t, res); !strings.Contains(only, "1 matching event") {
		t.Errorf(`event_kinds ["error"] should match exactly the sidecar row, got:\n%s`, only)
	}
}

// TestSessionInspectStats pins D2: a whole-session rollup, derived in one
// streaming pass. Reaching these numbers by listing meant walking every event
// at a retrieval cost proportional to the whole context — technically possible,
// priced so nobody asks.
func TestSessionInspectStats(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	path := filepath.Join(core.SessionsDir(home, cwd), "sess1.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"meta","meta":{"id":"x","cwd":"` + cwd + `","format_version":2}}
{"type":"message","message":{"role":"assistant","content":[{"type":"tool_call","id":"c1","name":"read","arguments":{}}]}}
{"type":"message","message":{"role":"tool","content":[{"type":"tool_result","call_id":"c1","is_error":true,"content":[{"type":"text","text":"jailed"}]}]}}
{"type":"message","message":{"role":"assistant","content":[{"type":"tool_call","id":"c2","name":"bash","arguments":{}}]}}
{"type":"message","message":{"role":"tool","content":[{"type":"tool_result","call_id":"c2","content":[{"type":"text","text":"ok"}]}]}}
{"type":"usage","usage":{"input_tokens":1000,"output_tokens":50,"cache_read_tokens":9000,"cache_write_tokens":0,"cost_usd":0.5},"cumulative":{"input_tokens":1000,"output_tokens":50,"cache_read_tokens":9000,"cache_write_tokens":0,"cost_usd":0.5}}
{"type":"usage","usage":{"input_tokens":0,"output_tokens":0,"cache_read_tokens":0,"cache_write_tokens":0,"cost_usd":0},"cumulative":{"input_tokens":1000,"output_tokens":50,"cache_read_tokens":9000,"cache_write_tokens":0,"cost_usd":0.5}}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	writeErrorSidecar(t, path,
		`{"time":"2026-07-30T17:46:06Z","error":"Our servers are currently overloaded.","provider":"openai-codex","model":"gpt-5.6-sol"}`)

	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"session_id":"sess1","stats":true}`), func(string) {})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.IsError {
		t.Fatalf("stats errored: %s", inspectText(t, res))
	}
	got := inspectText(t, res)
	for _, want := range []string{
		"cost: $0.5000 over 2 billed turn(s)",
		"cache hit rate 90.0% of input",
		"1 turn(s) recorded zero tokens and zero cost", // the dead turn
		"1  read", // tool-call histogram
		"1  bash",
		"provider errors (1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stats missing %q:\n%s", want, got)
		}
	}
	// The failure histogram attributes to the CALLING tool, via call_id.
	failAt := strings.Index(got, "failed tool results:")
	if failAt < 0 || !strings.Contains(got[failAt:], "read") {
		t.Errorf("failed-result histogram should name read:\n%s", got)
	}
	// Machine-readable too, so a caller can act without parsing prose.
	det, ok := res.Details.(map[string]any)
	if !ok {
		t.Fatalf("Details = %T, want map[string]any", res.Details)
	}
	if det["cost_usd"] != 0.5 || det["dead_turns"] != 1 {
		t.Errorf("details = %v, want cost_usd 0.5 and dead_turns 1", det)
	}

	// stats and expand are contradictory modes, not a narrowing.
	res, _ = tool.Execute(context.Background(), json.RawMessage(`{"session_id":"sess1","stats":true,"expand":1}`), func(string) {})
	if !res.IsError {
		t.Error("stats + expand should be rejected, not silently resolved")
	}
	// ...but a padded call carrying the listing filters is fine: stats ignores
	// them, and rejecting the common shape is the TW-031 mistake again.
	res, _ = tool.Execute(context.Background(), json.RawMessage(`{"session_id":"sess1","stats":true,"cursor":0,"limit":0,"expand":0,"text_offset":0}`), func(string) {})
	if res.IsError {
		t.Errorf("a fully padded stats call must work: %s", inspectText(t, res))
	}
}

// A model with no published rate prices at zero, so a real session reports
// cost_usd 0 on every turn. Rendering that as "$0.0000 over N billed turn(s)"
// says the session was free; one reviewed session moved 112 million tokens and
// read exactly that way. Absence of a price is not a measurement of spend.
//
// The fixture sets cost_usd 0 explicitly, so it stays valid whatever the
// catalogue prices — which is what makes it a test of the RENDERING rule rather
// than of any one provider's rates.
func TestSessionInspectStatsNamesUnpricedCost(t *testing.T) {
	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	path := filepath.Join(core.SessionsDir(home, cwd), "subs.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"meta","meta":{"id":"x","cwd":"` + cwd + `","format_version":2}}
{"type":"message","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"type":"usage","usage":{"input_tokens":2193445,"output_tokens":452267,"cache_read_tokens":109350656,"cache_write_tokens":0,"cost_usd":0},"cumulative":{"input_tokens":2193445,"output_tokens":452267,"cache_read_tokens":109350656,"cache_write_tokens":0,"cost_usd":0}}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := &SessionInspectTool{TervaHome: home, CWD: cwd}
	res, err := tool.Execute(context.Background(), json.RawMessage(`{"session_id":"subs","stats":true}`), func(string) {})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := inspectText(t, res)
	if strings.Contains(got, "$0.0000") {
		t.Errorf("an unpriced session must not report a dollar figure:\n%s", got)
	}
	if !strings.Contains(got, "not priced") {
		t.Errorf("stats should say the model carries no published rate:\n%s", got)
	}
	// "subscription" would now name the one case this is NOT: every subscription
	// provider carries published rates, so its readout is an estimate, not a zero.
	if strings.Contains(got, "subscription") {
		t.Errorf("the unpriced line still blames subscriptions, which are priced now:\n%s", got)
	}
	// The token counts are the real signal and must survive.
	if !strings.Contains(got, "109350656") {
		t.Errorf("cache-read tokens missing from an unpriced rollup:\n%s", got)
	}
	// A genuinely zero-token session is a different thing and keeps the old shape.
	if !strings.Contains(got, "cache hit rate") {
		t.Errorf("cache hit rate should still be reported:\n%s", got)
	}
}
