package core

import (
	"reflect"
	"testing"

	"terva.sh/terva/packages/provider"
)

// The tail span is the last response's swipeable alternatives, keyed by the
// index it starts at. Deleting a message BELOW it shifts every later message
// down one, so tailStart must move with them.
//
// The AmendDelete branch rebased the message-scoped variants and left the tail
// span alone. That is not an off-by-one in a display: sealActiveTake seals
// effective[tailStart:] into the active take, so a stale (too high) start seals
// a SHORT or EMPTY span, and a later AmendSelect splices at the stale index and
// appends the swiped-away take instead of replacing the live one. The reloaded
// transcript comes back with two consecutive assistant turns, on disk, forever —
// the fold re-derives the bad span from the file on every load.
//
// seedTail's length check cannot catch it. It compares len(takes[active]) with
// len(msgs)-start, and takes[active] was sealed from effective[tailStart:] at
// the same stale start, so both sides shift together and are algebraically
// forced to agree.
//
// The setup is two ordinary verbs: regenerate a response, then delete an earlier
// message.
func TestTailSpanRebasesOnDeleteBelowIt(t *testing.T) {
	s := mvNewSession(t,
		mvMsg(provider.RoleUser, "u0"),
		mvMsg(provider.RoleAssistant, "a1"),
		mvMsg(provider.RoleUser, "u2"),
		mvMsg(provider.RoleAssistant, "a3"),
	)
	// Regenerate the last response: retract at 3, then a fresh take.
	if err := s.AppendAmend(AmendRetract, 3, nil, "retry"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(mvMsg(provider.RoleAssistant, "a3-take2")); err != nil {
		t.Fatal(err)
	}
	// Delete a message BELOW the span.
	if err := s.AppendAmend(AmendDelete, 0, nil, "delete"); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	msgs, err := ReadSessionMessages(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := mvTexts(msgs); !reflect.DeepEqual(got, []string{"a1", "u2", "a3-take2"}) {
		t.Fatalf("transcript = %v, want [a1 u2 a3-take2]", got)
	}

	start, takes, active, err := SessionTail(path)
	if err != nil {
		t.Fatal(err)
	}
	if start != 2 {
		t.Fatalf("tail start = %d, want 2 — the span did not follow the shifted transcript. "+
			"sealActiveTake will seal effective[%d:] (short or empty) and a later swipe will splice "+
			"at the wrong index, appending a take instead of replacing one.", start, start)
	}
	if len(takes) != 2 {
		t.Fatalf("takes = %d, want 2", len(takes))
	}
	if active < 0 || active >= len(takes) {
		t.Fatalf("active = %d, out of range for %d takes", active, len(takes))
	}
	// The live take must be the span actually present in the transcript, not an
	// empty slice sealed from a stale offset.
	if got := mvTexts(takes[active]); !reflect.DeepEqual(got, []string{"a3-take2"}) {
		t.Fatalf("active take = %v, want [a3-take2]", got)
	}

	// The real damage: swipe back to take 0 and reload. A stale start appends.
	s2, _, err := OpenSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.AppendSelect(2, 0, "swipe"); err != nil {
		t.Fatal(err)
	}
	_ = s2.Close()

	after, err := ReadSessionMessages(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := mvTexts(after); !reflect.DeepEqual(got, []string{"a1", "u2", "a3"}) {
		t.Fatalf("after swiping back the transcript = %v, want [a1 u2 a3].\n"+
			"A length of 4 with two assistant turns means the select spliced at a stale index "+
			"and appended the swiped-away take instead of replacing the live one.", got)
	}
}

// A truncation at or below the span start removes the span itself. Leaving
// tailStart set would seal an empty take and advertise a swipe over messages
// that no longer exist.
func TestTailSpanDropsOnTruncateAtOrBelowIt(t *testing.T) {
	s := mvNewSession(t,
		mvMsg(provider.RoleUser, "u0"),
		mvMsg(provider.RoleAssistant, "a1"),
		mvMsg(provider.RoleUser, "u2"),
		mvMsg(provider.RoleAssistant, "a3"),
	)
	if err := s.AppendAmend(AmendRetract, 3, nil, "retry"); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(mvMsg(provider.RoleAssistant, "a3-take2")); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendAmend(AmendTruncate, 2, nil, "truncate"); err != nil {
		t.Fatal(err)
	}
	path := s.Path
	_ = s.Close()

	msgs, err := ReadSessionMessages(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := mvTexts(msgs); !reflect.DeepEqual(got, []string{"u0", "a1"}) {
		t.Fatalf("transcript = %v, want [u0 a1]", got)
	}
	start, takes, _, err := SessionTail(path)
	if err != nil {
		t.Fatal(err)
	}
	if start >= 0 {
		t.Fatalf("tail start = %d with takes %d, want no span: the messages it described were truncated away",
			start, len(takes))
	}
}
