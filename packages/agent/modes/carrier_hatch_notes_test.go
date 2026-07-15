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
