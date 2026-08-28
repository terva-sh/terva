package build

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/lore"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

func guardTestAgent(t *testing.T, msg string) *core.Agent {
	t.Helper()
	ag := &core.Agent{}
	ag.SetMessages([]provider.Message{
		{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: msg}}},
	})
	return ag
}

// Lore rides the tail as a user-role message, the position that won 20 of 20
// final answers away from the user on the inactive-groups note. So the frame
// leads with the prohibition, and the ORDER is the measured part.
//
// The guard must also appear exactly ONCE. The lore frame and the extension
// section render the same key, and a tail carrying both should not say the same
// sentence to the model twice in a row.
func TestLoreFrameLeadsWithTheBackgroundGuard(t *testing.T) {
	r := &Resolved{
		loreTriggered: []lore.Entry{{
			Name:    "vault",
			Keys:    []string{"vault"},
			Content: "the vault is sealed",
			Source:  "vault.md",
		}},
		loreConfig: lore.Config{},
		loreFired:  &LoreFiredRecord{},
	}
	out := r.PerTurnContext(guardTestAgent(t, "tell me about the vault"))()
	if !strings.Contains(out, "the vault is sealed") {
		t.Fatalf("precondition: the lore entry did not fire:\n%s", out)
	}
	if !strings.HasPrefix(out, "[background] Do not reply") {
		t.Errorf("the lore frame does not LEAD with the prohibition:\n%s", out)
	}
	if n := strings.Count(out, "[background]"); n != 1 {
		t.Errorf("want exactly 1 background guard, got %d:\n%s", n, out)
	}
	if strings.Index(out, "[background]") > strings.Index(out, "the vault is sealed") {
		t.Errorf("the content precedes the prohibition; prohibition-first is the measured ordering:\n%s", out)
	}
}

// The expensive regression, pinned. A character card's post-history
// instructions are the strongest steering the format has: authored text whose
// whole purpose is to shape the very next reply. The background guard says the
// block is "not a request to act on", which is true of lore and an extension
// card and catastrophically false here.
//
// TailHost concatenates six different things, and only the reference ones are
// framed. A guard hoisted to the head of the whole block — the obvious
// simplification, and what an earlier draft of the proposal called for — would
// switch off PHI and the author's note without failing anything else.
//
// A tail carrying ONLY steering must therefore carry no guard at all.
func TestPostHistoryInstructionsAreNeverGuarded(t *testing.T) {
	r := &Resolved{
		postHistory: "Stay terse. Never break character.",
		loreFired:   &LoreFiredRecord{},
	}
	out := r.PerTurnContext(guardTestAgent(t, "hello"))()
	if !strings.Contains(out, "Stay terse") {
		t.Fatalf("precondition: post-history instructions missing from the tail:\n%s", out)
	}
	if strings.Contains(out, "[background]") {
		t.Errorf("the background guard reached post-history instructions; it tells the model they are "+
			"'not a request to act on', which switches off the card's strongest steering:\n%s", out)
	}
}

// Same rule for the author's note, which is live steering the user edits
// mid-session to change tone or pacing. It rides LAST in the host tail, the
// most recency-weighted slot there is, precisely because it is meant to land.
func TestTheAuthorsNoteIsNeverGuarded(t *testing.T) {
	note := &NoteRecord{}
	note.Set("Keep the pace up; end on a hook.")
	r := &Resolved{note: note, loreFired: &LoreFiredRecord{}}
	out := r.PerTurnContext(guardTestAgent(t, "hello"))()
	if !strings.Contains(out, "end on a hook") {
		t.Fatalf("precondition: the author's note is missing from the tail:\n%s", out)
	}
	if strings.Contains(out, "[background]") {
		t.Errorf("the background guard reached the author's note, which exists to steer the reply:\n%s", out)
	}
}
