package dialogs

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// stripRow is the tab strip: the first body row of a question set.
func stripRow(t *testing.T, d *QuestionDialog, width int) string {
	t.Helper()
	rows := d.Render(tui.Theme{}, width)
	if len(rows) < 2 {
		t.Fatalf("nothing rendered: %q", rows)
	}
	return widgets.StripANSIBytes(rows[1])
}

func slugSet(d *QuestionDialog) chan []core.UserAnswer {
	return askN(d,
		core.UserQuestion{Question: "which model?", Slug: "model pick", Options: []string{"a"}},
		core.UserQuestion{Question: "which database?", Slug: "db choice", Options: []string{"b"}},
		core.UserQuestion{Question: "roll out how?", Slug: "rollout", Options: []string{"c"}})
}

// A numbered chip says where you are and nothing about what is behind it.
// Given room, every chip carries its name.
func TestQuestionStripNamesEveryChipWhenTheyFit(t *testing.T) {
	d := NewQuestionDialog()
	_ = slugSet(d)
	got := stripRow(t, d, 100)
	for _, want := range []string{"model pick", "db choice", "rollout"} {
		if !strings.Contains(got, want) {
			t.Fatalf("wide strip dropped %q: %q", want, got)
		}
	}
}

// Under width pressure the active chip keeps its name and the rest fall
// back to numbers — a name clipped to a few columns tells you less than
// the position does, which is why this was numbers-only before.
func TestQuestionStripNamesOnlyTheActiveChipWhenNarrow(t *testing.T) {
	d := NewQuestionDialog()
	_ = slugSet(d)
	d.HandleKey(tui.Key{Kind: tui.KeyTab}) // onto question 2

	// 40 columns: all three names need 52 and are refused, the active one
	// needs 33 and fits.
	got := stripRow(t, d, 40)
	if !strings.Contains(got, "db choice") {
		t.Fatalf("the active chip lost its name: %q", got)
	}
	if strings.Contains(got, "model pick") || strings.Contains(got, "rollout") {
		t.Fatalf("a tier that does not fit was used anyway: %q", got)
	}
}

// The strip is a rendered row like any other and must stay in the frame,
// at every tier — including the one where even numbers overflow.
func TestQuestionStripNeverOverflowsTheFrame(t *testing.T) {
	d := NewQuestionDialog()
	_ = slugSet(d)
	for _, width := range []int{20, 26, 34, 50, 80, 120} {
		for tab := range 4 {
			d.mu.Lock()
			d.tab = tab
			d.mu.Unlock()
			got := stripRow(t, d, width)
			if w := runewidth.StringWidth(got); w > width {
				t.Errorf("strip at width %d is %d columns: %q", width, w, got)
			}
		}
	}
}

// A question the model did not name stays a number in every tier. The
// feature is optional per question, and one named chip beside two
// numbered ones still says more than three numbers.
func TestQuestionStripMixesNamedAndUnnamed(t *testing.T) {
	d := NewQuestionDialog()
	_ = askN(d,
		core.UserQuestion{Question: "which model?", Slug: "model pick", Options: []string{"a"}},
		core.UserQuestion{Question: "and this one?", Options: []string{"b"}})
	got := stripRow(t, d, 100)
	if !strings.Contains(got, "1 model pick") {
		t.Fatalf("named chip lost its name: %q", got)
	}
	if !strings.Contains(got, "2") {
		t.Fatalf("unnamed chip lost its number: %q", got)
	}
}

// A slug is a chip label, not a second question. A model that sends a
// sentence gets a number, because half a sentence in a tab strip is worse
// than the position it replaced.
func TestSanitizeSlugBoundsWhatAChipCanHold(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"db choice", "db choice"},
		{"  spaced   out  ", "spaced out"},
		{"one two three", "one two three"},
		{"one two three four", ""},
		{"averyveryverylongsingleword-that-keeps-going", ""},
		{"line\nbreak", "line break"},
		{"", ""},
		{"   ", ""},
	} {
		if got := core.SanitizeSlug(tc.in); got != tc.want {
			t.Errorf("SanitizeSlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The dialog sanitizes on the way out too: the ctrlproto path builds
// UserQuestions straight off the wire, so a peer that skipped the tool's
// validation cannot push an unbounded slug into the strip.
func TestQuestionStripSanitizesAnUnboundedSlug(t *testing.T) {
	d := NewQuestionDialog()
	_ = askN(d,
		core.UserQuestion{Question: "one?", Slug: strings.Repeat("very long slug ", 5), Options: []string{"a"}},
		core.UserQuestion{Question: "two?", Options: []string{"b"}})
	got := stripRow(t, d, 100)
	if strings.Contains(got, "very long slug") {
		t.Fatalf("an unbounded slug reached the strip: %q", got)
	}
}
