package talkoot

import (
	"reflect"
	"strings"
	"testing"
)

// Each signal is a sealed room line that a restart reads back whole. The
// router rebuilds nothing from them, so the statuses after a restart are the
// ones before it.
func TestSignalsAreRoomLinesAReplayReads(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.router.TurnEnded("jev", 2); err != nil {
		t.Fatal(err)
	}
	before := f.router.Statuses()
	steps := []func() error{
		func() error { return f.router.ToolFailed("jev", "call-0", "edit", "old_string not found") },
		func() error { return f.router.Retried("jev", 2, "overloaded") },
		func() error { return f.router.CardOpened("jev", CardPermission, "call-1", "bash") },
		func() error { return f.router.CardClosed("jev", CardPermission, "call-1", OutcomeDenied) },
	}
	for _, step := range steps {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	signals := func() []Line {
		var out []Line
		for _, l := range f.lines() {
			switch l.Type {
			case LineToolError, LineRetry, LineCardOpen, LineCardClose:
				l.At, l.Kid, l.MAC = f.clock.now(), "", ""
				out = append(out, l)
			}
		}
		return out
	}
	got := signals()
	at := f.clock.now()
	want := []Line{
		{Type: LineToolError, At: at, Member: "jev", Ref: "call-0", Tool: "edit", Reason: "old_string not found"},
		{Type: LineRetry, At: at, Member: "jev", Attempt: 2, Reason: "overloaded"},
		{Type: LineCardOpen, At: at, Member: "jev", Card: CardPermission, Ref: "call-1", Tool: "bash"},
		{Type: LineCardClose, At: at, Member: "jev", Card: CardPermission, Ref: "call-1", Outcome: OutcomeDenied},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("signal lines = %+v\nwant %+v", got, want)
	}
	f.reopen()
	if after := f.router.Statuses(); !reflect.DeepEqual(after, before) {
		t.Fatalf("a replay of signals changed the statuses:\nbefore %+v\nafter  %+v", before, after)
	}
	// The router's own writes after the replay still seal to the signals.
	if err := f.router.TurnEnded("jev", 1); err != nil {
		t.Fatal(err)
	}
	if got := signals(); !reflect.DeepEqual(got, want) {
		t.Fatalf("after a restart the signal lines = %+v", got)
	}
}

// A worker reports a tool's name and a call's id, so the router drops their
// control characters and bounds them.
func TestASignalBoundsTheWorkersFields(t *testing.T) {
	f := newFixture(t, nil)
	long := strings.Repeat("x", 4*maxSignalField)
	if err := f.router.ToolFailed("jev", long, "ba\nsh\x1b[31m"+long, "x"); err != nil {
		t.Fatal(err)
	}
	var l Line
	for _, x := range f.lines() {
		if x.Type == LineToolError {
			l = x
		}
	}
	if len(l.Tool) > maxSignalField || len(l.Ref) > maxSignalField || !strings.HasPrefix(l.Tool, "bash[31m") {
		t.Fatalf("tool %q (%d bytes), ref %d bytes", l.Tool, len(l.Tool), len(l.Ref))
	}
}

// A signal names a member id. A member that left the roster still had it, so
// any id passes, and a name that is no id does not.
func TestASignalNeedsAMemberID(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.router.ToolFailed("gone", "c1", "read", "x"); err != nil {
		t.Fatalf("a member that left: %v", err)
	}
	if err := f.router.Retried("Not An ID", 1, "x"); err == nil {
		t.Fatal("a name that is no member id passed")
	}
}
