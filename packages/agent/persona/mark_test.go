package persona

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/look"
)

// A persona may ship a mark, so an extension's persona brings its own look.
// Check holds it to the same closed set and colour format as a roster member.
func TestAPersonaMarkIsReadAndChecked(t *testing.T) {
	good := "---\nname: branded\nmark:\n  shape: cloud\n  color: \"#12A594\"\n---\nYou help.\n"
	p, problems := Check([]byte(good), "branded.md")
	if len(problems) != 0 {
		t.Fatalf("a valid mark was refused: %v", problems)
	}
	if p.Mark != (look.Mark{Shape: "cloud", Color: "#12A594"}) {
		t.Errorf("mark = %+v", p.Mark)
	}
	bad := "---\nname: branded\nmark:\n  shape: star\n  color: teal\n---\nYou help.\n"
	_, problems = Check([]byte(bad), "branded.md")
	joined := strings.Join(problems, "; ")
	if !strings.Contains(joined, `mark shape "star"`) || !strings.Contains(joined, `mark color "teal"`) {
		t.Errorf("problems = %v, want the shape and the colour refused", problems)
	}
}

// An empty mark says nothing, and is refused as a roster refuses it. A bare
// `mark:` is null, and means no mark.
func TestAPersonaWithAnEmptyMarkIsRefused(t *testing.T) {
	for _, mark := range []string{"mark: {}", "mark:\n  shape: \"  \""} {
		_, problems := Check([]byte("---\nname: branded\n"+mark+"\n---\nYou help.\n"), "branded.md")
		if !strings.Contains(strings.Join(problems, "; "), "mark sets neither shape nor color") {
			t.Errorf("%q: problems = %v, want the empty mark refused", mark, problems)
		}
	}
	if _, problems := Check([]byte("---\nname: branded\nmark:\n---\nYou help.\n"), "branded.md"); len(problems) != 0 {
		t.Errorf("a null mark was refused: %v", problems)
	}
}
