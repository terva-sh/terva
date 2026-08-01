package core

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

func msgText(role provider.Role, text string) provider.Message {
	return provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

func ladderOf(sys string, msgs ...provider.Message) prefixLadder {
	return buildPrefixLadder(nil, sys, msgs)
}

// The healthy case, and the one that must never fire: a request that only
// appends. This is every ordinary turn, so a false positive here would bury the
// real signal under one row per request.
func TestPrefixLadderAppendIsNotADivergence(t *testing.T) {
	a := ladderOf("sys", msgText(provider.RoleUser, "one"))
	b := ladderOf("sys", msgText(provider.RoleUser, "one"), msgText(provider.RoleAssistant, "two"))

	d := comparePrefixLadders(a, b)
	if !d.Appended {
		t.Fatalf("an append was reported as a divergence at rung %d (%s)", d.Rung, d.Label)
	}
	if d.Mutation() {
		t.Error("Mutation() true for a pure append")
	}
}

// The case this exists for: the same conversation, same message count, one
// EARLIER message rebuilt into different bytes. Invisible in the transcript,
// fatal to the cache — a provider re-reads everything from that point.
func TestPrefixLadderCatchesARewrittenEarlyMessage(t *testing.T) {
	a := ladderOf("sys",
		msgText(provider.RoleUser, "one"),
		msgText(provider.RoleAssistant, "two"),
		msgText(provider.RoleUser, "three"))
	b := ladderOf("sys",
		msgText(provider.RoleUser, "one"),
		msgText(provider.RoleAssistant, "TWO-rebuilt"),
		msgText(provider.RoleUser, "three"))

	d := comparePrefixLadders(a, b)
	if !d.Mutation() {
		t.Fatal("a rewritten early message was not reported as a mutation")
	}
	// Rung 0 route, 1 tools, 2 system, so message 1 is rung 4.
	if d.Label != "message 1" {
		t.Errorf("label = %q, want the specific message; a bare 'something changed' is a hunt, not a fact", d.Label)
	}
	if d.MsgCount != 3 || d.PrevMsgCount != 3 {
		t.Errorf("counts = %d/%d, want 3/3 — the ratio is what says how far back the rewrite reached", d.MsgCount, d.PrevMsgCount)
	}
}

// The prefix is more than the messages. A changed tool set or system prompt
// invalidates from its own rung, and each must be named rather than blamed on
// the first message after it.
func TestPrefixLadderNamesToolsAndSystem(t *testing.T) {
	base := buildPrefixLadder(
		[]provider.Tool{{Name: "read", Description: "d", Schema: []byte(`{}`)}},
		"sys", []provider.Message{msgText(provider.RoleUser, "one")})

	sysChanged := buildPrefixLadder(
		[]provider.Tool{{Name: "read", Description: "d", Schema: []byte(`{}`)}},
		"sys CHANGED", []provider.Message{msgText(provider.RoleUser, "one")})
	if d := comparePrefixLadders(base, sysChanged); d.Label != "system" {
		t.Errorf("system change reported as %q", d.Label)
	}

	toolsChanged := buildPrefixLadder(
		[]provider.Tool{{Name: "read", Description: "DIFFERENT", Schema: []byte(`{}`)}},
		"sys", []provider.Message{msgText(provider.RoleUser, "one")})
	if d := comparePrefixLadders(base, toolsChanged); d.Label != "tools" {
		t.Errorf("tool change reported as %q", d.Label)
	}

	// Order is part of the payload: a reordered tools array is a real
	// invalidation, and normalizing it away would hide the likeliest cause.
	reordered := buildPrefixLadder(
		[]provider.Tool{
			{Name: "b", Description: "d", Schema: []byte(`{}`)},
			{Name: "a", Description: "d", Schema: []byte(`{}`)},
		}, "sys", nil)
	swapped := buildPrefixLadder(
		[]provider.Tool{
			{Name: "a", Description: "d", Schema: []byte(`{}`)},
			{Name: "b", Description: "d", Schema: []byte(`{}`)},
		}, "sys", nil)
	if comparePrefixLadders(reordered, swapped).Appended {
		t.Error("a reordered tools array compared equal; provider caches are byte-ordered")
	}
}

// A compaction shortens the transcript. That is expected and explicable — but it
// must still be RECORDED, because a reader needs the expected truncation on the
// timeline to tell it apart from the unexplained divergences that follow it.
func TestPrefixLadderReportsTruncation(t *testing.T) {
	long := ladderOf("sys",
		msgText(provider.RoleUser, "one"),
		msgText(provider.RoleAssistant, "two"),
		msgText(provider.RoleUser, "three"))
	short := ladderOf("sys", msgText(provider.RoleUser, "one"))

	d := comparePrefixLadders(long, short)
	if d.Appended {
		t.Fatal("a shortened transcript was reported as an append")
	}
	if d.Label != "truncated" {
		t.Errorf("label = %q, want truncated", d.Label)
	}
}

// End to end through a real turn loop: ordinary turns append and stay silent,
// and a transcript rewritten behind the agent's back fires exactly once with the
// message named.
func TestAgentReportsAPrefixMutationOnce(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "m", "sys", Registry{})
	a.SetPrefixDivergenceRecording(true) // core's zero value is off; build ships it on
	var got []PrefixDivergence
	a.AddPrefixDivergenceObserver(func(d PrefixDivergence) { got = append(got, d) })

	for i := 0; i < 3; i++ {
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("prompt %d: %v", i, err)
		}
	}
	if len(got) != 0 {
		t.Fatalf("ordinary appending turns reported %d divergence(s): %+v", len(got), got)
	}

	// Rewrite an early message in place — the shape a transcript rebuild takes.
	msgs := a.Messages()
	if len(msgs) < 2 {
		t.Fatalf("expected a transcript to rewrite, got %d messages", len(msgs))
	}
	msgs[0] = msgText(provider.RoleUser, "REBUILT")
	a.SetMessages(msgs)

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly one divergence, got %d: %+v", len(got), got)
	}
	if !got[0].Mutation() {
		t.Error("a rewritten transcript was not classified as a mutation")
	}
	if !strings.HasPrefix(got[0].Label, "message") {
		t.Errorf("label = %q, want the offending message", got[0].Label)
	}
}

// The economics a compaction is supposed to have: ONE invalidation, paid once,
// and then the cache rebuilds as turns append to the new baseline.
//
// A measured session says otherwise — every one of seven compactions was
// followed by a sustained collapse (83% mean hit before, 27% after, still
// degrading thirty requests later), which is not the shape of "pay once and
// rebuild". This asserts the intended shape so the guard reports where the
// extra invalidations come from.
func TestCompactionCostsExactlyOneInvalidation(t *testing.T) {
	// prefixSpyClient rather than reqCaptureClient: compactHeld rejects an empty
	// summary, and this one streams real text.
	client := &prefixSpyClient{name: "spy"}
	a := NewAgent(client, "m", "sys", Registry{})
	a.SetPrefixDivergenceRecording(true)
	var got []PrefixDivergence
	a.AddPrefixDivergenceObserver(func(d PrefixDivergence) { got = append(got, d) })

	for i := 0; i < 4; i++ {
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("pre-compaction prompt %d: %v", i, err)
		}
	}
	if len(got) != 0 {
		t.Fatalf("appending turns diverged before any compaction: %+v", got)
	}

	if _, err := a.Compact(context.Background(), 0, nil); err != nil {
		t.Fatalf("compact: %v", err)
	}

	for i := 0; i < 4; i++ {
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("post-compaction prompt %d: %v", i, err)
		}
	}

	if len(got) == 0 {
		t.Fatal("the compaction itself was not recorded; the expected invalidation must be on the timeline")
	}
	if len(got) > 1 {
		t.Errorf("compaction cost %d invalidations, want exactly 1 — the extras are the collapse:", len(got))
		for i, d := range got {
			t.Errorf("   #%d rung=%d %q  msgs %d -> %d", i+1, d.Rung, d.Label, d.PrevMsgCount, d.MsgCount)
		}
	}
}

// The toggle has to actually stop the work, not just the reporting: a user who
// turns a diagnostic off is asking not to pay for it.
func TestPrefixDivergenceRecordingCanBeTurnedOff(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "m", "sys", Registry{})
	a.SetPrefixDivergenceRecording(false)
	var got []PrefixDivergence
	a.AddPrefixDivergenceObserver(func(d PrefixDivergence) { got = append(got, d) })

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	msgs := a.Messages()
	msgs[0] = msgText(provider.RoleUser, "REBUILT")
	a.SetMessages(msgs)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("recording is off but %d divergence(s) fired: %+v", len(got), got)
	}
	// And no ladder was built, so turning it back on later starts clean rather
	// than comparing against a stale prefix.
	if a.PrefixDivergenceRecordingEnabled() {
		t.Error("PrefixDivergenceRecordingEnabled disagrees with the setter")
	}
}
