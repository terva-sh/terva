package workspace

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// A directed post lands in the transcript as an attributed assistant message,
// and — the load-bearing invariant for any transcript mutation — what a reload
// reconstructs from disk is byte-for-byte the live transcript, attribution and
// all. Also exercises the greeting-flush-on-first-append promotion: the posts
// happen before any real turn.
func TestPostDirectedReplayMatchesLive(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()

	imported, err := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Ivy","first_mes":"Hello there."}`)})
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	live := w.live(info.ID)
	if live == nil {
		t.Fatal("session is not live")
	}

	// A narrator beat, then a named actor's line — both before the first real turn,
	// so the first post also flushes the deferred greeting (promotes the draft).
	if err := w.PostLine(ctx, info.ID, ctrlproto.PostLineParams{Text: "The door creaks open."}); err != nil {
		t.Fatal(err)
	}
	if live.sess.HasPendingGreeting() {
		t.Error("a directed post should have flushed the deferred greeting")
	}
	if err := w.PostLine(ctx, info.ID, ctrlproto.PostLineParams{Actor: "Kael", Text: `Kael steps in. "You're late."`}); err != nil {
		t.Fatal(err)
	}

	liveMsgs := live.agent.Messages()
	if len(liveMsgs) != 3 { // greeting, narrator beat, actor line
		t.Fatalf("live transcript has %d messages, want 3", len(liveMsgs))
	}

	_, replayed, err := core.OpenSession(live.sess.Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != len(liveMsgs) {
		t.Fatalf("replayed %d messages, live has %d", len(replayed), len(liveMsgs))
	}
	for i := range liveMsgs {
		lw := core.MessageToWireFull(liveMsgs[i])
		rw := core.MessageToWireFull(replayed[i])
		if !reflect.DeepEqual(lw, rw) {
			t.Errorf("message %d: replayed != live\n live=%+v\n disk=%+v", i, lw, rw)
		}
	}

	// Attribution survived the round-trip: the actor line names Kael, the beat is a
	// narrator post (directed, no actor), and neither reads as a plain model turn.
	actor := core.MessageToWireFull(replayed[2])
	if !actor.Directed || actor.Actor != "Kael" {
		t.Errorf("actor line lost attribution on reload: directed=%v actor=%q", actor.Directed, actor.Actor)
	}
	beat := core.MessageToWireFull(replayed[1])
	if !beat.Directed || beat.Actor != "" {
		t.Errorf("narrator beat lost attribution on reload: directed=%v actor=%q", beat.Directed, beat.Actor)
	}
}

// An empty post is rejected, not committed as a blank turn.
func TestPostDirectedRejectsEmpty(t *testing.T) {
	w := draftWorkspace(t)
	ctx := context.Background()
	imported, _ := w.CardsImport(ctx, ctrlproto.CardImportParams{Bytes: []byte(`{"name":"Ivy","first_mes":"Hi."}`)})
	info, err := w.CreateSession(ctx, ctrlproto.CreateOpts{Experience: "chat", Card: imported.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.PostLine(ctx, info.ID, ctrlproto.PostLineParams{Text: "   "}); err == nil {
		t.Error("an empty directed post should be rejected")
	}
}

// A direction (Phase 6b) starts one turn steered by a [Direction] message — the
// same convention cast.speak uses — reusing the whole turn path. An empty
// direction starts no turn.
func TestDirectTurn(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)
	sub := s.hub.add(nil, true)

	if err := s.directTurn("   "); err == nil {
		t.Error("an empty direction should error")
	}
	if s.busy() {
		t.Fatal("a refused direction must not start a turn")
	}

	if err := s.directTurn("night falls; a storm rolls in"); err != nil {
		t.Fatalf("directTurn: %v", err)
	}
	<-cl.started
	if !s.busy() {
		t.Error("directTurn should have started a turn")
	}
	got := reviseTexts(s.agent.Messages())
	if len(got) == 0 || !strings.Contains(got[len(got)-1], "[Direction]") || !strings.Contains(got[len(got)-1], "night falls") {
		t.Errorf("direction not wrapped as a [Direction] steer: %v", got)
	}
	close(cl.release)
	drainUntil(t, sub, "done")
	waitIdle(t, s)
}

// renderSuggestSystem writes in the target's voice: the player by default, a
// named character for an actor target, the narrator for a narrator target.
func TestRenderSuggestSystemTargets(t *testing.T) {
	player := renderSuggestSystem(suggestTarget{}, userPersona{Name: "Aria", Description: "a wary traveller"}, nil, "", nil)
	if !strings.Contains(player, "human player compose THEIR next message") {
		t.Errorf("player draft lost its player-voice framing:\n%s", player)
	}

	actor := renderSuggestSystem(suggestTarget{kind: "actor", name: "Kael", voice: "a gruff dockmaster"}, userPersona{Name: "Aria"}, nil, "", nil)
	if !strings.Contains(actor, "next line for Kael") {
		t.Errorf("actor draft is not written for Kael:\n%s", actor)
	}
	if !strings.Contains(actor, "WHO YOU ARE VOICING") || !strings.Contains(actor, "gruff dockmaster") {
		t.Errorf("actor draft dropped the voice description:\n%s", actor)
	}
	if !strings.Contains(actor, "do not write their words") {
		t.Errorf("actor draft should frame the player as context, not the voice:\n%s", actor)
	}

	narrator := renderSuggestSystem(suggestTarget{kind: "narrator"}, userPersona{Name: "Aria"}, nil, "", nil)
	if !strings.Contains(narrator, "NARRATIVE beat") {
		t.Errorf("narrator draft lost its narrator framing:\n%s", narrator)
	}
}
