package modes

import (
	"context"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/replay"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// walkKeys moves the cursor the way a hand does: to the row, one past when
// there is one, and back. A first-row answer still glances at the second.
func TestWalkKeys(t *testing.T) {
	kinds := func(keys []tui.Key) []tui.KeyKind {
		out := make([]tui.KeyKind, len(keys))
		for i, k := range keys {
			out[i] = k.Kind
		}
		return out
	}
	eq := func(a, b []tui.KeyKind) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	d, u := tui.KeyDown, tui.KeyUp
	for _, tc := range []struct {
		option, n int
		want      []tui.KeyKind
	}{
		{1, 5, []tui.KeyKind{d, u}},
		{2, 5, []tui.KeyKind{d, d, u}},
		{5, 5, []tui.KeyKind{d, d, d, d, u, d}},
		{1, 1, nil},
	} {
		if got := kinds(walkKeys(tc.option, tc.n)); !eq(got, tc.want) {
			t.Errorf("walkKeys(%d, %d) = %v, want %v", tc.option, tc.n, got, tc.want)
		}
		// The player holds the next frame for exactly this many keys.
		if got := len(walkKeys(tc.option, tc.n)); got != replay.WalkLength(tc.option, tc.n) {
			t.Errorf("walkKeys(%d, %d) has %d keys, replay.WalkLength says %d", tc.option, tc.n, got, replay.WalkLength(tc.option, tc.n))
		}
	}
}

// In a replay a resolution that names the option is played as keystrokes
// through the dialog, which resolves through its own channel with the
// decision that option carries; a resolution that names nothing, or one for
// a live carrier, still dismisses.
func TestReplayResolutionWalksTheDialog(t *testing.T) {
	old := dialogWalkStep
	dialogWalkStep, dialogWalkTypeStep = time.Millisecond, time.Millisecond
	defer func() { dialogWalkStep, dialogWalkTypeStep = old, replay.WalkTypeStep }()

	rc, err := replay.Open(writeReplayFixture(t), replay.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	c := &answerSpy{Carrier: rc, decisions: make(chan core.ConfirmDecision, 4), answers: make(chan []core.UserAnswer, 4)}
	i := newReplayTestInteractive(t, rc)
	i.cfg.Carrier = c
	i.actions = make(chan func(), 64)

	// Drain the main-loop queue until the dialog has resolved or time runs out.
	settle := func(active func() bool) {
		deadline := time.Now().Add(5 * time.Second)
		for active() && time.Now().Before(deadline) {
			select {
			case fn := <-i.actions:
				fn()
			case <-time.After(5 * time.Millisecond):
			}
		}
	}

	// A refusal walks to the fifth row and answers no.
	i.handleCarrierEvent(ctrlproto.PermissionEvent(ctrlproto.PermissionRequest{CallID: "c1", Tool: "bash"}))
	i.mu.Lock()
	cr := i.carrierPerm["c1"]
	i.mu.Unlock()
	ev := ctrlproto.PermissionResolvedEvent("c1")
	ev.Resolved.Option = 5
	i.handleCarrierEvent(ev)
	settle(i.confirmDialog.Active)
	_ = cr
	select {
	case d := <-c.decisions:
		if d.Allow {
			t.Fatalf("walking to option 5 answered %+v, want a refusal", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the walk did not answer through the dialog")
	}

	// A question: walk to the second option, type the note, answer.
	i.handleCarrierEvent(ctrlproto.AskEvent(ctrlproto.NewAskRequest("a1", []core.UserQuestion{{
		Question: "which?", Options: []string{"first", "second", "third"},
	}})))
	i.mu.Lock()
	qr := i.carrierAsk["a1"]
	i.mu.Unlock()
	ev = ctrlproto.AskResolvedEvent("a1")
	ev.Resolved.Option, ev.Resolved.Note = 2, "because"
	i.handleCarrierEvent(ev)
	settle(i.questionDialog.Active)
	_ = qr
	select {
	case as := <-c.answers:
		if len(as) != 1 || as[0].Answer != "second" || as[0].Note != "because" {
			t.Fatalf("the walk answered %+v, want second with the note", as)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the walk did not answer the question")
	}

	// No option named: dismissed, never answered.
	i.handleCarrierEvent(ctrlproto.PermissionEvent(ctrlproto.PermissionRequest{CallID: "c2", Tool: "bash"}))
	i.handleCarrierEvent(ctrlproto.PermissionResolvedEvent("c2"))
	if i.confirmDialog.Active() {
		t.Fatal("a resolution without an option must dismiss the dialog")
	}
}

// answerSpy is the replay carrier with the two answer verbs captured: the
// pump's goroutine consumes a dialog's response channel and forwards the
// decision here, so this is where a test sees what the walk answered.
type answerSpy struct {
	*replay.Carrier
	decisions chan core.ConfirmDecision
	answers   chan []core.UserAnswer
}

func (s *answerSpy) Approve(ctx context.Context, sess, callID string, d core.ConfirmDecision) error {
	s.decisions <- d
	return nil
}

func (s *answerSpy) Answer(ctx context.Context, sess, askID string, a []core.UserAnswer) error {
	s.answers <- a
	return nil
}
