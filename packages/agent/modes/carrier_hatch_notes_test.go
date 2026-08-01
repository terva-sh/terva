package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// The stuck-loop hatch's live events (PR 3) reach the TUI as ctrlproto events
// and render as inline chat-area notes, so the operator sees the harness act in
// real time instead of finding it in the session log afterwards.

func TestCarrierStallEventAddsInlineNote(t *testing.T) {
	i := newNotesTestInteractive()
	i.handleCarrierEvent(ctrlproto.ConversationEvent(core.WireEvent{
		Type:  "stall",
		Stall: &core.WireStall{Axis: "churn", Tool: "task_update", Detail: "activate_next must name a different task"},
	}))
	if len(i.extNotes) != 1 {
		t.Fatalf("a stall event should add exactly one inline note, got %d", len(i.extNotes))
	}
	if !strings.Contains(i.extNotes[0], "loop detected") || !strings.Contains(i.extNotes[0], "task_update") {
		t.Errorf("the note should announce the loop and name the looping tool: %q", i.extNotes[0])
	}
}

// Repeated nudges must COALESCE into one counted line, not stack — a wedged run
// fires many, and a growing pile of notes at the bottom of the TUI is noise.
func TestCarrierStallEventsCoalesceIntoOneCountedNote(t *testing.T) {
	i := newNotesTestInteractive()
	// An unrelated ext note to make sure coalescing doesn't disturb others.
	i.extNotes = append(i.extNotes, "  [kagi] working")
	for n := 0; n < 5; n++ {
		i.handleCarrierEvent(ctrlproto.ConversationEvent(core.WireEvent{
			Type:  "stall",
			Stall: &core.WireStall{Axis: "spin", Tool: "read"},
		}))
	}
	stalls := 0
	for _, note := range i.extNotes {
		if strings.Contains(note, stallNudgeGlyph) {
			stalls++
		}
	}
	if stalls != 1 {
		t.Fatalf("5 nudges must collapse to ONE note, got %d stall lines", stalls)
	}
	if len(i.extNotes) != 2 { // the coalesced stall line + the untouched ext note
		t.Errorf("coalescing should leave other notes alone: got %d notes %q", len(i.extNotes), i.extNotes)
	}
	// The single line reflects the count (5) so persistence is still visible.
	var stall string
	for _, note := range i.extNotes {
		if strings.Contains(note, stallNudgeGlyph) {
			stall = note
		}
	}
	if !strings.Contains(stall, "5") {
		t.Errorf("the coalesced note should count the nudges: %q", stall)
	}
}

// Rungs 3 and 4 are things terva DID, and they must not disappear into the
// nudge counter. A pane reading "nudged the model 9×" while calls were being
// blocked and the turn was ending understates all of it.
func TestCarrierStallActingRungsReadDistinctly(t *testing.T) {
	i := newNotesTestInteractive()
	stall := func(rung int, detail string) {
		i.handleCarrierEvent(ctrlproto.ConversationEvent(core.WireEvent{
			Type:  "stall",
			Stall: &core.WireStall{Axis: "spin", Tool: "task_update", Detail: detail, Rung: rung},
		}))
	}
	stall(1, "")
	stall(1, "")
	stall(3, "")
	stall(3, "")
	stall(4, "ended the turn: task_update repeated 7× with the same result")

	var nudge, refusal, stop string
	for _, note := range i.extNotes {
		switch {
		case strings.Contains(note, stallStopGlyph):
			stop = note
		case strings.Contains(note, stallRefuseGlyph):
			refusal = note
		case strings.Contains(note, stallNudgeGlyph):
			nudge = note
		}
	}
	if len(i.extNotes) != 3 {
		t.Fatalf("want one line per rung reached, got %d: %q", len(i.extNotes), i.extNotes)
	}
	// Each rung coalesces on its OWN glyph, so the counts stay separate.
	if !strings.Contains(nudge, "nudged") || !strings.Contains(nudge, "2") {
		t.Errorf("the nudge line should still count nudges: %q", nudge)
	}
	if !strings.Contains(refusal, "refused to run") || !strings.Contains(refusal, "2") {
		t.Errorf("the refusal line should count refusals separately: %q", refusal)
	}
	if !strings.Contains(stop, "ended the turn") {
		t.Errorf("the terminal line should say the turn ended and why: %q", stop)
	}
}

// The terminal note fires once per turn and must not stack if the event is
// somehow delivered twice.
func TestCarrierStallGiveUpNoteIsNotRepeated(t *testing.T) {
	i := newNotesTestInteractive()
	for n := 0; n < 3; n++ {
		i.handleCarrierEvent(ctrlproto.ConversationEvent(core.WireEvent{
			Type:  "stall",
			Stall: &core.WireStall{Axis: "spin", Tool: "task_update", Detail: "ended the turn", Rung: 4},
		}))
	}
	if len(i.extNotes) != 1 {
		t.Fatalf("the give-up note should appear once, got %d: %q", len(i.extNotes), i.extNotes)
	}
}

func TestCarrierEscalationEventNotesByDisposition(t *testing.T) {
	cases := []struct {
		name string
		esc  core.WireEscalation
		want []string // substrings all expected in the rendered note
	}{
		{"switched", core.WireEscalation{Disposition: "switched", ToModel: "gpt-5.6-sol", ToProvider: "openai-codex"},
			[]string{"escalated to", "gpt-5.6-sol", "openai-codex"}},
		{"failed", core.WireEscalation{Disposition: "failed", ToModel: "gpt-5.6-sol", Detail: "no credential for anthropic"},
			[]string{"failed", "no credential"}},
		{"declined", core.WireEscalation{Disposition: "declined", FromModel: "gemma-4-26b"},
			[]string{"declined", "gemma-4-26b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := newNotesTestInteractive()
			e := tc.esc
			i.handleCarrierEvent(ctrlproto.ConversationEvent(core.WireEvent{Type: "escalation", Escalation: &e}))
			if len(i.extNotes) != 1 {
				t.Fatalf("want exactly one note, got %d", len(i.extNotes))
			}
			for _, want := range tc.want {
				if !strings.Contains(i.extNotes[0], want) {
					t.Errorf("%s note %q is missing %q", tc.name, i.extNotes[0], want)
				}
			}
		})
	}
}

// A malformed event (type set, payload nil — e.g. an older peer, or a dropped
// field) must not panic or add a note.
func TestCarrierHatchEventsTolerateNilPayload(t *testing.T) {
	i := newNotesTestInteractive()
	i.handleCarrierEvent(ctrlproto.ConversationEvent(core.WireEvent{Type: "stall"}))
	i.handleCarrierEvent(ctrlproto.ConversationEvent(core.WireEvent{Type: "escalation"}))
	if len(i.extNotes) != 0 {
		t.Errorf("nil-payload hatch events should add no notes, got %d", len(i.extNotes))
	}
}
