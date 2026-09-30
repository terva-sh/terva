package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/talkoot"
)

// A person's field edit applies at once, writes a roster line in their name,
// and keeps what another edit changed meanwhile.
func TestAPersonsFieldEditAppliesAtOnce(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	// An edit to another field lands first, as a second card could.
	if _, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Ops: []ctrlproto.TalkootOp{
		{Op: "edit", Member: "helm", Set: map[string]any{"title": "Helm"}},
	}}); err != nil {
		t.Fatal(err)
	}
	v, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Ops: []ctrlproto.TalkootOp{
		{Op: "edit", Member: "helm", Set: map[string]any{"posture": "ask", "mark": map[string]any{"shape": "tab", "color": "#46A758"}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var helm ctrlproto.TalkootMember
	for _, m := range v.Members {
		if m.ID == "helm" {
			helm = m
		}
	}
	if helm.Title != "Helm" || helm.Posture != "ask" || helm.OwnMark == nil || helm.OwnMark.Shape != "tab" {
		t.Errorf("helm is %+v, want the title kept and the posture and mark set", helm)
	}
	lines := roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineRoster })
	if len(lines) != 2 || lines[1].By != "human:sothr" {
		t.Errorf("want a roster line by the person for each edit, got %+v", lines)
	}

	// A null resets the mark to the default.
	if _, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Ops: []ctrlproto.TalkootOp{
		{Op: "look", Member: "helm", Set: map[string]any{"mark": nil}},
	}}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(talkoot.Dir(), "crew", talkoot.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "mark:") || !strings.Contains(string(raw), "posture: ask") {
		t.Errorf("the reset left:\n%s", raw)
	}
}

func TestAnEditIsRefusedWhenItBreaksARuleOrChangesNothing(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	if _, err := w.talkootCreate(ctx, "crew", crewText(cwd)); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]ctrlproto.TalkootUpdateParams{
		"text and ops":  {ID: "crew", By: "sothr", Text: string(crewText(cwd)), Ops: []ctrlproto.TalkootOp{{Op: "remove", Member: "jev"}}},
		"neither":       {ID: "crew", By: "sothr"},
		"a bad posture": {ID: "crew", By: "sothr", Ops: []ctrlproto.TalkootOp{{Op: "edit", Member: "jev", Set: map[string]any{"posture": "sometimes"}}}},
		"no change":     {ID: "crew", By: "sothr", Ops: []ctrlproto.TalkootOp{{Op: "edit", Member: "jev", Set: map[string]any{"role": "specialist"}}}},
		"not a person":  {ID: "crew", By: "a b", Ops: []ctrlproto.TalkootOp{{Op: "edit", Member: "jev", Set: map[string]any{"title": "Jev"}}}},
		// The card sends a number that does not parse as its text.
		"a number as text": {ID: "crew", By: "sothr", Ops: []ctrlproto.TalkootOp{{Op: "edit", Member: "jev", Set: map[string]any{"turns_per_day": "lots"}}}},
		"a missing name":   {ID: "crew", By: "sothr", Ops: []ctrlproto.TalkootOp{{Op: "edit", Member: "nobody", Set: map[string]any{"title": "X"}}}},
	} {
		if _, err := w.UpdateTalkoot(ctx, p); err == nil {
			t.Errorf("%s: the edit was accepted", name)
		}
	}
	if n := len(roomLines(t, "crew", func(l talkoot.Line) bool { return l.Type == talkoot.LineRoster })); n != 0 {
		t.Errorf("a refused edit wrote %d roster lines", n)
	}
}
