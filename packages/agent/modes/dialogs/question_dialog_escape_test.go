package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// plainRender is the dialog's rendered block with the styling stripped,
// for assertions about what the user can actually read.
func plainRender(d *QuestionDialog, width int) string {
	var b strings.Builder
	for _, l := range d.Render(tui.Theme{}, width) {
		b.WriteString(widgets.StripANSIBytes(l))
		b.WriteString("\n")
	}
	return b.String()
}

// Selecting "Type my own answer…" used to be a trapdoor: the option list
// was gone, nothing on screen said how to get back to it, and esc — the
// key everyone tries first when leaving a text field — ended the whole
// ask instead. Esc now leaves the field for the list it was reached from.
func TestQuestionDialogEscFromCustomAnswerReturnsToOptions(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{
		Question: "Which one?", Options: []string{"alpha", "beta"}, AllowCustom: true,
	})
	// Down to the custom row, enter to start typing, type something.
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	for _, r := range "mine" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	if !strings.Contains(plainRender(d, 50), "your answer:") {
		t.Fatal("precondition: the custom row should have opened the answer field")
	}

	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if !d.Active() {
		t.Fatal("esc out of the answer field ended the ask; it should return to the options")
	}
	body := plainRender(d, 50)
	if !strings.Contains(body, "alpha") || !strings.Contains(body, "beta") {
		t.Fatalf("the option list did not come back:\n%s", body)
	}
	if !strings.Contains(body, "choose an answer") {
		t.Fatalf("still in the answer field after esc:\n%s", body)
	}

	// The draft survives, so re-entering resumes rather than starting over,
	// and enter on an option still answers with the option.
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // cursor is back on the custom row
	if got := plainRender(d, 50); !strings.Contains(got, "mine") {
		t.Fatalf("the typed draft was discarded on the way out:\n%s", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})   // back to the options again
	d.HandleKey(tui.Key{Kind: tui.KeyUp})    // onto "beta"
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // and answer with it
	if ans := one(t, resp); ans.Answer != "beta" {
		t.Fatalf("want beta (the leftover draft must not win), got %+v", ans)
	}
}

// The hint has to say so: the affordance is the whole point, and a user
// who cannot see the way back is in the same trapdoor whether or not the
// key works.
func TestQuestionDialogAnswerFieldSaysHowToGoBack(t *testing.T) {
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{
		Question: "Which one?", Options: []string{"alpha"}, AllowCustom: true,
	})
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if got := plainRender(d, 70); !strings.Contains(got, "esc back to options") {
		t.Fatalf("the answer field does not offer a way back:\n%s", got)
	}

	// A question with no options has nowhere to go back to, so it must not
	// claim otherwise — there esc still means skip.
	d2 := NewQuestionDialog()
	_ = ask1(d2, core.UserQuestion{Question: "Name?"})
	if got := plainRender(d2, 70); strings.Contains(got, "back to options") {
		t.Fatalf("an option-less question promises a list it does not have:\n%s", got)
	}
}

// Skipping is irreversible — the agent is told to proceed without you —
// so one reflexive esc must not do it. The first press warns, and says
// what would happen; the second press is the answer.
func TestQuestionDialogEscWarnsBeforeSkipping(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Pick", Options: []string{"a", "b"}})

	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if !d.Active() {
		t.Fatal("the first esc skipped the question outright")
	}
	warn := plainRender(d, 80)
	if !strings.Contains(warn, "esc again") {
		t.Fatalf("nothing on screen says a second esc would skip:\n%s", warn)
	}
	if !strings.Contains(warn, "proceeds without your answer") {
		t.Fatalf("the warning does not say what skipping costs:\n%s", warn)
	}

	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if ans := one(t, resp); !ans.Declined {
		t.Fatalf("the second esc should skip, got %+v", ans)
	}
}

// An armed skip that survives an unrelated keypress would fire on an esc
// pressed minutes later for something else entirely.
func TestQuestionDialogAnyKeyDisarmsTheSkip(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Pick", Options: []string{"a", "b"}})

	d.HandleKey(tui.Key{Kind: tui.KeyEsc})  // arm
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // ...and change your mind
	if got := plainRender(d, 80); strings.Contains(got, "esc again") {
		t.Fatalf("the warning outlived the keypress that cancelled it:\n%s", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if !d.Active() {
		t.Fatal("a disarmed skip still fired on the next single esc")
	}

	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if ans := one(t, resp); !ans.Declined {
		t.Fatalf("want declined after re-arming, got %+v", ans)
	}
}

// Ctrl+c stays unguarded on purpose: the agent goroutine is blocked on
// this dialog, so one key has to end it outright, every time.
func TestQuestionDialogCtrlCSkipsWithoutConfirming(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Pick", Options: []string{"a"}})
	d.HandleKey(tui.Key{Kind: tui.KeyCtrlC})
	if ans := one(t, resp); !ans.Declined {
		t.Fatalf("ctrl+c should decline immediately, got %+v", ans)
	}
}

// A set skips as a whole, so the warning has to name the whole — "skip
// this question" would understate it by two questions out of three.
func TestQuestionSetSkipWarningNamesTheWholeSet(t *testing.T) {
	d := NewQuestionDialog()
	resp := askN(d,
		core.UserQuestion{Question: "one?", Options: []string{"a"}},
		core.UserQuestion{Question: "two?", Options: []string{"b"}},
		core.UserQuestion{Question: "three?", Options: []string{"c"}},
	)
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if got := plainRender(d, 90); !strings.Contains(got, "ALL 3 questions") {
		t.Fatalf("the warning does not say the whole set goes:\n%s", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if ans := <-resp; len(ans) != 3 {
		t.Fatalf("declining gave %d answers, want 3", len(ans))
	}
}

// The submit tab declines on esc too, so it needs the same guard — it is
// the tab a user lands on with every answer already filled in, which is
// the worst possible moment to lose the set to one keypress.
func TestQuestionSetSubmitTabWarnsBeforeSkipping(t *testing.T) {
	d := NewQuestionDialog()
	resp := askN(d,
		core.UserQuestion{Question: "one?", Options: []string{"a"}},
		core.UserQuestion{Question: "two?", Options: []string{"b"}},
	)
	d.HandleKey(tui.Key{Kind: tui.KeyTab})
	d.HandleKey(tui.Key{Kind: tui.KeyTab}) // onto submit
	if got := plainRender(d, 90); !strings.Contains(got, "enter sends") {
		t.Fatalf("precondition: not on the submit tab:\n%s", got)
	}

	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if !d.Active() {
		t.Fatal("the first esc on the submit tab discarded the whole set")
	}
	if got := plainRender(d, 90); !strings.Contains(got, "esc again") {
		t.Fatalf("the submit tab does not warn:\n%s", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if ans := <-resp; len(ans) != 2 || !ans[0].Declined {
		t.Fatalf("want 2 declines, got %+v", ans)
	}
}

// Esc out of the answer field must not leave a skip armed behind it: the
// user asked to go back to the options, not to half-skip the ask.
func TestQuestionDialogReturningToOptionsLeavesNoArmedSkip(t *testing.T) {
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{
		Question: "Which?", Options: []string{"alpha"}, AllowCustom: true,
	})
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // into the answer field
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})   // back to the options
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})   // this is a FIRST esc, not a second
	if !d.Active() {
		t.Fatal("the esc that left the field also counted toward skipping")
	}
}
