package worker

import (
	"bufio"
	"os"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/mcpbridge"
)

// realStream is a transcript CAPTURED FROM THE ACTUAL CLI (2.1.209), not one
// hand-written from what the docs say it emits. That distinction is the whole
// value of the fixture: two of its event types appear in no research note, and a
// translator written from documentation would have been wrong about them on the
// day it shipped.
//
// Re-capture with:
//
//	claude -p --output-format stream-json --verbose --model haiku "Reply with exactly: OK"
func realStream(t *testing.T) []string {
	t.Helper()
	f, err := os.Open("testdata/claude-2.1.209-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}

// The golden translation: a real run, replayed, must produce exactly the swarm
// events terva expects — and no others.
func TestTranslateRealClaudeStream(t *testing.T) {
	var got []Event
	for _, line := range realStream(t) {
		got = append(got, translateClaude([]byte(line))...)
	}

	var types []string
	for _, e := range got {
		types = append(types, e.Type)
	}
	want := []string{"agent_ready", "assistant_message", "assistant_message", "task_end"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Fatalf("translated to %v, want %v", types, want)
	}

	// agent_ready carries the version stamp — free, in-band, and the version that
	// ACTUALLY RAN rather than one a separate probe subprocess reported.
	if v := got[0].Data["version"]; v != "2.1.209" {
		t.Errorf("version stamp = %v, want the CLI version from the init event", v)
	}

	// task_end is synthesized. There is no task_end abroad — a foreign worker
	// stops talking — so this event is terva's, made from the result envelope.
	end := got[len(got)-1]
	if _, isErr := end.Data["error"]; isErr {
		t.Errorf("a successful run must not synthesize an error: %v", end.Data)
	}
	cost, ok := end.Data["cost_usd"].(float64)
	if !ok || cost <= 0 {
		t.Errorf("cost accounting should come straight off the result envelope, got %v", end.Data["cost_usd"])
	}
	if end.Data["usage"] == nil {
		t.Error("usage (incl. cache reads) is in the envelope and should reach the snapshot")
	}
}

// The default case is the load-bearing one, and this is the evidence for it. The
// real CLI emitted `rate_limit_event` and `system/thinking_tokens` in a
// three-second run, and NEITHER appears in any research note this design was
// built from. A translator that switched exhaustively over a closed set would
// already have been dropping events from a vendor that ships weekly.
//
// So: an unmodelled event must translate to NOTHING and must not panic. The
// runner keeps the raw line regardless, which is what makes silence safe here.
func TestUnknownEventsDegradeInsteadOfExploding(t *testing.T) {
	for _, line := range []string{
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed"}}`,
		`{"type":"system","subtype":"thinking_tokens","estimated_tokens":47}`,
		`{"type":"a_type_that_does_not_exist_yet","payload":{"x":1}}`,
		`{"type":"system","subtype":"a_subtype_from_next_month"}`,
		`not json at all`,
		``,
	} {
		if evs := translateClaude([]byte(line)); len(evs) != 0 {
			t.Errorf("unmodelled line should translate to nothing, got %v for %q", evs, line)
		}
	}
}

// A failing run must synthesize a task_end that SAYS it failed. A worker that
// stops talking looks identical to one that finished, and the supervisor has
// nothing else to go on.
func TestFailedResultBecomesAnErrorTaskEnd(t *testing.T) {
	line := `{"type":"result","subtype":"error","is_error":true,"result":"could not find the test file","num_turns":3,"total_cost_usd":0.02}`
	evs := translateClaude([]byte(line))
	if len(evs) != 1 || evs[0].Type != "task_end" {
		t.Fatalf("want one task_end, got %v", evs)
	}
	if msg, _ := evs[0].Data["error"].(string); !strings.Contains(msg, "could not find the test file") {
		t.Errorf("the failure must carry its reason, got %v", evs[0].Data["error"])
	}
}

// Nothing terva-shaped may reach the child's command line. The scrub guards the
// briefing text; this guards the argv, which is the other thing the child reads.
func TestClaudeArgvNamesNoTervaTool(t *testing.T) {
	r := loadedRepo(t)
	b := Compose(r, demoTask(), Workspace{Path: "/leases/wt-1"})
	cmd, err := claudeCommand(Dispatch{Briefing: b, Dir: "/leases/wt-1", Cursor: claudeCursor("a-1")})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(cmd.Args, " ")
	if leaks := Scrub(argv, r); len(leaks) > 0 {
		t.Errorf("the command line leaked across the harness boundary: %v", leaks)
	}
	if strings.Contains(strings.ToLower(argv), "terva") {
		t.Errorf("terva named on a foreign agent's command line:\n%s", argv)
	}
	// stdin must be the steer channel, or the child blocks for three seconds on
	// every dispatch and cannot be steered at all.
	if !strings.Contains(argv, "--input-format stream-json") {
		t.Errorf("without --input-format stream-json the child blocks on stdin and cannot be steered:\n%s", argv)
	}
}

// The cursor exists BEFORE the process does, and it is a function of the agent
// rather than an accident of when it was minted. An agent whose meta.json was
// lost — or never written, because the child died first — is still recoverable.
func TestCursorIsMintedNotScraped(t *testing.T) {
	a := claudeCursor("flaky-retry-42")
	if a != claudeCursor("flaky-retry-42") {
		t.Error("the cursor must be derivable from the agent id, or a lost meta.json is an unrecoverable agent")
	}
	if a == claudeCursor("some-other-agent") {
		t.Error("two agents must not share a session")
	}
	// --session-id validates its argument as a uuid; an invalid one fails the
	// spawn, and it fails it at the worst possible time (after the lease).
	if len(a) != 36 || strings.Count(a, "-") != 4 {
		t.Fatalf("not uuid-shaped: %q", a)
	}
	if a[14] != '4' {
		t.Errorf("uuid version nibble must be 4, got %q in %q", a[14], a)
	}
	if !strings.ContainsRune("89ab", rune(a[19])) {
		t.Errorf("uuid variant nibble must be 8/9/a/b, got %q in %q", a[19], a)
	}
}

// A first run ESTABLISHES the session; a revival RESUMES it. Getting these
// backwards would either re-run the task from scratch on every restart, or fail
// to start at all.
func TestSpawnEstablishesTheSessionAndResumeReusesIt(t *testing.T) {
	b := Compose(loadedRepo(t), demoTask(), Workspace{Path: "/w"})
	cur := claudeCursor("a-1")

	fresh, _ := claudeCommand(Dispatch{Briefing: b, Dir: "/w", Cursor: cur})
	if !strings.Contains(strings.Join(fresh.Args, " "), "--session-id "+cur) {
		t.Errorf("a first run must PIN the session: %v", fresh.Args)
	}
	revived, _ := claudeCommand(Dispatch{Briefing: b, Dir: "/w", Cursor: cur, Resuming: true})
	joined := strings.Join(revived.Args, " ")
	if !strings.Contains(joined, "--resume "+cur) {
		t.Errorf("a revival must RESUME it: %v", revived.Args)
	}
	if strings.Contains(joined, "--session-id") {
		t.Errorf("--session-id on a resume would try to establish a session that already exists: %v", revived.Args)
	}
}

// The approval posture maps one way: it narrows or it stays put. It never widens.
// The hinge is canAsk (the bridge is wired): WITHOUT it a worker cannot ask a
// human, so an "ask" posture cannot be honored and the only safe reading is the
// most restrictive mode (plan) — promoting to acceptEdits would be a worker that
// silently stopped asking permission. WITH the bridge those same postures run in
// default mode, where every non-safe tool call is delegated to the human's card.
func TestApprovalPostureNeverWidens(t *testing.T) {
	// No bridge: the ask postures cannot act, so they must not.
	for posture, want := range map[string]string{
		"yolo":      "bypassPermissions", // un-gated by choice; no card, confined to a lease
		"plan":      "plan",
		"ask":       "plan", // cannot ask -> must not act
		"workspace": "plan",
		"auto-edit": "plan",
	} {
		if got := claudePermissionMode(posture, false); got != want {
			t.Errorf("posture %q (no bridge) mapped to %q, want %q", posture, got, want)
		}
	}
	// Bridge wired: the ask postures can finally ask, so they run where the
	// permission tool is consulted.
	for posture, want := range map[string]string{
		"yolo":      "bypassPermissions", // still un-gated: the bridge is not consulted
		"plan":      "plan",
		"ask":       "",            // default mode -> every non-safe call hits the bridge
		"workspace": "",            // same
		"auto-edit": "acceptEdits", // edits auto; other tools hit the bridge
	} {
		if got := claudePermissionMode(posture, true); got != want {
			t.Errorf("posture %q (bridge wired) mapped to %q, want %q", posture, got, want)
		}
	}
	// An unknown posture says nothing and lets the child use its own default,
	// rather than guessing a permissive one on its behalf.
	if got := claudePermissionMode("a-posture-from-the-future", true); got != "" {
		t.Errorf("an unknown posture must not be guessed at, got %q", got)
	}
}

// TestClaudeCommandWiresTheApprovalBridge: when the runner served an approval
// socket (Dispatch.ApprovalSocket set), claudeCommand registers `terva
// mcp-approval-bridge` as a stdio MCP server and delegates permissions to it,
// and an ask posture runs in default mode (no --permission-mode) so every
// non-safe tool call reaches the bridge. The flag shapes were grounded against
// claude 2.1.210.
func TestClaudeCommandWiresTheApprovalBridge(t *testing.T) {
	b := Compose(loadedRepo(t), demoTask(), Workspace{Path: "/w"})
	b.Policy = Policy{Posture: "workspace"}
	cmd, err := claudeCommand(Dispatch{Briefing: b, Dir: "/w", ApprovalSocket: "/lease/wt/in.ap"})
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(cmd.Args, " ")
	for _, want := range []string{
		"--mcp-config", "mcp-approval-bridge", "--socket", "/lease/wt/in.ap",
		"--strict-mcp-config", "--permission-prompt-tool " + mcpbridge.PermissionToolRef,
	} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv missing %q:\n%s", want, argv)
		}
	}
	// The bridge command is registered under the server name the tool ref uses.
	if !strings.Contains(argv, mcpbridge.ServerName) {
		t.Errorf("mcp-config must register the %q server: %s", mcpbridge.ServerName, argv)
	}
	// ask/workspace WITH the bridge = default mode, so NO plan downgrade — the
	// whole point of 2b-ii is that these postures can finally act by asking.
	if strings.Contains(argv, "--permission-mode") {
		t.Errorf("a workspace posture with the bridge must run in default mode, got: %s", argv)
	}
}

// TestClaudeCommandNoBridgeFallsBackToPlan: with no served socket the worker
// cannot ask, so claudeCommand adds no bridge and an ask posture degrades to
// plan — think and read, never act. The safe fallback.
func TestClaudeCommandNoBridgeFallsBackToPlan(t *testing.T) {
	b := Compose(loadedRepo(t), demoTask(), Workspace{Path: "/w"})
	b.Policy = Policy{Posture: "workspace"}
	cmd, err := claudeCommand(Dispatch{Briefing: b, Dir: "/w"}) // no ApprovalSocket
	if err != nil {
		t.Fatal(err)
	}
	argv := strings.Join(cmd.Args, " ")
	if strings.Contains(argv, "mcp-approval-bridge") || strings.Contains(argv, "--permission-prompt-tool") {
		t.Errorf("no socket must mean no bridge wiring, got: %s", argv)
	}
	if !strings.Contains(argv, "--permission-mode plan") {
		t.Errorf("a workspace posture without a bridge must fall back to plan, got: %s", argv)
	}
}

// Model ids are not portable. terva may be pointed at a Kimi or a GPT, and
// neither name means anything to this CLI — so we DECLINE to translate rather
// than inventing a mapping, and let the worker use its own default.
func TestOnlyAnUnambiguousModelFamilyCrosses(t *testing.T) {
	for terva, want := range map[string]string{
		"claude-opus-4-8":             "opus",
		"claude-sonnet-5":             "sonnet",
		"claude-haiku-4-5-20251001":   "haiku",
		"gpt-5.6-sol":                 "", // means nothing over there
		"kimi-k2":                     "",
		"some-local-model-via-ollama": "",
		"":                            "",
	} {
		if got := claudeModel(terva); got != want {
			t.Errorf("model %q mapped to %q, want %q", terva, got, want)
		}
	}
}

func TestSteerFrameIsAUserTurn(t *testing.T) {
	frame, err := steerClaude("also update the changelog")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(frame), "\n") {
		t.Error("a stream-json frame is a LINE; without the newline the child never sees it")
	}
	for _, want := range []string{`"type":"user"`, `"role":"user"`, "also update the changelog"} {
		if !strings.Contains(string(frame), want) {
			t.Errorf("steer frame missing %q: %s", want, frame)
		}
	}
}
