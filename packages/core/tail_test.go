package core

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// collectTail registers a tail observer and returns the slice it fills.
func collectTail(a *Agent) *[]TailRecord {
	var got []TailRecord
	a.AddTailObserver(func(rec TailRecord) { got = append(got, rec) })
	return &got
}

func tailIDs(rec TailRecord) []string {
	ids := make([]string, 0, len(rec.Blocks))
	for _, b := range rec.Blocks {
		ids = append(ids, b.ID)
	}
	return ids
}

// The whole point of finding G2: the tail is composed per request and discarded,
// so a session file holds the model's REACTION to a prompt injection and no
// trace of the injection. Establishing what the model had been shown meant
// reading agent.go. This records it — and records the DECAY, which is the thing
// a reviewer most needs to see, since "the note stopped repeating" is otherwise
// a claim about code rather than a fact about the session.
func TestTailRecordsTheCompositionAndItsDecay(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "m", "sys", decayRegistry())
	a.EnableLazyTools()
	got := collectTail(a)

	for i := 0; i < capabilityNoteVerboseTurns+3; i++ {
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("Prompt %d: %v", i, err)
		}
	}

	// Two rows across seven turns: the full inventory, then its one-line form.
	// A row per request would be seven, which is the cost this design refuses.
	if len(*got) != 2 {
		var ids []string
		for _, r := range *got {
			ids = append(ids, strings.Join(tailIDs(r), "+"))
		}
		t.Fatalf("recorded %d rows %v, want exactly 2 (full, then brief)", len(*got), ids)
	}
	if ids := tailIDs((*got)[0]); len(ids) != 1 || ids[0] != TailCapabilityFull {
		t.Errorf("first row = %v, want [%s]", ids, TailCapabilityFull)
	}
	if ids := tailIDs((*got)[1]); len(ids) != 1 || ids[0] != TailCapabilityBrief {
		t.Errorf("second row = %v, want [%s]", ids, TailCapabilityBrief)
	}

	// And the row carries the text, not just the identity. G1 was diagnosed from
	// the note's WORDING — a size would have shown it fired and nothing about why
	// the model kept answering it.
	if txt := (*got)[0].Blocks[0].Text; !strings.Contains(txt, "mail_send") {
		t.Errorf("the full-inventory row does not carry the inventory: %q", txt)
	}
	if txt := (*got)[1].Blocks[0].Text; strings.Contains(txt, "mail_send") {
		t.Errorf("the brief row carries the full inventory's text: %q", txt)
	}
}

// What was recorded must be what was SENT. A recorder that composes its own view
// of the tail is a second renderer, and this repository's recurring bug is two
// renderers of one concept drifting apart.
func TestTailRecordMatchesWhatTheProviderWasSent(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "m", "sys", decayRegistry())
	a.EnableLazyTools()
	a.ContextProvider = func() string { return "HOST CARD" }
	got := collectTail(a)

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("recorded %d rows, want 1", len(*got))
	}
	if sent, rec := client.ephemeral[0], TailText((*got)[0].Blocks); sent != rec {
		t.Errorf("recorded tail differs from the tail sent:\n sent: %q\n  rec: %q", sent, rec)
	}
	// Order is the order the model reads: the standing situation first.
	if ids := tailIDs((*got)[0]); len(ids) != 2 || ids[0] != TailHost || ids[1] != TailCapabilityFull {
		t.Errorf("blocks = %v, want [%s %s]", ids, TailHost, TailCapabilityFull)
	}
}

// The fingerprint is over block IDENTITIES, never their text. The pressure
// note's percentage moves on every request, so a fingerprint over text would
// write a row every request — recording everything, which is the cost the
// ephemeral design exists to avoid.
func TestTailFingerprintIgnoresChangingText(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "gpt-5.6-sol", "sys", Registry{})
	got := collectTail(a)

	// Past the warn fraction and CLIMBING A BAND each turn (272k window: 71%,
	// 81%, 86%). The band has to escalate for the note to ride every request —
	// inside one band the cadence deliberately stays quiet, which is a
	// different property, asserted in context_pressure_test.go. What this test
	// needs is three requests that all carry the note with different text.
	for i, used := range []int{195_000, 220_000, 235_000} {
		a.SeedLastTurnUsage(provider.Usage{InputTokens: used})
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("Prompt %d: %v", i, err)
		}
	}

	if len(*got) != 1 {
		t.Fatalf("recorded %d rows, want 1 — the note's percentage moved but its identity did not", len(*got))
	}
	if ids := tailIDs((*got)[0]); len(ids) != 1 || ids[0] != TailPressure {
		t.Fatalf("blocks = %v, want [%s]", ids, TailPressure)
	}
	// The control: the text really was different across those turns, so the test
	// is not passing because nothing changed.
	if client.ephemeral[0] == client.ephemeral[2] {
		t.Errorf("precondition: the pressure note did not change between turns, so this proves nothing")
	}
}

// A tail that empties is a change, and the row that ends the previous one — a
// reader reconstructing what any request carried takes the last row at or before
// it, and without this the composition would appear to run forever.
func TestTailRecordsTheDropToEmpty(t *testing.T) {
	client := &reqCaptureClient{}
	reg := decayRegistry()
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()
	got := collectTail(a)

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	// Every group active: nothing is hidden, so the note goes away.
	a.ActivateGroup("mail")
	a.ActivateGroup("mcp:github")
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt after activation: %v", err)
	}

	if len(*got) != 2 {
		t.Fatalf("recorded %d rows, want 2 (the note, then its absence)", len(*got))
	}
	if n := len((*got)[1].Blocks); n != 0 {
		t.Errorf("final row has %d blocks, want 0 — the tail is empty now", n)
	}
	// And it settles: a tail that stays empty writes nothing more.
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("third Prompt: %v", err)
	}
	if len(*got) != 2 {
		t.Errorf("an unchanged empty tail wrote another row: %d total", len(*got))
	}
}

// A continue turn suppresses the whole tail for exactly one request. That is not
// a change to the standing composition, and recording it would write two rows
// per continue turn — a flap, not a fact.
//
// It also must not mark anything DELIVERED. The capability note's verbose run is
// three dispatches; spending them on requests that carried nothing would decay
// the inventory to its one-line form before the model had ever seen it.
func TestContinueTurnNeitherRecordsNorMarksDelivered(t *testing.T) {
	client := &prefillFakeClient{cont: " and on."}
	a := NewAgent(client, "fake-model", "sys", decayRegistry())
	a.EnableLazyTools()
	got := collectTail(a)
	a.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "Tell me a story."}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "The knight rode on,"}}},
	})

	for i := 0; i < capabilityNoteVerboseTurns+1; i++ {
		if err := a.ContinueAssistant(context.Background(), nil); err != nil {
			t.Fatalf("ContinueAssistant %d: %v", i, err)
		}
		if client.lastReq.EphemeralContext != "" {
			t.Fatalf("precondition: continue turn %d sent a tail: %q", i, client.lastReq.EphemeralContext)
		}
	}
	if len(*got) != 0 {
		t.Errorf("a suppressed tail wrote %d rows; it is a one-request suppression, not a change", len(*got))
	}

	// The verbose run is intact: the next ordinary turn still gets the full
	// inventory, because none of the continues carried it.
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !strings.Contains(client.lastReq.EphemeralContext, "mail_send") {
		t.Errorf("the continue turns burned the note's verbose run: %q", client.lastReq.EphemeralContext)
	}
}

// The stall nudge is NOT gated the same way, and this pins why so the asymmetry
// is not "tidied" into symmetry later: a nudge lives for one turn, because
// runLoop resets the tracker at every turn boundary. So it cannot be pending
// when a continue turn begins, and the case the capability gate exists for
// cannot arise for the nudge.
func TestAStallNudgeDoesNotSurviveATurnBoundary(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "m", "sys", Registry{})
	a.stall.pending = "[stuck loop] you have called read on the same file four times"

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if strings.Contains(client.ephemeral[0], "stuck loop") {
		t.Error("a nudge armed before the turn was delivered; it is scoped to the turn that armed it")
	}
	if a.stall.nudge() != "" {
		t.Error("the tracker did not reset at the turn boundary")
	}
}

// A retried request must carry — and record — the same tail as the attempt it
// replaces. The composition is peeked for exactly this reason; recording on the
// peek path instead of after the request lands would double-count every retry.
func TestTailIsRecordedOncePerLandedRequest(t *testing.T) {
	client := &reqCaptureClient{}
	a := NewAgent(client, "m", "sys", decayRegistry())
	a.EnableLazyTools()
	got := collectTail(a)

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("recorded %d rows for one request, want 1", len(*got))
	}
	// A second identical turn changes nothing, so it records nothing — the
	// property that makes carrying full text affordable.
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("second Prompt: %v", err)
	}
	if len(*got) != 1 {
		t.Errorf("an unchanged composition recorded again: %d rows", len(*got))
	}
}

func TestTailFingerprintAndText(t *testing.T) {
	blocks := []TailBlock{{ID: TailHost, Text: "a"}, {ID: TailPressure, Text: "b"}}
	if got, want := TailText(blocks), "a\n\nb"; got != want {
		t.Errorf("TailText = %q, want %q", got, want)
	}
	if TailText(nil) != "" {
		t.Error("an empty composition must render as the empty string, not a separator")
	}
	// Two compositions differing only in text share a fingerprint; differing in
	// identity do not.
	same := []TailBlock{{ID: TailHost, Text: "x"}, {ID: TailPressure, Text: "y"}}
	if TailFingerprint(blocks) != TailFingerprint(same) {
		t.Error("fingerprint changed with the text; it must key on identity alone")
	}
	if TailFingerprint(blocks) == TailFingerprint(blocks[:1]) {
		t.Error("fingerprint ignored a missing block")
	}
	// Order is part of the identity: the model reads the tail top to bottom, and
	// a reordering is a real change to what it sees first.
	if TailFingerprint(blocks) == TailFingerprint([]TailBlock{blocks[1], blocks[0]}) {
		t.Error("fingerprint ignored block order")
	}
}
