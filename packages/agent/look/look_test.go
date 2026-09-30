package look

import (
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestCheckRefusesAShapeOutsideTheSetAndAColorThatIsNotHex(t *testing.T) {
	if p := Check(Mark{Shape: "hexagon", Color: "#3B82F6"}); len(p) != 0 {
		t.Errorf("a valid mark was refused: %v", p)
	}
	for _, m := range []Mark{{}, {Shape: "star"}, {Color: "blue"}, {Color: "#3B82F"}, {Shape: "circle", Color: "#GGGGGG"}} {
		if len(Check(m)) == 0 {
			t.Errorf("%+v passed", m)
		}
	}
	if len(Palette) != 13 {
		t.Errorf("the palette has %d colours, want 13", len(Palette))
	}
	for _, c := range Palette {
		if !ValidColor(c) {
			t.Errorf("palette colour %q is not #RRGGBB", c)
		}
	}
}

func TestResolveFillsTheDefaultsInOrder(t *testing.T) {
	got := Resolve([]Who{
		{ID: "own", Mark: Mark{Shape: "shield", Color: "#112233"}, Persona: "mieli", Source: Source{Accent: "#445566"}},
		{ID: "half", Mark: Mark{Shape: "drop"}, Persona: "other", Source: Source{Accent: "#445566"}},
		{ID: "persona", Persona: "branded", Source: Source{Mark: Mark{Shape: "cloud", Color: "#778899"}, Accent: "#445566"}},
		{ID: "bare", Persona: "plain"},
	})
	if got["own"] != (Mark{Shape: "shield", Color: "#112233"}) {
		t.Errorf("a member's own mark = %+v", got["own"])
	}
	if got["half"].Shape != "drop" || got["half"].Color != "#445566" {
		t.Errorf("a member with a shape takes the accent colour: %+v", got["half"])
	}
	if got["persona"] != (Mark{Shape: "cloud", Color: "#778899"}) {
		t.Errorf("a persona's own mark = %+v", got["persona"])
	}
	if b := got["bare"]; !ValidShape(b.Shape) || !ValidColor(b.Color) {
		t.Errorf("a member with nothing set = %+v", b)
	}
	// The same input resolves the same way on every run.
	again := Resolve([]Who{{ID: "bare", Persona: "plain"}})
	if again["bare"] != got["bare"] {
		t.Errorf("the default is not stable: %+v then %+v", got["bare"], again["bare"])
	}
}

// 🔑 Two members from one persona must still look different.
func TestMembersOfOnePersonaTakeDifferentShapes(t *testing.T) {
	var who []Who
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		who = append(who, Who{ID: id, Persona: "mieli", Source: Source{Accent: "#b5651d", Mark: Mark{Shape: "circle"}}})
	}
	// One member sets a shape, and no default may land on it.
	who[3].Mark.Shape = "tab"
	got := Resolve(who)
	seen := map[string]string{}
	for _, w := range who {
		s := got[w.ID].Shape
		if prev, ok := seen[s]; ok {
			t.Errorf("%s and %s share the shape %s", prev, w.ID, s)
		}
		seen[s] = w.ID
		if got[w.ID].Color != "#b5651d" {
			t.Errorf("%s colour = %s, want the persona accent", w.ID, got[w.ID].Color)
		}
	}
	if got["a"].Shape != "circle" {
		t.Errorf("the first member takes the persona's shape, got %s", got["a"].Shape)
	}
}

// A proposal adds a member at the end of the roster, and the added member
// moves no one's mark, whatever its id.
func TestAnAddedMemberMovesNoOne(t *testing.T) {
	who := []Who{
		{ID: "helm", Persona: "mieli", Source: Source{Mark: Mark{Shape: "cloud"}}},
		{ID: "scout", Persona: "mieli", Source: Source{Mark: Mark{Shape: "cloud"}}},
	}
	before := Resolve(who)
	for _, id := range []string{"atlas", "aaa", "zed"} {
		who = append(who, Who{ID: id, Persona: "mieli", Source: Source{Mark: Mark{Shape: "cloud"}}})
		after := Resolve(who)
		for id, m := range before {
			if after[id] != m {
				t.Errorf("adding %s moved %s from %+v to %+v", who[len(who)-1].ID, id, m, after[id])
			}
		}
		before = after
	}
}

// A persona mark colour that is not #RRGGBB falls through to the accent.
func TestABadPersonaColourFallsThroughToTheAccent(t *testing.T) {
	got := Resolve([]Who{{ID: "a", Source: Source{Mark: Mark{Color: "teal"}, Accent: "#445566"}}})
	if got["a"].Color != "#445566" {
		t.Errorf("colour = %s, want the accent", got["a"].Color)
	}
}

// ⚠️ The web client draws marks from its own copy of the shapes and the
// palette, so a shape added here that it cannot draw would render as a circle
// in silence. This reads the client's file as text and fails when they differ.
func TestTheWebClientMirrorsTheShapesAndThePalette(t *testing.T) {
	raw, err := os.ReadFile("../web/client/src/platform/talkoot/marks.ts")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	list := func(name string) []string {
		start := strings.Index(text, "export const "+name+" = [")
		if start < 0 {
			t.Fatalf("marks.ts has no %s", name)
		}
		end := strings.Index(text[start:], "] as const")
		var out []string
		for _, m := range regexp.MustCompile(`'([^']+)'`).FindAllStringSubmatch(text[start:start+end], -1) {
			out = append(out, m[1])
		}
		return out
	}
	if got := list("MARK_SHAPES"); !slices.Equal(got, Shapes) {
		t.Errorf("MARK_SHAPES = %v, want %v", got, Shapes)
	}
	if got := list("MARK_PALETTE"); !slices.Equal(got, Palette) {
		t.Errorf("MARK_PALETTE = %v, want %v", got, Palette)
	}
}

// A talkoot's own colour wins, and without a valid one its id picks the same
// palette colour on every run.
func TestTeamColor(t *testing.T) {
	d := TeamColor("crew", "")
	if !slices.Contains(Palette, d) || TeamColor("crew", "") != d {
		t.Fatalf("default = %q", d)
	}
	if got := TeamColor("crew", "#123456"); got != "#123456" {
		t.Errorf("own colour = %q", got)
	}
	if got := TeamColor("crew", "teal"); got != d {
		t.Errorf("a bad own colour = %q, want the default %q", got, d)
	}
}
