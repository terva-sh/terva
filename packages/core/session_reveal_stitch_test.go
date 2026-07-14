package core

import (
	"slices"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// TestRevealChainReconstructsDisplayHistoryWithoutOverlap pins the property the
// whole compaction-scrollback design rests on: a client can walk the reveal chain
// backward from the live transcript, PREPENDING each span, and end up with the
// full conversation — no message twice, no message missing, and every divider
// standing exactly where the turns it summarized end.
//
// It holds because RevealCompaction subtracts the tail its checkpoint kept
// verbatim, so a span never contains a message the client already has. If that
// ever stops being true, every client silently doubles the turns around each
// compaction — which is the kind of bug that looks like a rendering glitch and is
// actually a protocol one. Hence a test, not a comment.
//
// Note what the stitched order says. Each divider lands immediately after the
// messages it folded away, not at the wall-clock moment the compaction ran. That
// is the more useful boundary and the one the UI promises: everything above this
// line is condensed, everything below it is verbatim.
func TestRevealChainReconstructsDisplayHistoryWithoutOverlap(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	// m0..m3, compact (keep m2,m3), m4, m5, compact again (keep m4,m5). The second
	// checkpoint folds in the FIRST summary, which is what makes the chain a chain.
	for _, m := range []provider.Message{revUser("m0"), revUser("m1"), revUser("m2"), revUser("m3")} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendCompaction([]provider.Message{revSummary("summary1"), revUser("m2"), revUser("m3")}, CompactResult{}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []provider.Message{revUser("m4"), revUser("m5")} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendCompaction([]provider.Message{revSummary("summary2"), revUser("m4"), revUser("m5")}, CompactResult{}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	text := func(m provider.Message) string {
		if len(m.Content) == 0 {
			return ""
		}
		tb, _ := m.Content[0].(provider.TextBlock)
		return tb.Text
	}
	texts := func(ms []provider.Message) []string {
		out := make([]string, len(ms))
		for i, m := range ms {
			out[i] = text(m)
		}
		return out
	}

	// What a client starts with: the live transcript the daemon would snapshot.
	reopened, loaded, err := OpenSession(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	display := texts(loaded)
	if want := []string{"summary2", "m4", "m5"}; !slices.Equal(display, want) {
		t.Fatalf("live transcript = %v, want %v", display, want)
	}

	// Now walk the chain backward exactly as a client scrolling up would: reveal the
	// latest checkpoint, prepend, follow PrevOrdinal, repeat until there is no
	// checkpoint behind this one.
	for ordinal := -1; ; {
		span, err := RevealCompaction(s.Path, ordinal)
		if err != nil {
			t.Fatalf("reveal(%d): %v", ordinal, err)
		}
		display = append(texts(span.Replaced), display...)
		if span.PrevOrdinal < 0 {
			break
		}
		ordinal = span.PrevOrdinal
	}

	// Every turn back, each divider directly below the turns it summarized:
	//   m0 m1        <- folded into summary1
	//   summary1     <- divider
	//   m2 m3        <- folded (with summary1) into summary2
	//   summary2     <- divider
	//   m4 m5        <- still in context
	want := []string{"m0", "m1", "summary1", "m2", "m3", "summary2", "m4", "m5"}
	if !slices.Equal(display, want) {
		t.Fatalf("stitched display history = %v,\n                        want %v", display, want)
	}

	// And say the overlap check outright, so a regression names itself rather than
	// showing up as a duplicated turn in someone's scrollback.
	seen := map[string]int{}
	for _, m := range display {
		seen[m]++
	}
	for m, n := range seen {
		if n > 1 {
			t.Errorf("message %q appears %d times — a reveal span overlapped the live transcript", m, n)
		}
	}
}

// TestRevealStopsAtAClear pins the floor. /clear writes an EMPTY checkpoint
// (AppendCompaction(nil)), which is what makes it recognizable — and it is a
// deliberate act, "done with that, start fresh", nearer a session boundary than a
// compaction. So the backward walk must not stroll across one on its own.
//
// It is a floor, not a wall: PrevOrdinal stays truthful and the crossing is served
// when a client asks for the clear's own ordinal. Deliberate to make, deliberate to
// undo. And it is not redaction — the turns before the clear are still in the file,
// which this test relies on to prove the crossing works at all.
func TestRevealStopsAtAClear(t *testing.T) {
	dir := testsupport.TempDir(t)
	s, err := NewSession(dir, dir, "fake", "model", "v")
	if err != nil {
		t.Fatal(err)
	}
	// Two turns, /clear, two more turns, then a compaction keeping the last one.
	for _, m := range []provider.Message{revUser("secret0"), revUser("secret1")} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendCompaction(nil, CompactResult{}); err != nil { // this is what /clear writes
		t.Fatal(err)
	}
	for _, m := range []provider.Message{revUser("after0"), revUser("after1")} {
		if err := s.AppendMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AppendCompaction([]provider.Message{revSummary("summary"), revUser("after1")}, CompactResult{}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	text := func(m provider.Message) string {
		tb, _ := m.Content[0].(provider.TextBlock)
		return tb.Text
	}
	texts := func(ms []provider.Message) []string {
		out := make([]string, len(ms))
		for i, m := range ms {
			out[i] = text(m)
		}
		return out
	}

	// Reveal the live divider: gives back the post-clear turns it folded away, and
	// flags that a clear — not another compaction — sits behind it.
	span, err := RevealCompaction(s.Path, -1)
	if err != nil {
		t.Fatalf("reveal(-1): %v", err)
	}
	if got := texts(span.Replaced); !slices.Equal(got, []string{"after0"}) {
		t.Errorf("replaced = %v, want [after0]", got)
	}
	if !span.PrevClear {
		t.Error("PrevClear should be set — the checkpoint behind this one is a /clear")
	}
	if span.Clear {
		t.Error("Clear is about the TARGET, and the target is an ordinary compaction")
	}
	// Nothing from before the clear leaked into an ordinary backward step.
	for _, m := range texts(span.Replaced) {
		if strings.HasPrefix(m, "secret") {
			t.Errorf("the automatic walk crossed a clear and surfaced %q", m)
		}
	}
	// PrevOrdinal is still honest: the floor is a policy for the caller to apply,
	// not a fact hidden from it.
	if span.PrevOrdinal != 0 {
		t.Errorf("PrevOrdinal = %d, want 0 (the clear) — truthful, for a deliberate crossing", span.PrevOrdinal)
	}

	// The deliberate crossing: ask for the clear's own ordinal and it is served.
	crossed, err := RevealCompaction(s.Path, span.PrevOrdinal)
	if err != nil {
		t.Fatalf("reveal(%d): %v", span.PrevOrdinal, err)
	}
	if !crossed.Clear {
		t.Error("Clear should mark that this target IS the clear the user chose to cross")
	}
	if got := texts(crossed.Replaced); !slices.Equal(got, []string{"secret0", "secret1"}) {
		t.Errorf("crossing a clear = %v, want [secret0 secret1] — the whole conversation before it", got)
	}
}
