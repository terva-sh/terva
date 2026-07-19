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

// TestReviseMessageTextPreservesStructure pins the attribution guarantee: editing
// a message's prose keeps its tool-call / tool-result / image blocks (in order) so
// a cast turn's actor_spawn machinery — and the 🎭 attribution that rides its
// tool_result — survives an edit, while stale reasoning blocks are dropped.
func TestReviseMessageTextPreservesStructure(t *testing.T) {
	toolCall := provider.ToolCallBlock{ID: "call_1", Name: "actor_spawn"}
	img := provider.ImageBlock{MimeType: "image/png", Data: []byte{1, 2, 3}}
	reason := provider.ReasoningBlock{ID: "r1", Summary: "thinking"}

	cases := []struct {
		name string
		in   []provider.Content
		want []provider.Content
	}{
		{
			name: "pure text",
			in:   []provider.Content{provider.TextBlock{Text: "old"}},
			want: []provider.Content{provider.TextBlock{Text: "new"}},
		},
		{
			name: "text then tool call — tool preserved, order kept",
			in:   []provider.Content{provider.TextBlock{Text: "old"}, toolCall},
			want: []provider.Content{provider.TextBlock{Text: "new"}, toolCall},
		},
		{
			name: "tool call, text, reasoning — reasoning dropped",
			in:   []provider.Content{toolCall, provider.TextBlock{Text: "old"}, reason},
			want: []provider.Content{toolCall, provider.TextBlock{Text: "new"}},
		},
		{
			name: "two text blocks consolidate into the first",
			in:   []provider.Content{provider.TextBlock{Text: "a"}, provider.TextBlock{Text: "b"}},
			want: []provider.Content{provider.TextBlock{Text: "new"}},
		},
		{
			name: "no prose block — new text leads, structure kept",
			in:   []provider.Content{toolCall, img},
			want: []provider.Content{provider.TextBlock{Text: "new"}, toolCall, img},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := reviseMessageText(provider.Message{Role: provider.RoleAssistant, Content: tc.in}, "new")
			if got.Role != provider.RoleAssistant {
				t.Errorf("role = %v, want assistant", got.Role)
			}
			if !reflect.DeepEqual(got.Content, tc.want) {
				t.Errorf("content = %#v, want %#v", got.Content, tc.want)
			}
		})
	}
}

// TestEditTailProducesSwipeableVariant drives the MVP through the real workspace:
// editing the tail response keeps the original as a swipeable take (rather than
// destroying it), the snapshot advertises the 2-take tail, a swipe restores the
// original, and a reload from disk reconstructs the same variant set — while an
// edit to an older message stays an in-place revise (no variant).
func TestEditTailProducesSwipeableVariant(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := w.live(info.ID)
	if s == nil {
		t.Fatal("created session is not live")
	}

	base := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "u0"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a0"}}},
	}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)

	// Edit the tail assistant message → it becomes a swipeable variant.
	if err := w.EditMessage(context.Background(), info.ID, s.agent.TranscriptEpoch(), 1, "a0-edited"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if got := reviseTexts(s.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", "a0-edited"}) {
		t.Fatalf("after edit = %v, want [u0 a0-edited]", got)
	}
	// The snapshot advertises a 2-take tail with the edit active.
	if tail := s.snapshot().Tail; tail == nil || tail.Variants != 2 || tail.Active != 1 || tail.SpanStart != 1 {
		t.Fatalf("tail = %+v, want {SpanStart:1 Variants:2 Active:1}", tail)
	}
	// Swiping back to take 0 restores the original line.
	if err := w.SwipeTurn(context.Background(), info.ID, s.agent.TranscriptEpoch(), 0); err != nil {
		t.Fatalf("swipe: %v", err)
	}
	if got := reviseTexts(s.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", "a0"}) {
		t.Fatalf("after swipe-back = %v, want [u0 a0]", got)
	}
	// Persisted: a reload reconstructs the same 2-take tail (active is the swiped-to 0).
	start, takes, active, err := core.SessionTail(info.Path)
	if err != nil {
		t.Fatalf("SessionTail: %v", err)
	}
	if start != 1 || len(takes) != 2 || active != 0 {
		t.Fatalf("reloaded tail: start=%d takes=%d active=%d, want start=1 takes=2 active=0", start, len(takes), active)
	}

	// Add a new turn so message 1 is no longer the tail, then clear the tail state
	// as a real turn would (this test appends messages directly, bypassing the turn
	// machinery that commits the tail).
	for _, m := range []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "u1"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a1"}}},
	} {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(append(s.agent.Messages(),
		provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "u1"}}},
		provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a1"}}},
	))
	s.clearTail()
	// Editing that now-OLDER message (index 1) is a message-scoped variant: the
	// downstream stays, the tail is untouched, and the position gets a marker.
	if err := w.EditMessage(context.Background(), info.ID, s.agent.TranscriptEpoch(), 1, "a0-again"); err != nil {
		t.Fatalf("edit older: %v", err)
	}
	snap2 := s.snapshot()
	if snap2.Tail != nil {
		t.Fatalf("an older edit must not create a tail variant, got %+v", snap2.Tail)
	}
	if len(snap2.VariantMarks) != 1 || snap2.VariantMarks[0].Index != 1 || snap2.VariantMarks[0].Span {
		t.Fatalf("older edit marks = %+v, want one message-scoped mark at index 1", snap2.VariantMarks)
	}
	if got := reviseTexts(s.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", "a0-again", "u1", "a1"}) {
		t.Fatalf("after older edit = %v, want [u0 a0-again u1 a1]", got)
	}
}

// TestEditOlderMessageCreatesVariantMarker drives Option C's read side: editing a
// message BEFORE the current response span records a message-scoped variant (the
// original kept, the downstream shared), the snapshot marks that position (not the
// tail), and the position is persisted so a resumed session re-seeds the marker.
func TestEditOlderMessageCreatesVariantMarker(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	cwd := testsupport.TempDir(t)
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := w.live(info.ID)
	if s == nil {
		t.Fatal("created session is not live")
	}

	base := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "u0"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a0"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "u1"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a1"}}},
	}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)

	// Edit the OLDER assistant (index 1); lastResponseStart is 3, so this is a
	// message-scoped variant, not a tail edit.
	if err := w.EditMessage(context.Background(), info.ID, s.agent.TranscriptEpoch(), 1, "a0-edited"); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if got := reviseTexts(s.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", "a0-edited", "u1", "a1"}) {
		t.Fatalf("effective = %v, want [u0 a0-edited u1 a1] (downstream preserved)", got)
	}
	snap := s.snapshot()
	if snap.Tail != nil {
		t.Fatalf("an older edit must not create a tail variant, got %+v", snap.Tail)
	}
	if len(snap.VariantMarks) != 1 || snap.VariantMarks[0] != (ctrlproto.VariantMark{Index: 1, Variants: 2, Active: 1}) {
		t.Fatalf("marks = %+v, want one {Index:1 Variants:2 Active:1}", snap.VariantMarks)
	}

	// Persisted (keep_prior) and re-seeded on resume: a fresh workspace over the same
	// home resolves the session and draws the marker from disk.
	w2, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	s2, err := w2.resolve(info.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if marks := s2.snapshot().VariantMarks; len(marks) != 1 || marks[0] != (ctrlproto.VariantMark{Index: 1, Variants: 2, Active: 1}) {
		t.Fatalf("resumed marks = %+v, want the seeded {Index:1 Variants:2 Active:1}", marks)
	}
}

// TestSwipeMessageVariant drives Option C's write side: swiping a message-scoped
// variant switches the active take in place (downstream untouched), persists, and
// — the lazy-hydration half — a resumed session whose take list was not in memory
// swipes correctly by reading it from disk on demand.
func TestSwipeMessageVariant(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	cwd := testsupport.TempDir(t)
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := w.live(info.ID)
	base := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "u0"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a0"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "u1"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a1"}}},
	}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)
	// Two edits of the older assistant (index 1): a0 → a0-p1 → a0-p2.
	for _, txt := range []string{"a0-p1", "a0-p2"} {
		if err := w.EditMessage(context.Background(), info.ID, s.agent.TranscriptEpoch(), 1, txt); err != nil {
			t.Fatalf("edit %s: %v", txt, err)
		}
	}
	// Swipe back to the original (take 0), then to take 1 — the active message swaps
	// in place, downstream (u1, a1) unchanged.
	if err := w.SwipeMessage(context.Background(), info.ID, s.agent.TranscriptEpoch(), 1, 0); err != nil {
		t.Fatalf("swipe to 0: %v", err)
	}
	if got := reviseTexts(s.agent.Messages()); !reflect.DeepEqual(got, []string{"u0", "a0", "u1", "a1"}) {
		t.Fatalf("after swipe-0 = %v, want [u0 a0 u1 a1]", got)
	}
	if err := w.SwipeMessage(context.Background(), info.ID, s.agent.TranscriptEpoch(), 1, 1); err != nil {
		t.Fatalf("swipe to 1: %v", err)
	}
	if got := reviseText(s.agent.Messages()[1]); got != "a0-p1" {
		t.Fatalf("after swipe-1, message 1 = %q, want a0-p1", got)
	}

	// Resume in a fresh workspace: the take list is NOT in memory (seedMsgVars kept
	// only the count), so this swipe must hydrate it from disk.
	w2, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: cwd}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	s2, err := w2.resolve(info.ID)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if got := reviseText(s2.agent.Messages()[1]); got != "a0-p1" {
		t.Fatalf("resumed message 1 = %q, want a0-p1 (last selected)", got)
	}
	if err := w2.SwipeMessage(context.Background(), info.ID, s2.agent.TranscriptEpoch(), 1, 2); err != nil {
		t.Fatalf("resumed swipe to 2 (lazy hydrate): %v", err)
	}
	if got := reviseText(s2.agent.Messages()[1]); got != "a0-p2" {
		t.Fatalf("after hydrated swipe, message 1 = %q, want a0-p2", got)
	}
}

// seedThreeTakeVariant makes a session with a message-scoped variant at index 1
// carrying three takes ([a0, a0-p1, a0-p2], active 2), returning the workspace,
// session id, and live session.
func seedThreeTakeVariant(t *testing.T) (*Workspace, string, *wsSession) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	w, err := NewWorkspace(build.Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, "test")
	if err != nil {
		t.Fatal(err)
	}
	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{Experience: "chat"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	s := w.live(info.ID)
	base := []provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "u0"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a0"}}},
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "u1"}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "a1"}}},
	}
	for _, m := range base {
		if err := s.sess.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	s.agent.SetMessages(base)
	for _, txt := range []string{"a0-p1", "a0-p2"} {
		if err := w.EditMessage(context.Background(), info.ID, s.agent.TranscriptEpoch(), 1, txt); err != nil {
			t.Fatalf("edit %s: %v", txt, err)
		}
	}
	return w, info.ID, s
}

// TestPruneVariants proves prune-to-latest closes the position: the marker goes,
// the active take stays live, and the file no longer reconstructs the variant.
func TestPruneVariants(t *testing.T) {
	w, id, s := seedThreeTakeVariant(t)
	defer w.Close()
	if len(s.snapshot().VariantMarks) != 1 {
		t.Fatalf("precondition: expected one variant mark, got %+v", s.snapshot().VariantMarks)
	}
	if err := w.PruneVariants(context.Background(), id, s.agent.TranscriptEpoch(), 1); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if marks := s.snapshot().VariantMarks; len(marks) != 0 {
		t.Fatalf("prune should remove the marker, got %+v", marks)
	}
	if got := reviseText(s.agent.Messages()[1]); got != "a0-p2" {
		t.Fatalf("prune should keep the active take, message 1 = %q, want a0-p2", got)
	}
	if vs, _ := core.SessionVariants(w.sessionPath(id)); len(vs) != 0 {
		t.Fatalf("prune should close the position on disk, got %+v", vs)
	}
}

// TestDropVariant proves per-take removal drops one take (keeping the rest) and
// closes the position once one take remains.
func TestDropVariant(t *testing.T) {
	w, id, s := seedThreeTakeVariant(t)
	defer w.Close()
	// Drop take 0 (the original a0): the active (2) shifts to 1, a0-p2 stays live.
	if err := w.DropVariant(context.Background(), id, s.agent.TranscriptEpoch(), 1, 0); err != nil {
		t.Fatalf("drop 0: %v", err)
	}
	if m := s.snapshot().VariantMarks; len(m) != 1 || m[0] != (ctrlproto.VariantMark{Index: 1, Variants: 2, Active: 1}) {
		t.Fatalf("after drop, marks = %+v, want one {Index:1 Variants:2 Active:1}", s.snapshot().VariantMarks)
	}
	if got := reviseText(s.agent.Messages()[1]); got != "a0-p2" {
		t.Fatalf("drop of a non-active take should keep it live, message 1 = %q, want a0-p2", got)
	}
	// Drop the now-active take (index 1) → one take left → position closes.
	if err := w.DropVariant(context.Background(), id, s.agent.TranscriptEpoch(), 1, 1); err != nil {
		t.Fatalf("drop 1: %v", err)
	}
	if m := s.snapshot().VariantMarks; len(m) != 0 {
		t.Fatalf("dropping to one take should close the position, got %+v", m)
	}
	if got := reviseText(s.agent.Messages()[1]); got != "a0-p1" {
		t.Fatalf("after closing, message 1 = %q, want a0-p1", got)
	}
}
