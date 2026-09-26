package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// tailClient records the tail each request carried, and answers with a plain
// end of turn.
type tailClient struct {
	mu        sync.Mutex
	ephemeral []string
}

func (c *tailClient) Name() string { return "tail" }

func (c *tailClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.ephemeral = append(c.ephemeral, req.EphemeralContext)
	c.mu.Unlock()
	out := make(chan provider.Event, 2)
	out <- provider.EventStart{Provider: "tail", Model: req.Model}
	out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
		Role:    provider.RoleAssistant,
		Content: []provider.Content{provider.TextBlock{Text: "done"}},
	}}
	close(out)
	return out, nil
}

// collectTail registers a tail observer and returns the slice it fills.
func collectTail(a *Agent) *[]TailRecord {
	var got []TailRecord
	a.addTailObserver(func(rec TailRecord) { got = append(got, rec) })
	return &got
}

// decayNote is a component note that decays, the shape of lazy tools'
// inactive-group note without the groups: its full text for the first
// decayNoteVerbose requests that carry it, then a one-line form, and nothing
// once cleared. The tail tests use it because they are about recording a
// composition that changes, and a decay is the change a reviewer most needs.
type decayNote struct {
	mu      sync.Mutex
	shown   int
	cleared bool
}

const (
	decayNoteVerbose = 3
	decayNoteFull    = "note.full"
	decayNoteBrief   = "note.brief"
)

func (n *decayNote) segment() Segment {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case n.cleared:
		return Segment{Stability: Volatile, Tag: decayNoteFull}
	case n.shown < decayNoteVerbose:
		return Segment{Stability: Volatile, Tag: decayNoteFull, Content: "inactive: mail (mail_send), mcp:github (gh_pr)"}
	default:
		return Segment{Stability: Volatile, Tag: decayNoteBrief, Content: "inactive: mail, mcp:github"}
	}
}

func (n *decayNote) delivered(ids []string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, id := range ids {
		if id == decayNoteFull || id == decayNoteBrief {
			n.shown++
			return
		}
	}
}

func (n *decayNote) clear() {
	n.mu.Lock()
	n.cleared = true
	n.mu.Unlock()
}

// newNoteAgent is an agent whose frame carries a decayNote.
func newNoteAgent(client provider.Client) (*Agent, *decayNote) {
	a := newTestAgent(client, "m", "sys", Registry{})
	note := &decayNote{}
	testFrameOf(a).setNote(note)
	return a, note
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
	client := &tailClient{}
	a, _ := newNoteAgent(client)
	got := collectTail(a)

	for i := 0; i < decayNoteVerbose+3; i++ {
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
	if ids := tailIDs((*got)[0]); len(ids) != 1 || ids[0] != decayNoteFull {
		t.Errorf("first row = %v, want [%s]", ids, decayNoteFull)
	}
	if ids := tailIDs((*got)[1]); len(ids) != 1 || ids[0] != decayNoteBrief {
		t.Errorf("second row = %v, want [%s]", ids, decayNoteBrief)
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
	client := &tailClient{}
	a, _ := newNoteAgent(client)
	testFrameOf(a).setHost(func() string { return "HOST CARD" })
	got := collectTail(a)

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(*got) != 1 {
		t.Fatalf("recorded %d rows, want 1", len(*got))
	}
	if sent, rec := client.ephemeral[0], tailText((*got)[0].Blocks); sent != rec {
		t.Errorf("recorded tail differs from the tail sent:\n sent: %q\n  rec: %q", sent, rec)
	}
	// Order is the order the model reads: the standing situation first.
	if ids := tailIDs((*got)[0]); len(ids) != 2 || ids[0] != TailHost || ids[1] != decayNoteFull {
		t.Errorf("blocks = %v, want [%s %s]", ids, TailHost, decayNoteFull)
	}
}

// The fingerprint is over block IDENTITIES, never their text. A host note that
// moves every request, as terva's context-pressure percentage does, would
// otherwise write a row every request — recording everything, which is the cost
// the ephemeral design exists to avoid.
func TestTailFingerprintIgnoresChangingText(t *testing.T) {
	client := &tailClient{}
	a := newTestAgent(client, "gpt-5.6-sol", "sys", Registry{})
	got := collectTail(a)
	n := 0
	testFrameOf(a).setHost(func() string { n++; return fmt.Sprintf("the window is %d%% full", 70+n) })

	for i := 0; i < 3; i++ {
		if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
			t.Fatalf("Prompt %d: %v", i, err)
		}
	}

	if len(*got) != 1 {
		t.Fatalf("recorded %d rows, want 1 — the host text moved but its identity did not", len(*got))
	}
	if ids := tailIDs((*got)[0]); len(ids) != 1 || ids[0] != TailHost {
		t.Fatalf("blocks = %v, want [%s]", ids, TailHost)
	}
	// The control: the text really was different across those turns, so the test
	// is not passing because nothing changed.
	if client.ephemeral[0] == client.ephemeral[2] {
		t.Errorf("precondition: the host text did not change between turns, so this proves nothing")
	}
}

// A tail that empties is a change, and the row that ends the previous one — a
// reader reconstructing what any request carried takes the last row at or before
// it, and without this the composition would appear to run forever.
func TestTailRecordsTheDropToEmpty(t *testing.T) {
	client := &tailClient{}
	a, note := newNoteAgent(client)
	got := collectTail(a)

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	// Nothing left to say, so the note goes away.
	note.clear()
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
// It also must not mark anything DELIVERED. A decaying note's verbose run is
// three dispatches; spending them on requests that carried nothing would decay
// it to its one-line form before the model had ever seen it.
func TestContinueTurnNeitherRecordsNorMarksDelivered(t *testing.T) {
	client := &prefillFakeClient{cont: " and on."}
	a := newTestAgent(client, "fake-model", "sys", Registry{})
	testFrameOf(a).setNote(&decayNote{})
	got := collectTail(a)
	a.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: "Tell me a story."}}},
		{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "The knight rode on,"}}},
	})

	for i := 0; i < decayNoteVerbose+1; i++ {
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
	// text, because none of the continues carried it.
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !strings.Contains(client.lastReq.EphemeralContext, "mail_send") {
		t.Errorf("the continue turns burned the note's verbose run: %q", client.lastReq.EphemeralContext)
	}
}

// A retried request must carry — and record — the same tail as the attempt it
// replaces. The composition is peeked for exactly this reason; recording on the
// peek path instead of after the request lands would double-count every retry.
func TestTailIsRecordedOncePerLandedRequest(t *testing.T) {
	client := &tailClient{}
	a, _ := newNoteAgent(client)
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
	blocks := []TailBlock{{ID: TailHost, Text: "a"}, {ID: TailStageCue, Text: "b"}}
	if got, want := tailText(blocks), "a\n\nb"; got != want {
		t.Errorf("tailText = %q, want %q", got, want)
	}
	if tailText(nil) != "" {
		t.Error("an empty composition must render as the empty string, not a separator")
	}
	// Two compositions differing only in text share a fingerprint; differing in
	// identity do not.
	same := []TailBlock{{ID: TailHost, Text: "x"}, {ID: TailStageCue, Text: "y"}}
	if tailFingerprint(blocks) != tailFingerprint(same) {
		t.Error("fingerprint changed with the text; it must key on identity alone")
	}
	if tailFingerprint(blocks) == tailFingerprint(blocks[:1]) {
		t.Error("fingerprint ignored a missing block")
	}
	// Order is part of the identity: the model reads the tail top to bottom, and
	// a reordering is a real change to what it sees first.
	if tailFingerprint(blocks) == tailFingerprint([]TailBlock{blocks[1], blocks[0]}) {
		t.Error("fingerprint ignored block order")
	}
}
