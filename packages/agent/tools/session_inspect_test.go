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
	writeSessionFixture(t, swarm.AgentSessionPath(swarm.DefaultRoot(home), "review-x-123000"),
		cwd, "task text", "the full findings report")
	res := run(`{"session_id":"review-x-123000"}`)
	if res.IsError {
		t.Fatalf("swarm child id should resolve, got error: %q", inspectText(t, res))
	}
	if got := inspectText(t, res); !strings.Contains(got, "the full findings report") {
		t.Errorf("listing should show the child's events, got: %q", got)
	}

	// A child of ANOTHER project is refused (fails closed on cwd mismatch).
	writeSessionFixture(t, swarm.AgentSessionPath(swarm.DefaultRoot(home), "other-999000"),
		testsupport.TempDir(t), "task", "secret findings")
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
	streamReplay = func(ctx context.Context, path string, maxBytes int64, fn func(int, provider.Message)) (core.SessionMeta, bool, error) {
		scanned = true
		return old(ctx, path, maxBytes, fn)
	}
	defer func() { streamReplay = old }()

	home := testsupport.TempDir(t)
	cwd := testsupport.TempDir(t)
	// A child whose meta records a FOREIGN cwd, with a marker in the body.
	writeSessionFixture(t, swarm.AgentSessionPath(swarm.DefaultRoot(home), "foreign-123"),
		testsupport.TempDir(t), "SHOULD_NOT_BE_SCANNED")

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
	writeSessionFixture(t, swarm.AgentSessionPath(swarm.DefaultRoot(home), "working-1"), cwd)
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
	writeSessionFixture(t, swarm.AgentSessionPath(swarm.DefaultRoot(home), "cold-1"), cwd)
	got := inspectText(t, run(`{"session_id":"cold-1","expand":-1}`))
	if !strings.Contains(got, "may have failed before its first turn") {
		t.Errorf("child with no event log should not claim to be running, got: %q", got)
	}

	// A FINISHED child whose filters genuinely exclude everything must keep the
	// filter message — the diagnosis is about an empty transcript, not an empty
	// match, and conflating them would hide a real filter mistake.
	writeSessionFixture(t, swarm.AgentSessionPath(swarm.DefaultRoot(home), "done-1"),
		cwd, "task text", "the findings")
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
