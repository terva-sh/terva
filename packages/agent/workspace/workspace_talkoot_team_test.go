package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/look"
	"terva.sh/terva/packages/agent/talkoot"
)

func strp(s string) *string { return &s }

func listed(t *testing.T, w *Workspace, id string) ctrlproto.TalkootSummary {
	t.Helper()
	list, err := w.Talkoots(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("%s is not listed", id)
	return ctrlproto.TalkootSummary{}
}

// A person sets the team colour and returns it to the default. The roster
// file, the view, the list, and a roster line in the room all follow.
func TestAPersonSetsTheTeamColour(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	v, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))})
	if err != nil {
		t.Fatal(err)
	}
	def := look.TeamColor("crew", "")
	if v.Color != def || v.OwnColor != "" {
		t.Fatalf("a new team's colour = %q (own %q), want the default %q", v.Color, v.OwnColor, def)
	}
	if s := listed(t, w, "crew"); s.Color != def || s.Name != "crew" {
		t.Fatalf("listed = %+v", s)
	}
	v, err = w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Color: strp("#12A594")})
	if err != nil {
		t.Fatal(err)
	}
	if v.Color != "#12A594" || v.OwnColor != "#12A594" {
		t.Fatalf("after the change = %q (own %q)", v.Color, v.OwnColor)
	}
	text, err := os.ReadFile(filepath.Join(talkoot.Dir(), "crew", talkoot.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "color: '#12A594'") && !strings.Contains(string(text), `color: "#12A594"`) {
		t.Fatalf("the roster file holds no colour:\n%s", text)
	}
	if s := listed(t, w, "crew"); s.Color != "#12A594" || s.OwnColor != "#12A594" {
		t.Fatalf("the list shows %q (own %q)", s.Color, s.OwnColor)
	}
	page, err := w.TalkootRoom(ctx, ctrlproto.TalkootRoomParams{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	last := page.Lines[len(page.Lines)-1]
	if last.Type != talkoot.LineRoster || last.By != "human:sothr" || last.ColorBefore != def || last.ColorAfter != "#12A594" {
		t.Fatalf("the roster line = %+v", last)
	}

	v, err = w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Color: strp("")})
	if err != nil {
		t.Fatal(err)
	}
	if v.Color != def || v.OwnColor != "" {
		t.Fatalf("after a reset = %q (own %q)", v.Color, v.OwnColor)
	}
	if s := listed(t, w, "crew"); s.Color != def || s.OwnColor != "" {
		t.Fatalf("after a reset the list shows %q (own %q)", s.Color, s.OwnColor)
	}
}

// A colour update refuses a bad colour, a colour the team shows already,
// whether as its default or its own, and a second form in the same request.
// Each refusal names its reason, so a refusal for another reason fails.
func TestATeamColourUpdateRefusesWhatItCannotApply(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	if _, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))}); err != nil {
		t.Fatal(err)
	}
	def := look.TeamColor("crew", "")
	for name, c := range map[string]struct {
		p    ctrlproto.TalkootUpdateParams
		want string
	}{
		"a bad colour":         {ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Color: strp("teal")}, "is not a #RRGGBB value"},
		"the reset it has":     {ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Color: strp("")}, "is already " + def},
		"the default it shows": {ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Color: strp(def)}, "is already " + def},
		"a colour and ops": {ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Color: strp("#12A594"),
			Ops: []ctrlproto.TalkootOp{{Op: "look", Member: "jev", Set: map[string]any{"title": "Dev"}}}}, "holds one of"},
		"a colour, no person": {ctrlproto.TalkootUpdateParams{ID: "crew", By: "", Color: strp("#12A594")}, "must name a person"},
	} {
		_, err := w.UpdateTalkoot(ctx, c.p)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one that says %q", name, err, c.want)
		}
	}
	if _, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Color: strp("#12a594")}); err != nil {
		t.Fatal(err)
	}
	// The team's own colour in another hex case is the same colour.
	if _, err := w.UpdateTalkoot(ctx, ctrlproto.TalkootUpdateParams{ID: "crew", By: "sothr", Color: strp("#12A594")}); err == nil || !strings.Contains(err.Error(), "is already #12a594") {
		t.Errorf("the own colour again: err = %v", err)
	}
}

// A failed turn stops helm and needs a person, and the team state says so on
// the status event, in the view, and in the list. The change also tells the
// clients on #workspace that the list changed.
func TestTheTeamStateFollowsItsMembers(t *testing.T) {
	cwd := talkootHome(t)
	w := openTalkootWorkspace(t, cwd)
	ctx := t.Context()
	v, err := w.CreateTalkoot(ctx, ctrlproto.TalkootCreateParams{ID: "crew", Text: string(crewText(cwd))})
	if err != nil {
		t.Fatal(err)
	}
	if v.State != talkoot.TeamOnline {
		t.Fatalf("a new team is %q", v.State)
	}
	ws, err := w.Subscribe(ctx, ctrlproto.AddrWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	room, err := w.Subscribe(ctx, ctrlproto.TalkootAddr("crew"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.PostTalkoot(ctx, ctrlproto.TalkootPostParams{ID: "crew", By: "sothr", To: []string{"helm"}, Body: "start"}); err != nil {
		t.Fatal(err)
	}
	// Each move of the team state sends one notice. The team can pass through
	// busy on its way, so the test counts the moves and waits for a notice
	// per move, the last of them the move to needs-you.
	moves, prev := 0, v.State
	for prev != talkoot.TeamNeedsYou {
		ev := nextEvent(t, room, ctrlproto.EventTalkootStatus)
		if ev.Talkoot.State != prev {
			moves, prev = moves+1, ev.Talkoot.State
		}
	}
	for range moves {
		nextEvent(t, ws, ctrlproto.EventTalkootsChanged)
	}
	if s := listed(t, w, "crew"); s.State != talkoot.TeamNeedsYou {
		t.Fatalf("the list shows %q", s.State)
	}
	v, err = w.Talkoot(ctx, ctrlproto.TalkootRef{ID: "crew"})
	if err != nil {
		t.Fatal(err)
	}
	if v.State != talkoot.TeamNeedsYou {
		t.Fatalf("the view shows %q", v.State)
	}
}
