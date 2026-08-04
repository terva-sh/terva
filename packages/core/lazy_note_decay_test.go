package core

import (
	"context"
	"strings"
	"testing"
)

func decayRegistry() Registry {
	return Registry{
		"read":      &flagTool{name: "read"},
		"mail_send": extTool("mail_send", "mail"),
		"gh_pr":     extTool("gh_pr", "mcp:github"),
	}
}

// The inactive-group note arrives on EVERY turn while any group is hidden. Sent
// in full each time it is not information after the first few, and it reads as a
// question: one reviewed session shows the model answering it once and then
// answering it again in 109 of the next 217 assistant messages, 44 of those with
// the identical sentence. The note is ephemeral, but the model's ANSWER is an
// assistant message — permanent, re-sent every turn, and it survives compaction.
// So the full inventory decays to a one-line form once it has been offered.
func TestCapabilityNoteDecaysToOneLine(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "m", "sys", decayRegistry())
	a.EnableLazyTools()

	for i := 0; i < capabilityNoteVerboseTurns+3; i++ {
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("Prompt %d: %v", i, err)
		}
	}
	if len(client.ephemeral) < capabilityNoteVerboseTurns+3 {
		t.Fatalf("captured %d dispatches, want at least %d", len(client.ephemeral), capabilityNoteVerboseTurns+3)
	}

	// The opening run carries the full inventory: group names AND tool names.
	for i := 0; i < capabilityNoteVerboseTurns; i++ {
		note := client.ephemeral[i]
		if !strings.Contains(note, "mail_send") || !strings.Contains(note, "gh_pr") {
			t.Errorf("dispatch %d should carry the full inventory, got %q", i, note)
		}
	}

	// After it, the tool names are gone but the groups remain nameable, so
	// activate_tools' description still points at something real.
	for i := capabilityNoteVerboseTurns; i < len(client.ephemeral); i++ {
		note := client.ephemeral[i]
		if strings.Contains(note, "mail_send") || strings.Contains(note, "gh_pr") {
			t.Errorf("dispatch %d should have decayed to the brief form, got %q", i, note)
		}
		if !strings.Contains(note, "[inactive tool groups]") {
			t.Errorf("dispatch %d dropped the note entirely, got %q", i, note)
		}
		for _, g := range []string{"mail", "mcp:github"} {
			if !strings.Contains(note, g) {
				t.Errorf("dispatch %d brief form should still name group %q, got %q", i, g, note)
			}
		}
	}
}

// Both forms must prohibit a reply BEFORE the inventory the prohibition
// governs. The tic is a model answering what it takes to be a question, and
// the order is measured, not stylistic: with the excusal mid-block Haiku
// answered the note instead of the user in 20 of 20 runs, and with the
// prohibition first it answered the user in 20 of 20 (scripts/eval,
// session-inspect-cost final-answer row, 2026-08).
func TestCapabilityNoteProhibitsReplyFirst(t *testing.T) {
	reg := decayRegistry()
	active := map[string]bool{}
	full := inactiveGroupNote(reg, active)
	brief := inactiveGroupBrief(reg, active)

	for name, note := range map[string]string{"full": full, "brief": brief} {
		if note == "" {
			t.Fatalf("%s note is empty", name)
		}
		lower := strings.ToLower(note)
		at := strings.Index(lower, "do not reply")
		if at < 0 {
			t.Errorf("%s note does not excuse the model from replying: %q", name, note)
			continue
		}
		if inv := strings.Index(lower, "activate_tools"); inv >= 0 && inv < at {
			t.Errorf("%s note buries the prohibition after the detail it governs: %q", name, note)
		}
	}
	// The old opener was an imperative directed at the model, which is what it
	// answered every turn.
	if strings.Contains(full, "Call activate_tools") {
		t.Errorf("full note still opens with an imperative: %q", full)
	}
}

// A changed inactive set is news again — a newly installed extension must be
// announced in full, not inherit the previous set's silence.
func TestCapabilityNoteReShowsWhenTheSetChanges(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "m", "sys", decayRegistry())
	a.EnableLazyTools()

	for i := 0; i < capabilityNoteVerboseTurns+1; i++ {
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("Prompt %d: %v", i, err)
		}
	}
	settled := client.ephemeral[len(client.ephemeral)-1]
	if strings.Contains(settled, "mail_send") {
		t.Fatalf("precondition: note should have decayed by now, got %q", settled)
	}

	// A new group appears.
	reg := decayRegistry()
	reg["cal_add"] = extTool("cal_add", "calendar")
	a.SetTools(reg)
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt after change: %v", err)
	}
	note := client.ephemeral[len(client.ephemeral)-1]
	if !strings.Contains(note, "cal_add") {
		t.Errorf("a changed inactive set must re-show the full inventory, got %q", note)
	}
}

// Peeking must not advance the decay: /context renders the note through
// CapabilityNote, and inspecting the context cannot be allowed to change what
// the next turn actually sends.
func TestCapabilityNotePreviewDoesNotAdvanceDecay(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "m", "sys", decayRegistry())
	a.EnableLazyTools()

	for i := 0; i < capabilityNoteVerboseTurns*3; i++ {
		if got := a.CapabilityNote(); !strings.Contains(got, "mail_send") {
			t.Fatalf("preview %d should still be the full note (nothing dispatched yet), got %q", i, got)
		}
	}
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatal(err)
	}
	if got := client.ephemeral[0]; !strings.Contains(got, "mail_send") {
		t.Errorf("previews consumed the verbose run; first dispatch got %q", got)
	}
}

// The preview must agree with what is actually on the wire, or /context's
// accounting of what deferred discovery costs is wrong on any settled session.
func TestCapabilityNotePreviewMatchesTheWire(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "m", "sys", decayRegistry())
	a.EnableLazyTools()

	// Read the preview BEFORE dispatching: it describes the tail the next turn
	// will carry, which is the question /context is asked.
	for i := 0; i < capabilityNoteVerboseTurns+2; i++ {
		preview := a.CapabilityNote()
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("Prompt %d: %v", i, err)
		}
		if sent := client.ephemeral[len(client.ephemeral)-1]; !strings.Contains(sent, preview) {
			t.Errorf("turn %d: preview %q is not what the wire carried %q", i, preview, sent)
		}
	}
}
