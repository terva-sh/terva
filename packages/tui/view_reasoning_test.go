package tui

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/provider"
)

// Recorded thinking used to be written to the session file and then rendered by
// nothing at all: MessageToWire carried the summary to every surface and no
// surface had a ReasoningBlock arm. These pin the box that closes that gap, and
// the boundary that keeps it from colliding with the ephemeral live line.

func reasoningView(summary, prose string) View {
	return View{Theme: Dark, Now: func() time.Time { return pinnedNow }, Messages: []provider.Message{{
		Role: provider.RoleAssistant,
		Content: []provider.Content{
			provider.ReasoningBlock{
				Summary: summary,
				Shape:   provider.ReasoningShapeAnthropicThinking,
			},
			provider.TextBlock{Text: prose},
		},
	}}}
}

// A SUPERSEDED thinking block collapses to its marker: present and announced,
// but not pushing the answer off the screen.
//
// The NEWEST block now stays open instead (see
// TestNewestThinkingIsOpenAndOlderOnesCollapse). Two complaints bound this
// behaviour from opposite sides: thinking that scrolled past unread is why the
// newest one is open, and a session filling with stale deliberation is why
// every older one is not.
func TestSupersededReasoningCollapsesToAMarker(t *testing.T) {
	v := reasoningView("weighing two indexes", "I used the btree.")
	// A later turn takes the open slot, superseding the block under test.
	v.Messages = append(v.Messages,
		provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "and then?"}}},
		provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{
			provider.ReasoningBlock{Summary: "checking the newer path"},
			provider.TextBlock{Text: "Then I cached it."},
		}},
	)
	plain := stripANSI(strings.Join(v.Build(80), "\n"))

	if !strings.Contains(plain, "thinking") {
		t.Fatalf("no thinking marker on a message carrying recorded reasoning:\n%s", plain)
	}
	if !strings.Contains(plain, "ctrl+o") {
		t.Errorf("marker does not say how to open it:\n%s", plain)
	}
	if strings.Contains(plain, "weighing two indexes") {
		t.Errorf("collapsed must not spill the summary:\n%s", plain)
	}
	if !strings.Contains(plain, "I used the btree.") {
		t.Errorf("the reply itself went missing:\n%s", plain)
	}
}

// ctrl+o is the same control the compaction block and the tool boxes ride, which
// is why the box needed no setting and no new key of its own.
func TestExpandAllRevealsRecordedReasoning(t *testing.T) {
	v := reasoningView("weighing two indexes", "I used the btree.")
	v.ExpandAll = true
	plain := stripANSI(strings.Join(v.Build(80), "\n"))

	if !strings.Contains(plain, "weighing two indexes") {
		t.Fatalf("ExpandAll did not reveal the summary:\n%s", plain)
	}
	if !strings.Contains(plain, "I used the btree.") {
		t.Errorf("revealing the thinking cost the reply:\n%s", plain)
	}
}

// The boundary between the two reasoning paths. Any turn with recording off
// blanks Summary (core.stripUnrecordedSummaries) and leaves the block in place,
// so the transcript is full of reasoning blocks with nothing readable in them.
// Drawing a marker for those would promise an expansion that is empty.
//
// 🪤 The test that would let this regress is one that keys off the Shape tag:
// a stripped Thinking block and a native ThinkingOpaque one are byte-identical
// apart from that tag, so only the emptiness of Summary answers the display
// question honestly.
func TestBlankedSummaryDrawsNoReasoningAffordance(t *testing.T) {
	v := reasoningView("", "I used the btree.")
	plain := stripANSI(strings.Join(v.Build(80), "\n"))

	if strings.Contains(plain, "thinking") {
		t.Errorf("a display-only block promised an expansion with nothing behind it:\n%s", plain)
	}
	if !strings.Contains(plain, "I used the btree.") {
		t.Errorf("the reply itself went missing:\n%s", plain)
	}
}

// Block order is provider-specific — Anthropic emits thinking ahead of the text,
// the Responses backends emit it after — so rendering in content order would put
// the model's deliberation below its conclusion on half the providers. The box
// is hoisted out of content order for exactly this reason.
func TestReasoningRendersBeforeProseWhateverTheBlockOrder(t *testing.T) {
	v := View{Theme: Dark, Now: func() time.Time { return pinnedNow }, Messages: []provider.Message{{
		Role: provider.RoleAssistant,
		Content: []provider.Content{
			// Text FIRST, thinking after: the codex/Responses shape.
			provider.TextBlock{Text: "I used the btree."},
			provider.ReasoningBlock{Summary: "weighing two indexes"},
		},
	}}}
	v.ExpandAll = true
	plain := stripANSI(strings.Join(v.Build(80), "\n"))

	thinking := strings.Index(plain, "weighing two indexes")
	answer := strings.Index(plain, "I used the btree.")
	if thinking < 0 || answer < 0 {
		t.Fatalf("expected both the thinking and the reply:\n%s", plain)
	}
	if thinking > answer {
		t.Errorf("thinking rendered after the answer it produced:\n%s", plain)
	}
}
