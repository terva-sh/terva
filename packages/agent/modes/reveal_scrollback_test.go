package modes

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Reveal on the plain fakeCarrier: a daemon that does not serve it. fakeCarrier's
// embedded WorkspaceService is nil, so without this ANY test that scrolls up through
// a compacted transcript nil-derefs on the promoted method. It also exercises the
// real degradation path — the TUI stops asking and says so.
func (f *fakeCarrier) Reveal(context.Context, string, int) (ctrlproto.RevealResult, error) {
	return ctrlproto.RevealResult{}, errors.New("reveal not supported")
}

// revealCarrier is a fakeCarrier that DOES serve conversation.reveal. Embedding
// overrides the method above and leaves the shared fixture untouched.
type revealCarrier struct {
	*fakeCarrier

	mu    sync.Mutex
	spans map[int]ctrlproto.RevealResult
	asked []int
}

func (r *revealCarrier) Reveal(_ context.Context, _ string, ordinal int) (ctrlproto.RevealResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asked = append(r.asked, ordinal)
	return r.spans[ordinal], nil
}

func (r *revealCarrier) asks() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.asked...)
}

func wireUser(text string) core.WireMessage {
	return core.WireMessage{Role: "user", Content: []core.WireBlock{{Type: "text", Text: text}}}
}

func wireSummary(text string, tokens int) core.WireMessage {
	return core.MessageToWireFull(provider.Message{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "## Context Summary (compacted)\n\n" + text}},
		Meta:    map[string]string{core.MetaCompaction: "true", core.MetaTokensBefore: strconv.Itoa(tokens)},
	})
}

// TestScrollingPastTheDividerRevealsTheTurnsItFoldedAway drives the whole feature
// end-to-end on the VT harness: a compacted session paints a divider, scrolling to
// the top pages the folded-away turns back in from the daemon, and they appear ABOVE
// the divider.
//
// This is the gesture the TUI already teaches — View.TailLimit has always revealed
// older messages when you scroll past the top — continued past the point where the
// live transcript runs out. Before it, a compaction simply deleted your conversation
// from the screen.
func TestScrollingPastTheDividerRevealsTheTurnsItFoldedAway(t *testing.T) {
	fc := newFakeCarrier()
	fc.stream = make(chan ctrlproto.Event, 8)
	rc := &revealCarrier{
		fakeCarrier: fc,
		spans: map[int]ctrlproto.RevealResult{
			// The live divider is always the latest checkpoint (-1). Nothing behind it.
			-1: {Ordinal: 0, PrevOrdinal: -1, Total: 1, Messages: []core.WireMessage{
				wireUser("the buried question"),
			}},
		},
	}
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = rc
		cfg.CarrierSession = "s1"
	})

	fc.stream <- ctrlproto.SnapshotEvent(ctrlproto.Snapshot{
		Session:  ctrlproto.SessionInfo{ID: "s1"},
		Messages: []core.WireMessage{wireSummary("we renamed the widget", 112000), wireUser("carry on then")},
	})
	h.waitText("carry on then")
	h.waitText("compacted")
	// The folded-away turn is not on screen until asked for.
	if strings.Contains(h.term.Screen().Text(), "the buried question") {
		t.Fatal("history appeared without being revealed")
	}

	// Scroll to the top — the same gesture that widens the tail cap.
	for range 20 {
		h.term.Type("\x1b[5~") // PageUp
	}

	h.waitText("the buried question")
	if got := rc.asks(); len(got) == 0 || got[0] != -1 {
		t.Fatalf("asked for ordinals %v, want the first to be -1 (the latest checkpoint)", got)
	}
}

// TestRevealStopsAtAClearUntilAskedOutright is the TUI half of the floor.
//
// Scrolling walks back through compactions on its own. A /clear is different: it was
// a deliberate act — "done with that, start fresh" — nearer a session boundary than
// a compaction, which merely condenses a conversation you are still having. So the
// walk stops, and /reveal is what crosses it. Deliberate to make, deliberate to undo.
func TestRevealStopsAtAClearUntilAskedOutright(t *testing.T) {
	fc := newFakeCarrier()
	fc.stream = make(chan ctrlproto.Event, 8)
	rc := &revealCarrier{
		fakeCarrier: fc,
		spans: map[int]ctrlproto.RevealResult{
			// Behind the live divider: the post-clear turns, and a clear behind THEM.
			-1: {Ordinal: 1, PrevOrdinal: 0, Total: 2, PrevClear: true,
				Messages: []core.WireMessage{wireUser("after the clear")}},
			// The deliberate crossing: everything from before it.
			0: {Ordinal: 0, PrevOrdinal: -1, Total: 2, Clear: true,
				Messages: []core.WireMessage{wireUser("from before the clear")}},
		},
	}
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = rc
		cfg.CarrierSession = "s1"
	})

	fc.stream <- ctrlproto.SnapshotEvent(ctrlproto.Snapshot{
		Session:  ctrlproto.SessionInfo{ID: "s1"},
		Messages: []core.WireMessage{wireSummary("a summary", 9000), wireUser("carry on then")},
	})
	h.waitText("carry on then")

	for range 20 {
		h.term.Type("\x1b[5~")
	}
	// The ordinary walk gets as far as the clear and no further.
	h.waitText("after the clear")
	if strings.Contains(h.term.Screen().Text(), "from before the clear") {
		t.Fatal("scrolling crossed a /clear on its own — it must take saying so outright")
	}

	// A clear leaves no message behind, so the boundary has to be MINTED or the
	// history above just stops with nothing to say why. It must be on screen, and it
	// must say how to cross it.
	h.waitText("conversation cleared")
	h.waitText("/reveal to show what came before")

	// Say so outright.
	h.term.Type("/reveal\r")
	h.waitText("from before the clear")

	// The divider stays: the point is to show WHERE the conversation was cut, not to
	// pretend it wasn't. It just stops offering a crossing already made.
	screen := h.term.Screen().Text()
	if !strings.Contains(screen, "conversation cleared") {
		t.Errorf("the clear boundary vanished once crossed; screen:\n%s", screen)
	}
	if strings.Contains(screen, "/reveal to show what came before") {
		t.Errorf("the crossed divider is still offering a crossing already made; screen:\n%s", screen)
	}

	// And it asked for the clear's own ordinal, which is what the daemon serves the
	// crossing for.
	found := false
	for _, o := range rc.asks() {
		if o == 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("asked for ordinals %v, want a crossing at ordinal 0 (the clear)", rc.asks())
	}
}
