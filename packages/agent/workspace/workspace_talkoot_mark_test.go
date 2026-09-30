package workspace

import (
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/look"
	"terva.sh/terva/packages/agent/persona"
	"terva.sh/terva/packages/agent/talkoot"
)

// Every member reaches the view with a whole mark. Two members of the default
// persona still differ, and a member's own mark wins and is reported apart,
// so the card can reset it.
func TestTheViewCarriesEveryMembersMark(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	v, err := w.Talkoot(ctx, ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	marks := map[string]ctrlproto.TalkootMember{}
	for _, m := range v.Members {
		if !look.ValidShape(m.Mark.Shape) || !look.ValidColor(m.Mark.Color) || m.OwnMark != nil {
			t.Errorf("%s: mark %+v, own %+v", m.ID, m.Mark, m.OwnMark)
		}
		marks[m.ID] = m
	}
	if marks["helm"].Mark.Shape == marks["jev"].Mark.Shape {
		t.Errorf("helm and jev share the shape %s, though both take the default persona", marks["helm"].Mark.Shape)
	}

	text := "---\nname: crew\nhome: " + cwd + "\nbudget_usd_per_day: 5\nmembers:\n  - id: helm\n    role: coordinator\n  - id: jev\n    role: specialist\n    mark:\n      shape: shield\n      color: \"#46A758\"\n---\n"
	v, err = w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Text: text})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range v.Members {
		if m.ID != "jev" {
			continue
		}
		want := ctrlproto.TalkootMark{Shape: "shield", Color: "#46A758"}
		if m.Mark != want || m.OwnMark == nil || *m.OwnMark != want {
			t.Errorf("jev: mark %+v, own %+v, want both %+v", m.Mark, m.OwnMark, want)
		}
	}
}

// A member with no persona and a member that names the default run one
// persona, so they take different shapes.
func TestMembersThatNameTheDefaultPersonaShareItsGroup(t *testing.T) {
	def, err := persona.Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	var members []talkoot.Member
	// Five of each fill the ten shapes, so any shared shape is a group split.
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		members = append(members, talkoot.Member{ID: id}, talkoot.Member{ID: id + "-named", Persona: def.Name})
	}
	seen := map[string]string{}
	for id, m := range talkootMarks(members) {
		if prev, ok := seen[m.Shape]; ok {
			t.Errorf("%s and %s share the shape %s", prev, id, m.Shape)
		}
		seen[m.Shape] = id
	}
}

// A proposal that resets a mark says what the member will look like, which
// the entry alone cannot: a reset leaves no mark in it.
func TestAProposalCarriesTheMarksBeforeAndAfter(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	own := map[string]any{"shape": "shield", "color": "#46A758"}
	if _, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Ops: []ctrlproto.TalkootOp{{Op: "look", Member: "jev", Set: map[string]any{"mark": own}}}}); err != nil {
		t.Fatal(err)
	}
	p, err := w.ProposeTalkoot(ctx, ctrlproto.TalkootProposeParams{ID: "crew", By: "sothr", Ops: []ctrlproto.TalkootOp{{Op: "look", Member: "jev", Set: map[string]any{"mark": nil}}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Changes) != 1 {
		t.Fatalf("changes = %+v", p.Changes)
	}
	ch := p.Changes[0]
	if ch.After == nil || ch.After.Mark != nil {
		t.Fatalf("the reset should leave no mark in the entry: %+v", ch.After)
	}
	if ch.MarkBefore == nil || *ch.MarkBefore != (ctrlproto.TalkootMark{Shape: "shield", Color: "#46A758"}) {
		t.Errorf("mark before = %+v, want jev's own", ch.MarkBefore)
	}
	if ch.MarkAfter == nil || !look.ValidShape(ch.MarkAfter.Shape) || *ch.MarkAfter == *ch.MarkBefore {
		t.Errorf("mark after = %+v, want the default jev resets to", ch.MarkAfter)
	}
}
