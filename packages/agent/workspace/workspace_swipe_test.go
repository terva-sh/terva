package workspace

import (
	"context"
	"reflect"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

func swipeMsg(role provider.Role, text string) provider.Message {
	return provider.Message{Role: role, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

// writeVariantTailSession creates an immersive session, writes a tail span with
// two takes to its file (u0, a0, retract@1, a1 → takes [[a0],[a1]], active 1),
// closes the workspace, and returns a FRESH workspace on the same home+cwd. A
// resume through it exercises the real materialize path (buildSession → seedTail)
// — the honest daemon-restart simulation.
func writeVariantTailSession(t *testing.T) (w *Workspace, id, path string) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	cwd := testsupport.TempDir(t)
	w1, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	info, err := w1.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s1 := w1.live(info.ID)
	if s1 == nil {
		t.Fatal("created session is not live")
	}
	must := func(e error) {
		if e != nil {
			t.Fatal(e)
		}
	}
	must(s1.sess.AppendMessage(swipeMsg(provider.RoleUser, "u0")))
	must(s1.sess.AppendMessage(swipeMsg(provider.RoleAssistant, "a0")))
	must(s1.sess.AppendAmend(core.AmendRetract, 1, nil, "retry"))
	must(s1.sess.AppendMessage(swipeMsg(provider.RoleAssistant, "a1")))
	w1.Close() // flush + close; the file persists for a cold resume

	w, err = NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	return w, info.ID, info.Path
}

// TestSwipeTailVariants drives the Phase-1e turn.swipe verb end to end through the
// real workspace: a resumed immersive session re-derives its tail's takes from the
// file (seedTail), the snapshot advertises them, a swipe rebuilds the transcript
// as prefix+take and persists a select amend (a reload matches), a stale epoch is
// refused, a swipe to the active variant is a no-op, and an out-of-range variant
// is a clean bad-request.
func TestSwipeTailVariants(t *testing.T) {
	w, id, path := writeVariantTailSession(t)
	defer w.Close()
	ctx := context.Background()

	s, err := w.resolve(id) // cold materialize → seedTail
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	// Resumed with the newer take (a1) live, and both takes switchable.
	if got := reviseTexts(s.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", "a1"}) {
		t.Fatalf("resumed transcript = %v, want [u0 a1]", got)
	}
	snap := s.snapshot()
	if snap.Tail == nil {
		t.Fatal("resumed session should carry tail swipe metadata")
	}
	if snap.Tail.SpanStart != 1 || snap.Tail.Variants != 2 || snap.Tail.Active != 1 {
		t.Fatalf("tail = %+v, want {span_start:1 variants:2 active:1}", *snap.Tail)
	}

	// Swipe back to take 0 (the original a0).
	epoch := s.agent.TranscriptEpoch()
	if err := w.SwipeTurn(ctx, id, epoch, 0); err != nil {
		t.Fatalf("swipe: %v", err)
	}
	if got := reviseTexts(s.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", "a0"}) {
		t.Errorf("after swipe = %v, want [u0 a0]", got)
	}
	if snap := s.snapshot(); snap.Tail == nil || snap.Tail.Active != 0 {
		t.Errorf("tail after swipe = %+v, want active 0", snap.Tail)
	}
	// Persisted: a reload from disk reconstructs the swiped transcript.
	if _, reloaded, err := core.OpenSession(path); err != nil {
		t.Fatalf("reopen: %v", err)
	} else if got := reviseTexts(reloaded); !reflect.DeepEqual(got, []string{"u0", "a0"}) {
		t.Errorf("reloaded after swipe = %v, want [u0 a0]", got)
	}

	// The swipe advanced the epoch, so the original one is now stale → refused.
	if err := w.SwipeTurn(ctx, id, epoch, 1); err == nil {
		t.Error("swipe with a stale epoch should be refused")
	}
	// Swipe forward to a1 again at the current epoch.
	if err := w.SwipeTurn(ctx, id, s.agent.TranscriptEpoch(), 1); err != nil {
		t.Fatalf("swipe forward: %v", err)
	}
	if got := reviseTexts(s.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", "a1"}) {
		t.Errorf("after swipe forward = %v, want [u0 a1]", got)
	}
	// A swipe to the already-active variant is an accepted no-op — no new epoch.
	before := s.agent.TranscriptEpoch()
	if err := w.SwipeTurn(ctx, id, before, 1); err != nil {
		t.Errorf("no-op swipe should succeed: %v", err)
	}
	if s.agent.TranscriptEpoch() != before {
		t.Error("a no-op swipe should not advance the epoch")
	}
	// Out-of-range variant is a clean bad-request, not a panic.
	if err := w.SwipeTurn(ctx, id, s.agent.TranscriptEpoch(), 9); err == nil {
		t.Error("swipe to an out-of-range variant should error")
	}
}

// TestEditVariantTailAddsTake proves an edit to a tail that already carries swipe
// alternatives is non-destructive: rather than clearing them (the old behavior) or
// overwriting the active take, the edit lands as a NEW active take, so every prior
// take stays reachable — the "edit is always variant-producing" rule of the
// inline-editing proposal (§11).
func TestEditVariantTailAddsTake(t *testing.T) {
	w, id, _ := writeVariantTailSession(t)
	defer w.Close()

	s, err := w.resolve(id)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	tail := s.snapshot().Tail
	if tail == nil {
		t.Fatal("resumed session should carry tail metadata")
	}
	before := tail.Variants
	if err := w.EditMessage(context.Background(), id, s.agent.TranscriptEpoch(), 1, "a1-edited"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	snap := s.snapshot()
	if snap.Tail == nil || snap.Tail.Variants != before+1 || snap.Tail.Active != before {
		t.Fatalf("edit should append a take and make it active: tail=%+v, want Variants=%d Active=%d", snap.Tail, before+1, before)
	}
	if got := reviseText(s.agent.Messages()[1]); got != "a1-edited" {
		t.Errorf("active tail message = %q, want a1-edited", got)
	}
}

// TestRetryRegeneratesKeepingTake drives the Phase-1e turn.retry verb through a
// real regeneration: retract sets the current response aside, a fresh turn
// generates a new one, and the tail's variants are re-derived from disk so both
// are switchable — the snapshot advertises the pair, and a swipe-back restores
// the original (a reload confirms the persistence).
func TestRetryRegeneratesKeepingTake(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)
	build.WireHeadlessSessionPersist(s.agent, s.sess)

	// Seed a completed exchange into both the file and the agent.
	base := []provider.Message{swipeMsg(provider.RoleUser, "u0"), swipeMsg(provider.RoleAssistant, "a0")}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)

	sub := s.hub.add(func() ctrlproto.Event { return ctrlproto.SnapshotEvent(s.snapshot()) }, true)

	if err := s.retry(ctrlproto.TurnRetryParams{Epoch: s.agent.TranscriptEpoch()}); err != nil {
		t.Fatalf("retry: %v", err)
	}
	close(cl.release) // let the regeneration land ("ok")

	drainUntil(t, sub, "done")
	snapEv, _ := drainUntil(t, sub, ctrlproto.EventSnapshot)
	snap := snapEv.Snapshot

	// The regenerated response is live, and both takes are switchable.
	if got := reviseTexts(s.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", "ok"}) {
		t.Errorf("after retry = %v, want [u0 ok]", got)
	}
	if snap == nil || snap.Tail == nil {
		t.Fatalf("turn-end snapshot should carry tail metadata, got %+v", snap)
	}
	if snap.Tail.SpanStart != 1 || snap.Tail.Variants != 2 || snap.Tail.Active != 1 {
		t.Fatalf("tail = %+v, want {span_start:1 variants:2 active:1}", *snap.Tail)
	}

	// Swipe back to the original response, and confirm it persisted.
	if err := s.swipe(s.agent.TranscriptEpoch(), 0); err != nil {
		t.Fatalf("swipe: %v", err)
	}
	if got := reviseTexts(s.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", "a0"}) {
		t.Errorf("after swipe-back = %v, want [u0 a0]", got)
	}
	if _, reloaded, err := core.OpenSession(s.sess.Path); err != nil {
		t.Fatalf("reopen: %v", err)
	} else if got := reviseTexts(reloaded); !reflect.DeepEqual(got, []string{"u0", "a0"}) {
		t.Errorf("reloaded = %v, want [u0 a0]", got)
	}
}

// TestRetryGuards proves retry refuses the cases that have nothing to regenerate
// or act on a stale view, before starting any turn.
func TestRetryGuards(t *testing.T) {
	cl := &gatedTurnClient{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := newTurnTestSession(t, cl)

	// No response after the last user message → nothing to retry.
	s.agent.SetMessages([]provider.Message{swipeMsg(provider.RoleUser, "u0")})
	if err := s.retry(ctrlproto.TurnRetryParams{Epoch: s.agent.TranscriptEpoch()}); err == nil {
		t.Error("retry with no response to regenerate should error")
	}
	// A stale epoch is refused (the transcript shifted under the client).
	s.agent.SetMessages([]provider.Message{swipeMsg(provider.RoleUser, "u0"), swipeMsg(provider.RoleAssistant, "a0")})
	if err := s.retry(ctrlproto.TurnRetryParams{Epoch: s.agent.TranscriptEpoch() + 999}); err == nil {
		t.Error("retry with a stale epoch should be refused")
	}
	if s.busy() {
		t.Error("a refused retry must not leave a turn running")
	}
}
