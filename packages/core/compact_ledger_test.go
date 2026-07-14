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

	led := executedActionsLedger(msgs, ro)
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

	led := executedActionsLedger(msgs, nil)

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

// nil ReadOnly means "assume everything mutates". Over-reporting costs tokens;
// under-reporting invites a repeated side effect. Extensions and MCP servers
// register arbitrary tools, so an unknown tool is the common case.
func TestLedgerFailsClosedWithoutAReadOnlySet(t *testing.T) {
	msgs := []provider.Message{call("1", "some_mcp_tool", `{"x":1}`), result("1", "ok", false)}
	if led := executedActionsLedger(msgs, nil); !strings.Contains(led, "some_mcp_tool") {
		t.Errorf("an unknown tool must be assumed state-changing:\n%s", led)
	}
}

// Bounded, and loudly. The ledger rides in the compacted transcript forever
// after, so an unbounded one spends the context the compaction just reclaimed.
// A silent cap would read as "this is everything" — the one thing it must never
// claim falsely.
func TestLedgerIsBoundedAndSaysWhenItTruncates(t *testing.T) {
	var msgs []provider.Message
	for i := 0; i < ledgerMaxEntries+15; i++ {
		id := fmt.Sprintf("c%d", i)
		msgs = append(msgs, call(id, "bash", fmt.Sprintf(`{"command":"step-%d"}`, i)), result(id, "ok", false))
	}
	led := executedActionsLedger(msgs, nil)

	entries := strings.Count(led, "\n- ")
	if entries > ledgerMaxEntries {
		t.Errorf("ledger listed %d entries; capped at %d", entries, ledgerMaxEntries)
	}
	if !strings.Contains(led, "15 earlier state-changing calls are not listed") {
		t.Errorf("the ledger truncated silently, which reads as completeness:\n%s", led)
	}
	// The most recent survive: they are the likeliest to be re-attempted the
	// instant the agent resumes.
	if !strings.Contains(led, "step-54") {
		t.Errorf("the newest action was dropped:\n%s", led)
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
	led := executedActionsLedger(msgs, nil)
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
	led := executedActionsLedger(msgs, nil)
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

// A read-only stretch of conversation needs no ledger, and must not get an empty
// heading that implies actions were taken.
func TestLedgerIsEmptyWhenNothingMutated(t *testing.T) {
	ro := NewReadOnlySet("read")
	msgs := []provider.Message{call("1", "read", `{"path":"a.go"}`), result("1", "ok", false)}
	if led := executedActionsLedger(msgs, ro); led != "" {
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
	if !strings.Contains(tb.Text, "Do NOT repeat any of them") {
		t.Errorf("the ledger reached the transcript without its instruction:\n%s", tb.Text)
	}
}
