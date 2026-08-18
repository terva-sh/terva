package dialogs

import (
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

func jdUser(text string) provider.Message {
	return provider.Message{Role: provider.RoleUser, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

func jdAsst(text string) provider.Message {
	return provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: text}}}
}

// jdMachineAuthored is a transcript with one of each machine-authored RoleUser
// message interleaved with three the user actually typed, at known indices.
//
//	0 /clear divider          6 compaction summary
//	1 real-1                  7 host nudge
//	2 assistant               8 real-3
//	3 tool-image mirror
//	4 real-2
//	5 assistant
func jdMachineAuthored() []provider.Message {
	return []provider.Message{
		{Role: provider.RoleUser, Meta: map[string]string{core.MetaClear: "true"}},
		jdUser("real-1"),
		jdAsst("a1"),
		{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: core.ToolImageMirrorPrefix}},
			Meta:    map[string]string{"tool_image_mirror": "true"},
		},
		jdUser("real-2"),
		jdAsst("a2"),
		{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: "Conversation so far: …"}},
			Meta:    map[string]string{core.MetaCompaction: "true"},
		},
		{
			Role:    provider.RoleUser,
			Content: []provider.Content{provider.TextBlock{Text: "Please continue."}},
			Meta:    map[string]string{core.MetaSynthetic: "true"},
		},
		jdUser("real-3"),
	}
}

// TestThePickerOffersOnlyTurnsTheUserTook: four of the nine messages carry
// RoleUser without the user having said anything. The picker offered all four —
// a mirror previewing as the wire prefix, a /clear divider as "(empty)", a
// compaction summary as its own first line, a nudge as itself — and numbered
// them, so a transcript with any of them misnumbered every real turn after it.
func TestThePickerOffersOnlyTurnsTheUserTook(t *testing.T) {
	got := buildJumpTargets(jdMachineAuthored())
	if len(got) != 3 {
		var previews []string
		for _, g := range got {
			previews = append(previews, g.Preview)
		}
		t.Fatalf("picker offers %d rows, want the 3 turns the user took: %q", len(got), previews)
	}
	want := []struct {
		turn    int
		msgIdx  int
		preview string
	}{
		{1, 1, "real-1"},
		{2, 4, "real-2"},
		{3, 8, "real-3"},
	}
	for i, w := range want {
		if got[i].TurnNo != w.turn || got[i].Preview != w.preview {
			t.Errorf("row %d = turn %d %q, want turn %d %q", i, got[i].TurnNo, got[i].Preview, w.turn, w.preview)
		}
		// The index half matters as much as the row half: a fork resolves
		// MessageIdx against the slice Open was given, so a picker that
		// RENUMBERED rather than skipped would cut the branch in the wrong place.
		if got[i].MessageIdx != w.msgIdx {
			t.Errorf("row %d has MessageIdx %d, want %d — skipping a row must not shift the indices, "+
				"they still address the slice Open was handed", i, got[i].MessageIdx, w.msgIdx)
		}
	}
}

// TestTheSelectionCarriesThePurposeItWasOpenedWith: the domain has to come back
// out with the pick. It used to be held beside the dialog in a bool that one
// path set and four cleared, with nothing checking the two agreed.
func TestTheSelectionCarriesThePurposeItWasOpenedWith(t *testing.T) {
	for _, p := range []JumpPurpose{JumpScroll, JumpFork} {
		d := NewJumpDialog()
		d.Open(jdMachineAuthored(), "", p)
		act := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
		if !act.Select {
			t.Fatalf("purpose %v: enter did not select", p)
		}
		if act.Purpose != p {
			t.Errorf("opened with purpose %v, selection came back with %v", p, act.Purpose)
		}
	}
}

// TestReopeningForAnotherPurposeForgetsTheOldOne: the bool this replaces was
// cleared on four exit paths, and openJumpDialog was not one of them. Binding
// the purpose to Open makes forgetting it impossible rather than merely unlikely.
func TestReopeningForAnotherPurposeForgetsTheOldOne(t *testing.T) {
	d := NewJumpDialog()
	d.Open(jdMachineAuthored(), "", JumpFork)
	d.Open(jdMachineAuthored(), "", JumpScroll)
	act := d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if act.Purpose != JumpScroll {
		t.Errorf("re-opened for %v, selection came back with %v — the old purpose survived", JumpScroll, act.Purpose)
	}
}
