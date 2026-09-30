// Package look holds a Talkoot member's mark: one flat shape in one colour,
// drawn with two eye strokes wherever the member appears. A roster member and
// a persona can both carry one, so the definition sits below both packages.
//
// The mark is a look-class field (decision 0025): it changes what a person
// sees, and grants nothing.
package look

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"slices"
	"strings"
)

// Mark is a shape and a colour. Either may be empty, and the empty part then
// comes from the default (Resolve).
type Mark struct {
	Shape string `yaml:"shape,omitempty" json:"shape,omitempty"`
	Color string `yaml:"color,omitempty" json:"color,omitempty"`
}

// Shapes is the closed set of shapes. Every surface can draw every one, so a
// new shape is a change to terva and to each surface, not to a roster.
var Shapes = []string{
	"circle", "blob", "rounded-square", "pill", "triangle",
	"hexagon", "cloud", "drop", "tab", "shield", "gem",
}

// Palette is the picker's colours. Each reads on the light and the dark
// theme. A mark may hold any #RRGGBB value, so the palette is a suggestion.
var Palette = []string{
	"#E5484D", "#F76B15", "#FFB224", "#A18072", "#46A758",
	"#12A594", "#00A2C7", "#3E63DD", "#6E56CF", "#AB4ABA",
	"#D6409F", "#8D8D8D", "#5B8C3A",
}

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// ValidColor reports whether c is a #RRGGBB value, the format a persona's
// accent_color uses.
func ValidColor(c string) bool { return colorRe.MatchString(c) }

// ValidShape reports whether s is in Shapes.
func ValidShape(s string) bool { return slices.Contains(Shapes, s) }

// Check lists what is wrong with a mark. A mark with neither part set says
// nothing, and is refused so a stray `mark:` does not pass for a choice.
func Check(m Mark) []string {
	var out []string
	if m.Shape == "" && m.Color == "" {
		out = append(out, "mark sets neither shape nor color")
	}
	if m.Shape != "" && !ValidShape(m.Shape) {
		out = append(out, fmt.Sprintf("mark shape %q is not one of %s", m.Shape, strings.Join(Shapes, ", ")))
	}
	if m.Color != "" && !ValidColor(m.Color) {
		out = append(out, fmt.Sprintf("mark color %q is not a #RRGGBB value", m.Color))
	}
	return out
}

// Source is what a member's default mark comes from: its persona's own mark
// and accent colour. Both may be empty.
type Source struct {
	Mark   Mark
	Accent string
}

// Who is one member for Resolve: its id, the mark its roster entry sets, and
// the persona its default comes from.
type Who struct {
	ID      string
	Mark    Mark
	Persona string
	Source  Source
}

// Resolve gives every member a whole mark. A part the member sets wins, then
// the persona's mark, then the persona's accent colour for the colour, and a
// shape and colour picked from the member id. Persona is the key that groups
// members, so two references to one persona must use one key.
//
// 🔑 Two members built from one persona would take one look from it. So a
// shape the member did not set moves on to the next free shape among the
// members of its persona, and two such members differ while the set has
// shapes left. Members take shapes in roster order, because a proposal adds
// a member at the end: an added member with no shape of its own then moves no
// one's mark. A shape a member sets takes its place first, so a set shape can
// move another member's default, and a reorder or a removal can move one. A
// set mark never moves.
func Resolve(members []Who) map[string]Mark {
	out := make(map[string]Mark, len(members))
	taken := map[string]map[string]bool{} // persona -> shapes its members hold
	// Shapes a member set take their place first, so a default never lands
	// on one.
	for _, w := range members {
		if w.Mark.Shape != "" {
			mark(taken, w.Persona)[w.Mark.Shape] = true
		}
	}
	for _, w := range members {
		m := w.Mark
		// Each step skips a colour that is not #RRGGBB, so a bad persona mark
		// colour falls through to a good accent.
		for _, c := range []string{w.Source.Mark.Color, w.Source.Accent, Palette[pick(w.ID, len(Palette))]} {
			if ValidColor(m.Color) {
				break
			}
			m.Color = c
		}
		if m.Shape == "" {
			seen := mark(taken, w.Persona)
			start := pick(w.ID, len(Shapes))
			if s := w.Source.Mark.Shape; ValidShape(s) && !seen[s] {
				m.Shape = s
			} else {
				m.Shape = Shapes[start]
				for i := range Shapes {
					if s := Shapes[(start+i)%len(Shapes)]; !seen[s] {
						m.Shape = s
						break
					}
				}
			}
			seen[m.Shape] = true
		}
		out[w.ID] = m
	}
	return out
}

func mark(taken map[string]map[string]bool, persona string) map[string]bool {
	if taken[persona] == nil {
		taken[persona] = map[string]bool{}
	}
	return taken[persona]
}

// TeamColor is a talkoot's colour: its own when it sets a #RRGGBB value, and
// otherwise a palette colour that its id picks, as a member's default does.
func TeamColor(id, own string) string {
	if ValidColor(own) {
		return own
	}
	return Palette[pick(id, len(Palette))]
}

// pick maps an id to an index, the same on every machine and every run.
func pick(id string, n int) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	return int(h.Sum32() % uint32(n))
}
