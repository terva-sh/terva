package dialogs

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// choiceTheme is a theme with a real selection pair, so a test can tell a
// highlighted row from a muted one.
func choiceTheme() tui.Theme {
	return tui.Theme{Muted: 8, Tool: 2, Accent: 4, SelectionFG: 7, SelectionBG: 4}
}

// choiceLines returns the rendered option rows — everything between the
// "choose" hint and the closing frame rule — with their styling intact.
func choiceLines(t *testing.T, d *QuestionDialog, th tui.Theme, width int) []string {
	t.Helper()
	rows := d.Render(th, width)
	start := -1
	for i, r := range rows {
		if strings.Contains(widgets.StripANSIBytes(r), "choose") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("no choose hint in render:\n%s", strings.Join(rows, "\n"))
	}
	var out []string
	for _, r := range rows[start:] {
		if isFrameRuleLine(r) {
			break
		}
		out = append(out, r)
	}
	return out
}

// An option wider than the terminal used to be truncated at the right
// edge, so the tail of the answer — often the part that distinguishes it
// from its neighbour — could not be read at all, and unlike a truncated
// question there was nowhere to go to see the rest. It must wrap, the way
// the question text, the typed answer, and the ext panel already do, and
// the selection highlight must cover every row it folded onto so the fold
// still reads as one choice.
func TestQuestionDialogLongOptionWrapsAndKeepsHighlight(t *testing.T) {
	const width = 40
	th := choiceTheme()
	sel := th.SelectionStyle()
	long := "keep the existing behaviour and only add the new flag when the caller asks for it"

	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Pick one", Options: []string{long, "second"}})
	rows := choiceLines(t, d, th, width)

	// Nothing runs off the right edge.
	for _, r := range rows {
		if w := runewidth.StringWidth(widgets.StripANSIBytes(r)); w > width {
			t.Errorf("option row exceeds width %d (visible=%d): %q", width, w, widgets.StripANSIBytes(r))
		}
	}

	// Rows up to "second" belong to the first option.
	cut := len(rows)
	for i, r := range rows {
		if strings.Contains(widgets.StripANSIBytes(r), "second") {
			cut = i
			break
		}
	}
	first := rows[:cut]
	if len(first) < 2 {
		t.Fatalf("the long option did not wrap: %d row(s)\n%s", len(first), strings.Join(first, "\n"))
	}
	for _, r := range first {
		if !strings.Contains(r, sel) {
			t.Errorf("wrapped row of the selected option lost the highlight: %q", widgets.StripANSIBytes(r))
		}
	}
	if strings.Contains(rows[cut], sel) {
		t.Errorf("the unselected option is highlighted: %q", widgets.StripANSIBytes(rows[cut]))
	}

	// Every word of the answer survived the fold.
	var plain []string
	for _, r := range first {
		plain = append(plain, widgets.StripANSIBytes(r))
	}
	if got := strings.Join(strings.Fields(strings.Join(plain, " ")), " "); got != long {
		t.Fatalf("wrapped option lost text:\n got %q\nwant %q", got, long)
	}
}

// A long answer is not always a sentence. An option that is one unbroken
// token — a path, a URL, an id — has no space to fold on, and the byte
// counting wrap this replaced emitted it whole and let it run off the
// edge; it has to be split mid-token instead.
func TestQuestionDialogUnbreakableOptionStillFits(t *testing.T) {
	const width = 30
	th := choiceTheme()
	long := "/Users/x/" + strings.Repeat("verylongsegment/", 6) + "file.go"

	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Which file?", Options: []string{long, "other"}})
	rows := choiceLines(t, d, th, width)

	for _, r := range rows {
		if w := runewidth.StringWidth(widgets.StripANSIBytes(r)); w > width {
			t.Errorf("unbroken option row exceeds width %d (visible=%d): %q", width, w, widgets.StripANSIBytes(r))
		}
	}
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(strings.TrimSpace(widgets.StripANSIBytes(r)))
	}
	if !strings.Contains(b.String(), long) {
		t.Fatalf("split option lost text; rows were:\n%s", strings.Join(rows, "\n"))
	}
}

// Wrapping makes the list taller, so the row budget has to window on the
// wrapped rows rather than on option indexes: walking down must keep the
// selected option on screen, and the body must still fit MaxRows.
func TestQuestionDialogWrappedOptionsWindowWithTheSelection(t *testing.T) {
	const width, maxRows = 40, 10
	th := choiceTheme()
	sel := th.SelectionStyle()

	opts := make([]string, 12)
	for i := range opts {
		opts[i] = fmt.Sprintf("choice %d — %s", i, strings.Repeat("wordy ", 8))
	}
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Pick one", Options: opts})
	d.MaxRows = maxRows

	for i := range opts {
		if i > 0 {
			d.HandleKey(tui.Key{Kind: tui.KeyDown})
		}
		rendered := d.Render(th, width)
		if body := len(rendered) - 2; body > maxRows {
			t.Fatalf("body is %d rows at option %d, want <= %d:\n%s",
				body, i, maxRows, strings.Join(rendered, "\n"))
		}
		// The selected option's own label is on screen, on a highlighted row.
		want := fmt.Sprintf("choice %d —", i)
		found := false
		for _, r := range choiceLines(t, d, th, width) {
			if strings.Contains(widgets.StripANSIBytes(r), want) && strings.Contains(r, sel) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("selection %q scrolled out of the window:\n%s", want, strings.Join(rendered, "\n"))
		}
	}
}

// One option can be taller than the whole band. The window then shows its
// head rather than scrolling past it to bring its last row into view —
// which would leave the user reading an answer from the middle.
func TestQuestionDialogOptionTallerThanTheBandShowsItsHead(t *testing.T) {
	const width, maxRows = 40, 6
	th := choiceTheme()
	head := "the first words of a very long answer"

	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{
		Question: "Pick one",
		Options:  []string{head + " " + strings.Repeat("and more text ", 20), "short"},
	})
	d.MaxRows = maxRows

	rendered := d.Render(th, width)
	if body := len(rendered) - 2; body > maxRows {
		t.Fatalf("body is %d rows, want <= %d:\n%s", body, maxRows, strings.Join(rendered, "\n"))
	}
	first := widgets.StripANSIBytes(choiceLines(t, d, th, width)[0])
	if !strings.HasPrefix(strings.TrimSpace(first), "the first words") {
		t.Fatalf("window opened mid-answer; first option row is %q", first)
	}
}
