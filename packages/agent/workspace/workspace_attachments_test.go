package workspace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/attach"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The end-to-end claim of the attachment feature, at the seam where it is
// decided: a client names a staged file by id, and the MODEL is told where to
// read it. Everything either side of this (the upload route, the sandbox grant)
// is worthless if the path never reaches the turn.

// attachTestWorkspace is chatTestWorkspace plus a real staging store, since what
// is under test here is resolution against one.
func attachTestWorkspace(t *testing.T, id string) (*Workspace, *wsSession, *attach.Store) {
	t.Helper()
	w, s, _ := chatTestWorkspace(t, id)
	store := attach.NewStoreAt(testsupport.TempDir(t))
	w.attachments = store
	return w, s, store
}

// userTurn is the text of the first user message the agent recorded.
func userTurn(t *testing.T, s *wsSession) string {
	t.Helper()
	waitFor(t, "the prompt to reach the model", func() bool { return len(s.agent.Messages()) > 0 })
	for _, m := range s.agent.Messages() {
		if m.Role != provider.RoleUser {
			continue
		}
		var b strings.Builder
		for _, c := range m.Content {
			if tb, ok := c.(provider.TextBlock); ok {
				b.WriteString(tb.Text)
			}
		}
		return b.String()
	}
	t.Fatal("no user message reached the model")
	return ""
}

// userMessage is the first user-role message the agent recorded.
func userMessage(t *testing.T, s *wsSession) provider.Message {
	t.Helper()
	waitFor(t, "the prompt to reach the model", func() bool { return len(s.agent.Messages()) > 0 })
	for _, m := range s.agent.Messages() {
		if m.Role == provider.RoleUser {
			return m
		}
	}
	t.Fatal("no user message reached the model")
	return provider.Message{}
}

func TestPromptTellsTheModelWhereToReadAnAttachment(t *testing.T) {
	w, s, store := attachTestWorkspace(t, "s1")
	staged, err := store.Stage("s1", "filters.xml", strings.NewReader("<filters/>"))
	if err != nil {
		t.Fatal(err)
	}

	err = w.Prompt(context.Background(), "s1", ctrlproto.PromptParams{
		Text:        "check these filters",
		Attachments: []ctrlproto.AttachmentRef{{ID: staged.ID}},
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}

	turn := userTurn(t, s)
	if !strings.Contains(turn, staged.Path) {
		t.Errorf("the turn does not name the staged path.\nwant it to contain: %s\ngot:\n%s", staged.Path, turn)
	}
	if !strings.Contains(turn, "check these filters") {
		t.Errorf("the manifest displaced the user's own text:\n%s", turn)
	}
}

// The split is the whole point: the model reads one concatenated turn either
// way, but only a separate leading block lets a client drop the machine prose
// and keep the user's words. Two blocks, preamble FIRST — the contract the wire
// form and the title seed both key on.
func TestAttachmentPreambleIsItsOwnLeadingBlock(t *testing.T) {
	w, s, store := attachTestWorkspace(t, "s1")
	staged, _ := store.Stage("s1", "filters.xml", strings.NewReader("<filters/>"))

	_ = w.Prompt(context.Background(), "s1", ctrlproto.PromptParams{
		Text:        "check these filters",
		Attachments: []ctrlproto.AttachmentRef{{ID: staged.ID}},
	})

	m := userMessage(t, s)
	var texts []string
	for _, c := range m.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			texts = append(texts, tb.Text)
		}
	}
	if len(texts) != 2 {
		t.Fatalf("got %d text blocks %q, want 2 (preamble, then the user's words)", len(texts), texts)
	}
	if !strings.Contains(texts[0], staged.Path) {
		t.Errorf("block 0 is not the preamble: %q", texts[0])
	}
	if texts[1] != "check these filters" {
		t.Errorf("block 1 = %q, want the user's text alone", texts[1])
	}
}

// The metadata is what a client labels the message with. It describes only what
// resolved — a label for a file the daemon could not find would be a claim it
// cannot support — and carries no path or id, because the file is deliberately
// not retrievable from it.
func TestAttachmentMetaDescribesWhatResolved(t *testing.T) {
	w, s, store := attachTestWorkspace(t, "s1")
	staged, _ := store.Stage("s1", "filters.xml", strings.NewReader("<filters/>"))

	_ = w.Prompt(context.Background(), "s1", ctrlproto.PromptParams{
		Text:        "check these",
		Attachments: []ctrlproto.AttachmentRef{{ID: staged.ID}, {ID: "att_gone"}},
	})

	raw := userMessage(t, s).Meta[core.MetaAttachments]
	if raw == "" {
		t.Fatal("no attachment metadata on the message")
	}
	var got []core.WireAttachment
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("metadata is not a WireAttachment list: %v (%s)", err, raw)
	}
	if len(got) != 1 {
		t.Fatalf("described %d attachments, want only the one that resolved: %+v", len(got), got)
	}
	if got[0].Name != "filters.xml" || got[0].Kind != "document" || got[0].Size != 10 {
		t.Errorf("described = %+v, want it to label the staged file", got[0])
	}
	for _, leak := range []string{staged.Path, staged.ID} {
		if strings.Contains(raw, leak) {
			t.Errorf("metadata leaks %q — it is a label, not a handle: %s", leak, raw)
		}
	}
}

// The client drops content[0] when the message says it is a preamble, so the
// flag and the block must travel together or it would suppress the user's own
// words instead.
func TestWireFormCarriesAttachmentsAlongsideThePreamble(t *testing.T) {
	w, s, store := attachTestWorkspace(t, "s1")
	staged, _ := store.Stage("s1", "filters.xml", strings.NewReader("<filters/>"))

	_ = w.Prompt(context.Background(), "s1", ctrlproto.PromptParams{
		Text:        "check these filters",
		Attachments: []ctrlproto.AttachmentRef{{ID: staged.ID}},
	})

	wire := core.MessageToWireFull(userMessage(t, s))
	if len(wire.Attachments) != 1 || wire.Attachments[0].Name != "filters.xml" {
		t.Fatalf("wire attachments = %+v, want the staged file described", wire.Attachments)
	}
	if !wire.Preamble {
		t.Error("the wire form does not flag its preamble — the client will render the staging path as the user's words")
	}
	if len(wire.Content) < 2 || wire.Content[0].Type != "text" || !strings.Contains(wire.Content[0].Text, staged.Path) {
		t.Fatalf("wire content[0] is not the preamble: %+v", wire.Content)
	}
}

// THE case the feature is built around, and the one where keying the preamble
// off the attachment list broke: a message whose files had all been swept
// before it was sent.
//
// The preamble is still there — the model is being asked about files and has to
// be told it is not getting them — so the flag that hides it must be there too,
// and the count must survive to the wire. Inferring "there is a preamble" from
// "there are attachments" produced neither: the manifest prose rendered as the
// user's own words, and the session took its title from it.
func TestWireFormFlagsThePreambleWhenEveryAttachmentExpired(t *testing.T) {
	w, s, _ := attachTestWorkspace(t, "s1")

	_ = w.Prompt(context.Background(), "s1", ctrlproto.PromptParams{
		Text:        "check these filters",
		Attachments: []ctrlproto.AttachmentRef{{ID: "att_gone"}, {ID: "att_also_gone"}},
	})

	wire := core.MessageToWireFull(userMessage(t, s))
	if len(wire.Attachments) != 0 {
		t.Fatalf("wire attachments = %+v, want none — nothing resolved", wire.Attachments)
	}
	if !wire.Preamble {
		t.Error("no preamble flag, but content[0] IS the preamble — the panel will print the manifest inside the user's bubble")
	}
	if wire.AttachmentsMissing != 2 {
		t.Errorf("attachments_missing = %d, want 2 — otherwise this reads as a message that carried no files at all", wire.AttachmentsMissing)
	}
	if len(wire.Content) < 2 || !strings.Contains(wire.Content[0].Text, "no longer on disk") {
		t.Fatalf("content[0] is not the expiry preamble: %+v", wire.Content)
	}
}

// The partial case: one file survived, one did not. The label describes what is
// really there and the count says the rest is gone, so the user is not left
// comparing an answer against a set the panel never showed them.
func TestWireFormCountsMissingBesideWhatResolved(t *testing.T) {
	w, s, store := attachTestWorkspace(t, "s1")
	staged, _ := store.Stage("s1", "filters.xml", strings.NewReader("<filters/>"))

	_ = w.Prompt(context.Background(), "s1", ctrlproto.PromptParams{
		Text:        "check these",
		Attachments: []ctrlproto.AttachmentRef{{ID: staged.ID}, {ID: "att_gone"}},
	})

	wire := core.MessageToWireFull(userMessage(t, s))
	if len(wire.Attachments) != 1 || wire.AttachmentsMissing != 1 {
		t.Errorf("wire described %d and counted %d missing, want 1 and 1", len(wire.Attachments), wire.AttachmentsMissing)
	}
}

// An ordinary turn must gain none of it. The three fields are omitempty, so a
// message with no attachments serializes exactly as it did before they existed.
func TestWireFormOfAnOrdinaryTurnIsUnchanged(t *testing.T) {
	w, s, _ := attachTestWorkspace(t, "s1")

	_ = w.Prompt(context.Background(), "s1", ctrlproto.PromptParams{Text: "just a question"})

	wire := core.MessageToWireFull(userMessage(t, s))
	if wire.Preamble || wire.AttachmentsMissing != 0 || len(wire.Attachments) != 0 {
		t.Errorf("a plain turn carries attachment state: preamble=%v missing=%d files=%+v",
			wire.Preamble, wire.AttachmentsMissing, wire.Attachments)
	}
	encoded, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"preamble", "attachments"} {
		if strings.Contains(string(encoded), `"`+field+`"`) {
			t.Errorf("a plain turn's wire form mentions %q: %s", field, encoded)
		}
	}
}

// A title seeded off a $TERVA_HOME path instead of the question asked was the
// concrete cost of gluing the preamble onto the user's text.
func TestTitleSeedIgnoresTheAttachmentPreamble(t *testing.T) {
	w, s, store := attachTestWorkspace(t, "s1")
	staged, _ := store.Stage("s1", "filters.xml", strings.NewReader("<filters/>"))

	_ = w.Prompt(context.Background(), "s1", ctrlproto.PromptParams{
		Text:        "check these filters",
		Attachments: []ctrlproto.AttachmentRef{{ID: staged.ID}},
	})

	seed := core.BuildTitleSeed([]provider.Message{userMessage(t, s)}, 400)
	if strings.Contains(seed, "attachments") || strings.Contains(seed, staged.Path) {
		t.Errorf("the title seed is made of a staging path:\n%s", seed)
	}
	if !strings.Contains(seed, "check these filters") {
		t.Errorf("the title seed lost the user's question:\n%s", seed)
	}
}

// And it ignores it when there is nothing left to list. The seed reader used to
// skip content[0] only when the attachment metadata was present, so this send —
// preamble, no metadata — named the session after the manifest's own prose.
func TestTitleSeedIgnoresThePreambleWhenEveryAttachmentExpired(t *testing.T) {
	w, s, _ := attachTestWorkspace(t, "s1")

	_ = w.Prompt(context.Background(), "s1", ctrlproto.PromptParams{
		Text:        "check these filters",
		Attachments: []ctrlproto.AttachmentRef{{ID: "att_gone"}},
	})

	seed := core.BuildTitleSeed([]provider.Message{userMessage(t, s)}, 400)
	if strings.Contains(seed, "no longer on disk") || strings.Contains(seed, "the user attached files") {
		t.Errorf("the title seed is made of the manifest prose:\n%s", seed)
	}
	if !strings.Contains(seed, "check these filters") {
		t.Errorf("the title seed lost the user's question:\n%s", seed)
	}
}

// A staged file is allowed to expire out from under the message that named it —
// that is the whole point of the sweeper owning cleanup rather than the model.
// The send must still go, and the model must be told what it is not getting.
func TestPromptSurvivesAnExpiredAttachment(t *testing.T) {
	w, s, _ := attachTestWorkspace(t, "s1")

	err := w.Prompt(context.Background(), "s1", ctrlproto.PromptParams{
		Text:        "check these filters",
		Attachments: []ctrlproto.AttachmentRef{{ID: "att_gone"}},
	})
	if err != nil {
		t.Fatalf("prompt with an expired attachment failed the send: %v", err)
	}

	turn := userTurn(t, s)
	if !strings.Contains(turn, "check these filters") {
		t.Errorf("the user's text was lost:\n%s", turn)
	}
	if !strings.Contains(turn, "no longer on disk") {
		t.Errorf("the model was not told an attachment expired:\n%s", turn)
	}
}

// An id is the only thing a client sends, so it must not be a way to reach
// another session's files. Same-named store, different session directory.
func TestPromptRefusesAnotherSessionsAttachment(t *testing.T) {
	w, s, store := attachTestWorkspace(t, "s1")
	theirs, err := store.Stage("s2", "secrets.env", strings.NewReader("TOKEN=hunter2"))
	if err != nil {
		t.Fatal(err)
	}

	err = w.Prompt(context.Background(), "s1", ctrlproto.PromptParams{
		Text:        "what is in this",
		Attachments: []ctrlproto.AttachmentRef{{ID: theirs.ID}},
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}

	turn := userTurn(t, s)
	if strings.Contains(turn, theirs.Path) {
		t.Errorf("an id resolved across sessions — the turn names another session's file:\n%s", turn)
	}
	if !strings.Contains(turn, "no longer on disk") {
		t.Errorf("the cross-session id should read as missing:\n%s", turn)
	}
}

// The ordinary prompt is untouched: no attachments, no manifest, no blank line
// grafted onto the front of what the user typed.
func TestPromptWithoutAttachmentsIsUnchanged(t *testing.T) {
	w, s, _ := attachTestWorkspace(t, "s1")

	if err := w.Prompt(context.Background(), "s1", ctrlproto.PromptParams{Text: "just a question"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	if turn := userTurn(t, s); turn != "just a question" {
		t.Errorf("turn = %q, want the user's text verbatim", turn)
	}
}

// A workspace built without a store (test doubles do this, and so would a
// carrier that never staged anything) must report the ids as gone rather than
// dereference nothing.
func TestPromptWithNoStoreDoesNotPanic(t *testing.T) {
	w, s, _ := chatTestWorkspace(t, "s1")
	if w.attachments != nil {
		t.Fatal("this test is meaningless unless the store is nil")
	}

	err := w.Prompt(context.Background(), "s1", ctrlproto.PromptParams{
		Text:        "hello",
		Attachments: []ctrlproto.AttachmentRef{{ID: "att_x"}},
	})
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if turn := userTurn(t, s); !strings.Contains(turn, "hello") {
		t.Errorf("the user's text was lost:\n%s", turn)
	}
}
