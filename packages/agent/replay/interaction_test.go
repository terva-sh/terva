package replay

import (
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/session"
	"terva.sh/terva/packages/testsupport"
)

// The synth plays an exchange as its prompt, then its resolution after the
// recorded wait, clamped to [Think, WaitCap] so the pause is visible and
// bounded.
func TestSynthesizeInteractions(t *testing.T) {
	rows := []session.ReplayRow{
		{Kind: session.ReplayRowMessage, Message: msg(provider.RoleUser, provider.TextBlock{Text: "run the tests"})},
		{Kind: session.ReplayRowPermission, Permission: session.PermissionRecord{CallID: "c1", Tool: "bash", Waited: 10 * time.Minute, Allow: true}},
		{Kind: session.ReplayRowAsk, Ask: session.AskRecord{AskID: "a1", Questions: []session.RecordQuestion{{Question: "which?"}}, Answers: []session.RecordAnswer{{Answer: "b"}}, Waited: time.Millisecond}},
	}
	pace := DefaultPace()
	frames := Synthesize(rows, Options{Pace: pace})
	var got []string
	byType := map[string]Frame{}
	for _, f := range frames {
		got = append(got, f.Event.Type())
		byType[f.Event.Type()] = f
	}
	want := []string{"user_message", "permission_request", "permission_resolved", "ask_request", "ask_resolved", "done"}
	if len(got) != len(want) {
		t.Fatalf("frames %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("frames %v, want %v", got, want)
		}
	}
	if d := byType["permission_resolved"].Delay; d != pace.WaitCap {
		t.Errorf("a ten-minute wait plays as %v, want the cap %v", d, pace.WaitCap)
	}
	if d := byType["ask_resolved"].Delay; d != pace.Think {
		t.Errorf("a millisecond wait plays as %v, want the floor %v", d, pace.Think)
	}
	if e := byType["permission_request"].Event.(EvPermissionRequest); e.CallID != "c1" || e.Tool != "bash" {
		t.Errorf("request event: %+v", e)
	}
	if e := byType["ask_resolved"].Event.(EvAskResolved); len(e.Answers) != 1 || e.Answers[0].Answer != "b" {
		t.Errorf("resolved event: %+v", e)
	}
}

// The carrier turns the four replay-only events into the wire events a live
// client already renders, in order: a transcript with one approval and one
// question shows both beats to a subscriber.
func TestCarrierPlaysInteractionsOnTheWire(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "s.jsonl")
	sess, err := session.NewSessionAtPath(path, "/cwd", "prov", "model", "v1")
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(sess.AppendMessage(provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "go"}}}))
	must(sess.AppendPermission(session.PermissionRecord{CallID: "c1", Tool: "bash", Preview: "go test", Waited: time.Millisecond, Allow: true}))
	must(sess.AppendAsk(session.AskRecord{AskID: "a1", Questions: session.RecordQuestions([]core.UserQuestion{{Question: "which?", Options: []string{"a", "b"}}}), Answers: session.RecordAnswers([]core.UserAnswer{{Answer: "b"}}), Waited: time.Millisecond}))
	must(sess.Close())

	fast := DefaultPace()
	fast.Think, fast.WaitCap = time.Millisecond, time.Millisecond
	c, err := Open(path, Options{Pace: fast, Autoplay: true})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ch, err := c.Subscribe(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	deadline := time.After(5 * time.Second)
	for len(seen) < 4 {
		select {
		case ev := <-ch:
			switch ev.Type {
			case ctrlproto.EventPermissionRequest:
				if ev.Permission == nil || ev.Permission.CallID != "c1" || ev.Permission.Tool != "bash" {
					t.Fatalf("permission event: %+v", ev.Permission)
				}
				seen = append(seen, ev.Type)
			case ctrlproto.EventPermissionResolved:
				if ev.Resolved == nil || ev.Resolved.CallID != "c1" {
					t.Fatalf("resolved event: %+v", ev.Resolved)
				}
				seen = append(seen, ev.Type)
			case ctrlproto.EventAskRequest:
				if ev.Ask == nil || ev.Ask.AskID != "a1" || len(ev.Ask.Questions) != 1 {
					t.Fatalf("ask event: %+v", ev.Ask)
				}
				seen = append(seen, ev.Type)
			case ctrlproto.EventAskResolved:
				if ev.Resolved == nil || ev.Resolved.AskID != "a1" {
					t.Fatalf("ask resolved: %+v", ev.Resolved)
				}
				seen = append(seen, ev.Type)
			}
		case <-deadline:
			t.Fatalf("saw only %v before the deadline", seen)
		}
	}
	want := []string{ctrlproto.EventPermissionRequest, ctrlproto.EventPermissionResolved, ctrlproto.EventAskRequest, ctrlproto.EventAskResolved}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("order %v, want %v", seen, want)
		}
	}
}

// The resolution names the option that won, so a client can play the answer
// as keystrokes: a refusal is the fifth row of the confirm dialog, a
// session-wide allow the second, and a recorded answer its position in the
// question's list with its note.
func TestCarrierNamesTheChosenOption(t *testing.T) {
	if got := permissionOption(EvPermissionResolved{Allow: false}); got != 5 {
		t.Errorf("a refusal is option %d, want 5", got)
	}
	if got := permissionOption(EvPermissionResolved{Allow: true, Scope: session.PermissionScopeTool}); got != 2 {
		t.Errorf("a session-wide allow is option %d, want 2", got)
	}
	if got := permissionOption(EvPermissionResolved{Allow: true}); got != 1 {
		t.Errorf("a plain allow is option %d, want 1", got)
	}
	qs := []core.UserQuestion{{Question: "which?", Options: []string{"a", "b", "c"}}}
	if opt, note := askOption(qs, []core.UserAnswer{{Answer: "b", Note: "why"}}); opt != 2 || note != "why" {
		t.Errorf("answer b = option %d note %q, want 2 and why", opt, note)
	}
	if opt, _ := askOption(qs, []core.UserAnswer{{Answer: "typed my own"}}); opt != 0 {
		t.Errorf("a typed answer is option %d, want 0 so the client dismisses instead", opt)
	}
	if opt, _ := askOption(append(qs, qs[0]), []core.UserAnswer{{Answer: "a"}, {Answer: "a"}}); opt != 0 {
		t.Errorf("a two-question set is option %d, want 0", opt)
	}
}

// The live loop emits EvUsage for the session's own requests only; a
// sub-agent's row and a host side-channel row are booked total-only and never
// reach the event stream. A replay that emitted them moved the gauge on a turn
// the session never had.
func TestSynthesizeSkipsSideChannelAndDelegatedUsage(t *testing.T) {
	rows := []session.ReplayRow{
		{Kind: session.ReplayRowMessage, Message: msg(provider.RoleUser, provider.TextBlock{Text: "hi"})},
		{Kind: session.ReplayRowUsage, Usage: provider.Usage{InputTokens: 10}, Cumulative: provider.Usage{InputTokens: 10}},
		{Kind: session.ReplayRowUsage, Usage: provider.Usage{InputTokens: 5}, Cumulative: provider.Usage{InputTokens: 15}, Source: "next_step"},
		{Kind: session.ReplayRowUsage, Usage: provider.Usage{InputTokens: 900}, Cumulative: provider.Usage{InputTokens: 915}, Delegated: true},
	}
	var usage []core.EvUsage
	for _, f := range Synthesize(rows, Options{}) {
		if e, ok := f.Event.(core.EvUsage); ok {
			usage = append(usage, e)
		}
	}
	if len(usage) != 1 || usage[0].Usage.InputTokens != 10 {
		t.Fatalf("usage frames = %+v, want exactly the session's own row", usage)
	}
}
