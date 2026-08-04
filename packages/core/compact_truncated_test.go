package core

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// truncatedText is a summary the model was still writing when it ran into
// max_tokens: text arrived, no error arrived, and the stop reason is the only
// thing that says the account is unfinished.
func truncatedText(text string, input int) []provider.Event {
	return []provider.Event{
		provider.EventStart{Provider: "scripted"},
		provider.EventTextDelta{Delta: text},
		provider.EventUsage{Usage: provider.Usage{InputTokens: input, OutputTokens: 4096}},
		provider.EventDone{Stop: provider.StopLength, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: text}},
		}},
	}
}

// The defect: a summary cut off at the cap was indistinguishable from a whole
// one. The warm path accepted anything that was "no error, some text", so a
// checkpoint that stops mid-sentence became the conversation's entire memory
// with nothing anywhere recording that it was incomplete.
//
// Measured on a dogfooded session: three of ten compactions reported exactly
// 4096 output tokens and ended mid-word — one mid-`**` — and the only way to
// know was to notice output_tokens equalling the cap exactly.
func TestATruncatedWarmSummaryIsRecordedNotHidden(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		if n == 0 {
			return saidText("hi", 50), nil
		}
		return truncatedText("## Goal\nship it\n\n## Progress\nthe build broke because **", 100), nil
	}}
	a := cacheAwareAgent(t, client)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	// Append AFTER the turn: Compact's warm arm only runs when a prefix was
	// actually dispatched, and SetMessages alone dispatches nothing.
	a.SetMessages(append(a.Messages(),
		call("1", "bash", `{"command":"npm install"}`), result("1", "ok", false),
		provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "keep going"}}},
	))

	res, err := a.Compact(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Kept, not discarded: most of a checkpoint beats none, and re-running the
	// summarization pays the transcript-sized read again.
	if res.Summary == "" {
		t.Fatal("the truncated summary was thrown away; a partial checkpoint is still the best one available")
	}
	if !res.Truncated {
		t.Error("CompactResult.Truncated is false for a summary that stopped at the cap")
	}

	msgs := a.Messages()
	if len(msgs) == 0 {
		t.Fatal("no transcript after compaction")
	}
	tb, _ := msgs[0].Content[0].(provider.TextBlock)
	if !strings.Contains(tb.Text, "stops early") {
		t.Errorf("the checkpoint does not tell the model its own summary is incomplete:\n%s", tb.Text)
	}
	// The notice points at the ledger because the ledger is the one part of the
	// checkpoint a token limit cannot cut short — the harness appends it after
	// generation.
	if !strings.Contains(tb.Text, "npm install") {
		t.Errorf("the truncation notice claims the tool-call list is complete, but there is no list:\n%s", tb.Text)
	}
}

// The cold path discarded its stop reason outright. It is the FALLBACK, so it
// runs for compactions that already went wrong once — the least safe place to
// lose the signal that this attempt ended early too.
func TestATruncatedColdSummaryIsRecorded(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		switch n {
		case 0: // the turn
			return saidText("hi", 50), nil
		case 1: // warm: answered with a tool call, so the cold path takes over
			return calledATool(100), nil
		default: // cold, and it runs into the cap
			return truncatedText("## Goal\nship it, and then", 100), nil
		}
	}}
	a := cacheAwareAgent(t, client)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	// Append AFTER the turn: Compact's warm arm only runs when a prefix was
	// actually dispatched, and SetMessages alone dispatches nothing.
	a.SetMessages(append(a.Messages(),
		call("1", "bash", `{"command":"npm install"}`), result("1", "ok", false),
		provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "keep going"}}},
	))

	res, err := a.Compact(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Strategy != CompactWarmFellBack {
		t.Fatalf("Strategy = %q; want %q — this test only means something if the COLD path produced the summary", res.Strategy, CompactWarmFellBack)
	}
	if !res.Truncated {
		t.Error("a cold summary that stopped at the cap was recorded as complete")
	}
}

// A read-only stretch produces no ledger, so the notice must not promise a list
// of tool calls that is not there. The first version of this did — it was
// written against a transcript that always had one, and it would have told the
// model to trust a section the checkpoint does not contain.
func TestTheTruncationNoticePromisesNoLedgerItDoesNotHave(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		if n == 0 {
			return saidText("hi", 50), nil
		}
		return truncatedText("## Goal\nship it, and then", 100), nil
	}}
	a := cacheAwareAgent(t, client)
	a.ReadOnly = NewReadOnlySet("read")
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	// Only a READ, so the ledger comes back empty.
	a.SetMessages(append(a.Messages(),
		call("1", "read", `{"path":"a.go"}`), result("1", "ok", false),
		provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "keep going"}}},
	))

	res, err := a.Compact(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("setup wrong: this test needs a truncated summary to say anything")
	}
	tb, _ := a.Messages()[0].Content[0].(provider.TextBlock)
	if !strings.Contains(tb.Text, "stops early") {
		t.Fatalf("no truncation notice at all:\n%s", tb.Text)
	}
	if strings.Contains(tb.Text, "Executed Tool Calls") {
		t.Fatalf("setup wrong: a ledger appeared, so this is not the no-ledger case:\n%s", tb.Text)
	}
	if strings.Contains(tb.Text, "list of tool calls below") {
		t.Errorf("the notice points at a list the checkpoint does not contain:\n%s", tb.Text)
	}
}

// The negative control. Without it every assertion above passes on a constant
// true, and the flag would mark every compaction truncated forever.
func TestACompleteSummaryIsNotMarkedTruncated(t *testing.T) {
	client := &scriptedClient{name: "scripted", script: func(n int, req provider.Request) ([]provider.Event, error) {
		if n == 0 {
			return saidText("hi", 50), nil
		}
		return saidText("## Goal\nship it\n\n## Progress\ndone", 100), nil
	}}
	a := cacheAwareAgent(t, client)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	// Append AFTER the turn: Compact's warm arm only runs when a prefix was
	// actually dispatched, and SetMessages alone dispatches nothing.
	a.SetMessages(append(a.Messages(),
		call("1", "bash", `{"command":"npm install"}`), result("1", "ok", false),
		provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "keep going"}}},
	))

	res, err := a.Compact(context.Background(), 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Truncated {
		t.Error("a summary that ended on its own was marked truncated")
	}
	msgs := a.Messages()
	tb, _ := msgs[0].Content[0].(provider.TextBlock)
	if strings.Contains(tb.Text, "stops early") {
		t.Errorf("a complete checkpoint carries the truncation notice anyway:\n%s", tb.Text)
	}
}
