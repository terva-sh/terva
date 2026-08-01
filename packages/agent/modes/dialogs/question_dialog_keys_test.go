package dialogs

import (
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// legendRow is the key legend: the last body row before the frame rule.
func legendRow(t *testing.T, d *QuestionDialog, width int) string {
	t.Helper()
	rows := d.Render(tui.Theme{}, width)
	if len(rows) < 2 {
		t.Fatalf("no body to read a legend from: %q", rows)
	}
	return strings.TrimSpace(widgets.StripANSIBytes(rows[len(rows)-2]))
}

// Tab and shift+tab were the only way through a question set and the only
// keys nothing on screen mentioned — the strip showed WHERE you were
// without saying how to move. A key that exists and is never named is a
// key nobody presses.
func TestQuestionSetLegendNamesTabMovement(t *testing.T) {
	d := NewQuestionDialog()
	_ = askN(d,
		core.UserQuestion{Question: "one?", Options: []string{"a"}},
		core.UserQuestion{Question: "two?", Options: []string{"b"}})
	if got := legendRow(t, d, 90); !strings.Contains(got, "tab or alt+1-2 question") {
		t.Fatalf("a set does not advertise how to move between questions: %q", got)
	}

	// A single question has nowhere to tab to, and offering the key there
	// would be worse than silence — it would not do anything.
	single := NewQuestionDialog()
	_ = ask1(single, core.UserQuestion{Question: "one?", Options: []string{"a"}})
	if got := legendRow(t, single, 90); strings.Contains(got, "tab") {
		t.Fatalf("a single question offers a key that does nothing: %q", got)
	}
}

// ctrl+u already cleared the field — the editor has owned it all along —
// but nothing said so, which is the same as not having it.
func TestQuestionDialogLegendNamesClearWhileTyping(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Name?"})
	for _, r := range "wrong answer" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	if got := legendRow(t, d, 90); !strings.Contains(got, "ctrl+u clears") {
		t.Fatalf("the answer field does not advertise the clear key: %q", got)
	}
	// And it does what the legend says.
	d.HandleKey(tui.Key{Kind: tui.KeyCtrlU})
	for _, r := range "right" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, resp); ans.Answer != "right" {
		t.Fatalf("ctrl+u did not clear the field: %+v", ans)
	}
}

// The legend is mode-specific: a key that does nothing where you are
// standing is worse than one you were never told about.
func TestQuestionDialogLegendFollowsTheMode(t *testing.T) {
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{
		Question: "Pick", Options: []string{"a", "b"}, AllowCustom: true,
	})
	if got := legendRow(t, d, 90); !strings.Contains(got, "↑/↓ or 1-3 pick") {
		t.Fatalf("choice mode does not advertise picking: %q", got)
	}
	if got := legendRow(t, d, 90); strings.Contains(got, "ctrl+u") {
		t.Fatalf("choice mode offers a text-editing key: %q", got)
	}

	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // into the answer field
	if got := legendRow(t, d, 90); strings.Contains(got, "↑/↓") {
		t.Fatalf("the answer field offers list keys the editor has taken: %q", got)
	}
}

// Arrowing to option seven of eight is a walk. Digits go straight there,
// the way the confirm gate's 1-N already do.
func TestQuestionDialogDigitJumpsToAnOption(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{
		Question: "Pick", Options: []string{"alpha", "beta", "gamma"},
	})
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '3'})
	// A jump, not a jump-and-answer: a mistyped digit costs a cursor move,
	// not an answer the agent acts on.
	if !d.Active() {
		t.Fatal("a digit answered the question outright; it should only move the cursor")
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, resp); ans.Answer != "gamma" {
		t.Fatalf("want gamma, got %+v", ans)
	}
}

// A digit past the end of the list must not move the cursor somewhere
// there is no option, and must not be swallowed into an answer.
func TestQuestionDialogDigitPastTheListDoesNothing(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Pick", Options: []string{"alpha", "beta"}})
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '9'})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, resp); ans.Answer != "alpha" {
		t.Fatalf("a digit past the end moved the cursor: %+v", ans)
	}
}

// The custom row is a row like any other, so it gets a number too — it is
// otherwise the one entry in the list you cannot reach by its label.
func TestQuestionDialogDigitReachesTheCustomRow(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{
		Question: "Pick", Options: []string{"alpha", "beta"}, AllowCustom: true,
	})
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '3'})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	for _, r := range "mine" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, resp); ans.Answer != "mine" {
		t.Fatalf("want the custom answer, got %+v", ans)
	}
}

// Digits belong to the editor once a field is open, or "1" could never be
// typed as an answer.
func TestQuestionDialogDigitsTypeWhileAnswering(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "How many?"})
	for _, r := range "12" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, resp); ans.Answer != "12" {
		t.Fatalf("digits were eaten as shortcuts: %+v", ans)
	}
}

// Beyond nine there is no digit left to press, so the legend must stop
// promising one — but the rows keep counting, because the number is how
// you refer to a row, not only how you reach it.
func TestQuestionDialogLegendCapsTheDigitRange(t *testing.T) {
	opts := make([]string, 12)
	for i := range opts {
		opts[i] = fmt.Sprintf("option %d", i+1)
	}
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Pick", Options: opts})
	if got := legendRow(t, d, 90); !strings.Contains(got, "1-9 pick") {
		t.Fatalf("legend promises digits that do not exist: %q", got)
	}
	if got := plainRender(d, 90); !strings.Contains(got, "12. option 12") {
		t.Fatalf("the list stopped numbering at the shortcut limit:\n%s", got)
	}
}
