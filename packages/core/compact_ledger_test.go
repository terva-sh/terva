package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

func call(id, name, args string) provider.Message {
	return provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.ToolCallBlock{ID: id, Name: name, Arguments: json.RawMessage(args)}},
	}
}

func result(id, text string, isErr bool) provider.Message {
	return provider.Message{
		Role: provider.RoleTool,
		Content: []provider.Content{provider.ToolResultBlock{
			CallID: id, IsError: isErr,
			Content: []provider.Content{provider.TextBlock{Text: text}},
		}},
	}
}

// The ledger records what MUTATED and skips what merely looked. Reads are
// idempotent — re-running one costs a few tokens; re-running `npm install` or a
// migration costs the user something real.
func TestLedgerRecordsStateChangingCallsAndSkipsReads(t *testing.T) {
	ro := NewReadOnlySet("read", "grep")
	msgs := []provider.Message{
		call("1", "read", `{"path":"a.go"}`), result("1", "ok", false),
		call("2", "bash", `{"command":"npm install"}`), result("2", "added 400 packages", false),
		call("3", "grep", `{"pattern":"func"}`), result("3", "12 hits", false),
		call("4", "write", `{"path":"pkg/auth.go"}`), result("4", "written", false),
	}

	led := executedActionsLedger(msgs, ro, "")
	if !strings.Contains(led, "npm install") || !strings.Contains(led, "pkg/auth.go") {
		t.Errorf("the ledger dropped a state-changing call:\n%s", led)
	}
	if strings.Contains(led, "read ") || strings.Contains(led, "grep ") {
		t.Errorf("the ledger listed a read-only call; reads are safe to repeat and only cost context:\n%s", led)
	}
}

// The case that is easiest to get backwards, and the most dangerous to.
//
// A FAILED call's effect does NOT exist. An agent told "you already ran this"
// about a command that aborted will skip work it still has to do — the mirror
// image of the bug the ledger exists to prevent, and just as bad. And a call with
// no result at all was dispatched into the dark: compaction can land between the
// call and its result, and neither "it ran" nor "it didn't" is safe to assert.
func TestLedgerDistinguishesFailedAndUnresolvedCalls(t *testing.T) {
	msgs := []provider.Message{
		call("1", "bash", `{"command":"npm install"}`), result("1", "ok", false),
		call("2", "bash", `{"command":"rm -rf build"}`), result("2", "ENOENT", true),
		call("3", "bash", `{"command":"deploy staging"}`), // dispatched; compaction landed here
	}

	led := executedActionsLedger(msgs, nil, "")

	lines := map[string]string{}
	for _, l := range strings.Split(led, "\n") {
		switch {
		case strings.Contains(l, "npm install"):
			lines["ok"] = l
		case strings.Contains(l, "rm -rf build"):
			lines["failed"] = l
		case strings.Contains(l, "deploy staging"):
			lines["unresolved"] = l
		}
	}

	if strings.Contains(lines["ok"], "FAILED") || strings.Contains(lines["ok"], "UNKNOWN") {
		t.Errorf("a successful call was flagged: %q", lines["ok"])
	}
	if !strings.Contains(lines["failed"], "FAILED") {
		t.Errorf("a failed call must say so, or the agent skips work it still owes: %q", lines["failed"])
	}
	if !strings.Contains(lines["unresolved"], "OUTCOME UNKNOWN") {
		t.Errorf("a call with no recorded result must not be claimed as done: %q", lines["unresolved"])
	}
}

// A shell command is not one action, and its failure does not mean nothing
// happened. The exit status belongs to the LAST stage, so a command that failed
// can have changed the workspace several times on the way there.
//
// Measured on a dogfooded session: 12 bash calls were marked failed in
// compaction ledgers and all 12 were composite — six of them
// `gofmt -w <file> && go test <pkg>`, where the rewrite certainly happened and
// only the test after it failed. Each was recorded as "its effect does NOT
// exist", which is the ledger's own worst-case claim aimed the wrong way.
func TestLedgerDoesNotClaimAFailedShellCommandDidNothing(t *testing.T) {
	msgs := []provider.Message{
		call("1", "bash", `{"command":"gofmt -w pkg/auth_test.go && go test ./pkg"}`), result("1", "FAIL", true),
		call("2", "edit", `{"path":"pkg/auth.go"}`), result("2", "oldText not found", true),
	}

	led := executedActionsLedger(msgs, nil, "")
	var shell, edit string
	for _, l := range strings.Split(led, "\n") {
		switch {
		case strings.Contains(l, "gofmt -w"):
			shell = l
		case strings.Contains(l, "pkg/auth.go"):
			edit = l
		}
	}

	if !strings.Contains(shell, "FAILED") {
		t.Fatalf("a failed shell command must still be marked failed: %q", shell)
	}
	if strings.Contains(shell, "does NOT exist") {
		t.Errorf("the ledger claims a composite shell command had no effect; the `gofmt -w` in it ran: %q", shell)
	}
	if !strings.Contains(shell, "may still have taken effect") {
		t.Errorf("the shell note does not warn that earlier stages ran: %q", shell)
	}
	// The ordinary case must NOT drift: for a tool that either applies or does
	// not, "nothing happened" is exactly right, and softening it would invite
	// the resuming agent to skip work it still owes.
	if !strings.Contains(edit, "does NOT exist") {
		t.Errorf("a failed edit writes no bytes and the ledger must keep saying so: %q", edit)
	}
}

// nil ReadOnly means "assume everything mutates". Over-reporting costs tokens;
// under-reporting invites a repeated side effect. Extensions and MCP servers
// register arbitrary tools, so an unknown tool is the common case.
func TestLedgerFailsClosedWithoutAReadOnlySet(t *testing.T) {
	msgs := []provider.Message{call("1", "some_mcp_tool", `{"x":1}`), result("1", "ok", false)}
	if led := executedActionsLedger(msgs, nil, ""); !strings.Contains(led, "some_mcp_tool") {
		t.Errorf("an unknown tool must be assumed state-changing:\n%s", led)
	}
}

// Bounded, and loudly. The ledger rides in the compacted transcript forever
// after, so an unbounded one spends the context the compaction just reclaimed.
// A silent cap would read as "this is everything" — the one thing it must never
// claim falsely.
func TestLedgerIsBoundedAndSaysWhenItTruncates(t *testing.T) {
	const overflow = 15
	total := ledgerMaxEntries + overflow
	var msgs []provider.Message
	for i := 0; i < total; i++ {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs, call(id, "bash", fmt.Sprintf(`{"command":"step-%d"}`, i)), result(id, "ok", false))
	}
	led := executedActionsLedger(msgs, nil, "")

	entries := strings.Count(led, "\n- ")
	if entries > ledgerMaxEntries {
		t.Errorf("ledger listed %d entries; capped at %d", entries, ledgerMaxEntries)
	}
	if !strings.Contains(led, fmt.Sprintf("omits %d earlier calls that changed state", overflow)) {
		t.Errorf("the ledger truncated silently, which reads as completeness:\n%s", led)
	}
	// The most recent survive — they are the likeliest to be re-attempted the
	// instant the agent resumes — and the oldest, past the cap, do not. Derived
	// from the cap so this keeps testing "keeps the newest" if the cap moves.
	if newest := fmt.Sprintf("step-%d", total-1); !strings.Contains(led, newest) {
		t.Errorf("the newest action (%s) was dropped:\n%s", newest, led)
	}
	if strings.Contains(led, `step-0"`) {
		t.Errorf("the oldest action (step-0) survived past the cap; overflow should drop the front:\n%s", led)
	}
}

// The cap is sized from evidence, not taste. A real dogfood session ran 88
// distinct state-changing calls before its first compaction; a cap below that
// pushes more than half of a heavy session's actions into prose the model has to
// remember, which is the exact reliance the ledger exists to remove. Pin the
// intent so a future "tidy up the constants" pass can't quietly undo it.
func TestLedgerCapCoversAHeavyRealSession(t *testing.T) {
	const observedHeaviestSession = 88
	if ledgerMaxEntries < observedHeaviestSession {
		t.Errorf("ledgerMaxEntries = %d, below the %d distinct calls a real session produced before one "+
			"compaction — over half its actions would fall to prose-only", ledgerMaxEntries, observedHeaviestSession)
	}
}

// Repetition collapses. An agent that edited one file eleven times produces one
// line and a count, not eleven lines.
func TestLedgerCollapsesRepeatedIdenticalCalls(t *testing.T) {
	var msgs []provider.Message
	for i := 0; i < 11; i++ {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs, call(id, "write", `{"path":"a.go"}`), result(id, "ok", false))
	}
	led := executedActionsLedger(msgs, nil, "")
	if strings.Count(led, "\n- ") != 1 {
		t.Errorf("identical calls did not collapse:\n%s", led)
	}
	if !strings.Contains(led, "(x11)") {
		t.Errorf("the repeat count is missing:\n%s", led)
	}
}

// Arguments are clipped, on a rune boundary — a multi-byte argument cut in half
// would put invalid UTF-8 into the transcript.
func TestLedgerClipsLongArgumentsSafely(t *testing.T) {
	long := strings.Repeat("é", 400)
	msgs := []provider.Message{call("1", "bash", `{"command":"`+long+`"}`), result("1", "ok", false)}
	led := executedActionsLedger(msgs, nil, "")
	if !strings.Contains(led, "…") {
		t.Error("a long argument was not clipped")
	}
	if !strings.ContainsRune(led, 'é') {
		t.Error("the clipped argument lost its content entirely")
	}
	for _, r := range led {
		if r == '�' {
			t.Fatal("clipping produced invalid UTF-8 (cut a rune in half)")
		}
	}
}

// The preamble elision, and the reason it exists: a `cd` into the directory the
// command already runs in identifies nothing, and it lands in front of the clip.
//
// Measured on a dogfooded session — 1,090 of 1,112 `cd`s pointed at the agent's
// own cwd, eating 92 of the 160 characters the ledger allots to naming a call.
// The assertion is the one that matters: after eliding, the clip reaches the
// command. Before it, the entry named no command at all.
func TestLedgerElidesTheCdThatGoesNowhere(t *testing.T) {
	const cwd = "/Users/dev/Workspace/git.example.com/someone/a-project"
	cmd := "cd " + cwd + "\\nset +e\\ngo test ./internal/world/spec -run TestContestResolution"
	msgs := []provider.Message{call("1", "bash", `{"command":"`+cmd+`"}`), result("1", "ok", false)}

	// Scope to the entries. The header legitimately NAMES `set +e` when it
	// explains the elision, so a Contains over the whole ledger matches the
	// chrome and reports a failure that is not there.
	entries := ledgerEntries(executedActionsLedger(msgs, nil, cwd))

	if strings.Contains(entries, cwd) {
		t.Errorf("the no-op cd survived into the entry:\n%s", entries)
	}
	if strings.Contains(entries, "set +e") {
		t.Errorf("the no-op `set +e` survived into the entry:\n%s", entries)
	}
	// The point of the whole exercise.
	if !strings.Contains(entries, "TestContestResolution") {
		t.Errorf("the clip still fell inside the preamble; the entry names no command:\n%s", entries)
	}
}

// ledgerEntries returns only the "- " action lines, dropping the heading. The
// heading discusses the elision in prose, so an assertion about what an ENTRY
// contains has to be made against the entries.
func ledgerEntries(led string) string {
	var out []string
	for _, l := range strings.Split(led, "\n") {
		if strings.HasPrefix(l, "- ") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// The other direction, and the one that must never break: a `cd` somewhere else
// changed WHERE the work happened. Eliding it would misreport the action — a
// `make` in /tmp/build is not the `make` this ledger would then claim ran.
func TestLedgerKeepsACdThatMoved(t *testing.T) {
	const cwd = "/srv/app"
	for _, tc := range []struct{ name, command, keep string }{
		{"elsewhere", "cd /tmp/build && make install", "/tmp/build"},
		// Shares a prefix with cwd. Requiring a separator after the match is what
		// stops HasPrefix from rewriting this into a command that never ran.
		{"prefix-similar", "cd /srv/app2 && make install", "/srv/app2"},
		// Same, one character the other way.
		{"trailing-slash", "cd /srv/app/ && make install", "/srv/app/"},
		// Not a statement: `set +export` merely starts like `set +e`.
		{"set-lookalike", "set +export FOO=1; make", "+export"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msgs := []provider.Message{call("1", "bash", `{"command":"`+tc.command+`"}`), result("1", "ok", false)}
			if led := executedActionsLedger(msgs, nil, cwd); !strings.Contains(led, tc.keep) {
				t.Errorf("elided %q, which changes what the entry claims ran:\n%s", tc.keep, led)
			}
		})
	}
}

// Elision is bash-only and cwd-gated. A path-shaped argument to another tool is
// not a shell command, and an agent with no configured cwd has nothing to
// compare against — both must pass through byte-identical.
func TestLedgerElidesOnlyForBashAndOnlyWithACWD(t *testing.T) {
	const cwd = "/srv/app"
	msgs := []provider.Message{call("1", "write", `{"path":"/srv/app","body":"x"}`), result("1", "ok", false)}
	if led := executedActionsLedger(msgs, nil, cwd); !strings.Contains(led, `"/srv/app"`) {
		t.Errorf("a non-bash argument was rewritten:\n%s", led)
	}

	msgs = []provider.Message{call("1", "bash", `{"command":"cd /srv/app && make"}`), result("1", "ok", false)}
	if led := executedActionsLedger(msgs, nil, ""); !strings.Contains(led, "cd /srv/app") {
		t.Errorf("an unset cwd elided anyway, which cannot be verified as a no-op:\n%s", led)
	}
}

// A read-only stretch of conversation needs no ledger, and must not get an empty
// heading that implies actions were taken.
func TestLedgerIsEmptyWhenNothingMutated(t *testing.T) {
	ro := NewReadOnlySet("read")
	msgs := []provider.Message{call("1", "read", `{"path":"a.go"}`), result("1", "ok", false)}
	if led := executedActionsLedger(msgs, ro, ""); led != "" {
		t.Errorf("a read-only stretch produced a ledger:\n%s", led)
	}
}

// End to end: the ledger reaches the compacted transcript, riding the synthetic
// summary message — which is the only thing that survives the conversation.
func TestCompactionAttachesTheLedgerToTheSummary(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		// A summary that "forgets" the install — exactly the failure the ledger
		// exists to make impossible.
		return saidText("## Goal\nrefactor auth\n\n## Actions Already Executed\n- (none)", 100), nil
	}}
	a := cacheAwareAgent(t, client)
	a.ReadOnly = NewReadOnlySet("read")
	a.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "refactor auth"}}},
		call("1", "bash", `{"command":"npm install"}`), result("1", "ok", false),
		call("2", "write", `{"path":"pkg/auth.go"}`), result("2", "written", false),
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "keep going"}}},
	})

	if _, err := a.Compact(context.Background(), 0, nil); err != nil {
		t.Fatal(err)
	}

	msgs := a.Messages()
	if len(msgs) == 0 {
		t.Fatal("no transcript after compaction")
	}
	tb, _ := msgs[0].Content[0].(provider.TextBlock)
	if !strings.Contains(tb.Text, "npm install") || !strings.Contains(tb.Text, "pkg/auth.go") {
		t.Errorf("the model's summary forgot the executed actions and the harness did not correct it:\n%s", tb.Text)
	}
	if !strings.Contains(tb.Text, "Do not repeat any of them") {
		t.Errorf("the ledger reached the transcript without its instruction:\n%s", tb.Text)
	}
}
