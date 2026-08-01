package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// A custom answer typed and then escaped out of was kept but INVISIBLE:
// the option list showed the bare "Type my own answer…" row, identical to
// one never touched. Text you wrote and cannot see reads as text you
// lost, which is what makes someone type it twice.
func TestQuestionDialogParkedCustomAnswerIsVisible(t *testing.T) {
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{
		Question: "Which?", Options: []string{"alpha", "beta"}, AllowCustom: true,
	})
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '3'}) // the custom row
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	typeInto(d, "my own thing")
	d.HandleKey(tui.Key{Kind: tui.KeyEsc}) // back to the list

	body := plainRender(d, 60)
	if !strings.Contains(body, "my own thing") {
		t.Fatalf("the parked custom answer is invisible from the list:\n%s", body)
	}
	// It is still a draft, not the answer: moving to an option and
	// answering must send the option.
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '1'})
	if got := plainRender(d, 60); !strings.Contains(got, "my own thing") {
		t.Fatalf("the draft vanished when the cursor moved off its row:\n%s", got)
	}
}

// A note parked on another option had the same problem: it only rendered
// while its own option was selected, so stepping away made it disappear
// even though it was still there.
func TestQuestionDialogParkedNoteStaysVisible(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{
		Question: "Which?", Options: []string{"alpha", "beta"},
	})
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'n'})
	typeInto(d, "only with a pool in front")
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // stand on the other option

	if got := plainRender(d, 60); !strings.Contains(got, "only with a pool in front") {
		t.Fatalf("the note disappeared when the cursor moved off its option:\n%s", got)
	}
	// Visible is not the same as applied: it must still not ride along
	// with the option it was not written about.
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, resp); ans.Answer != "beta" || ans.Note != "" {
		t.Fatalf("a visible note was sent with the wrong answer: %+v", ans)
	}
}

// While the skip warning is up it takes the key legend's row, so the only
// key named on screen is the one that skips. Someone who hit esc by
// reflex needs the other half of the choice.
func TestQuestionDialogSkipWarningSaysHowToBackOut(t *testing.T) {
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Pick", Options: []string{"a", "b"}})
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if got := plainRender(d, 100); !strings.Contains(got, "any other key carries on") {
		t.Fatalf("the warning does not say how to cancel it:\n%s", got)
	}
}

// Eight questions was eight tabs to reach the last one. alt+N goes
// straight there.
func TestQuestionSetAltDigitJumpsToAQuestion(t *testing.T) {
	d := NewQuestionDialog()
	_ = askN(d,
		core.UserQuestion{Question: "first?", Options: []string{"a"}},
		core.UserQuestion{Question: "second?", Options: []string{"b"}},
		core.UserQuestion{Question: "third?", Options: []string{"c"}})

	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '3', Alt: true})
	if got := plainRender(d, 70); !strings.Contains(got, "third?") {
		t.Fatalf("alt+3 did not jump to question 3:\n%s", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '1', Alt: true})
	if got := plainRender(d, 70); !strings.Contains(got, "first?") {
		t.Fatalf("alt+1 did not jump back:\n%s", got)
	}
}

// The bare digit still picks an OPTION. If alt were optional the same key
// would mean two things in one dialog.
func TestQuestionSetBareDigitStillPicksAnOption(t *testing.T) {
	d := NewQuestionDialog()
	resp := askN(d,
		core.UserQuestion{Question: "first?", Options: []string{"a", "b", "c"}},
		core.UserQuestion{Question: "second?", Options: []string{"x"}})

	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '2'}) // option b, not question 2
	if got := plainRender(d, 70); !strings.Contains(got, "first?") {
		t.Fatalf("a bare digit moved between questions:\n%s", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyTab})
	d.HandleKey(tui.Key{Kind: tui.KeyTab}) // onto submit
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := <-resp; ans[0].Answer != "b" {
		t.Fatalf("answer 0 = %+v, want the option the digit picked", ans[0])
	}
}

// A digit past the end of the set must not land the user on a tab that
// does not exist.
func TestQuestionSetAltDigitPastTheSetDoesNothing(t *testing.T) {
	d := NewQuestionDialog()
	_ = askN(d,
		core.UserQuestion{Question: "first?", Options: []string{"a"}},
		core.UserQuestion{Question: "second?", Options: []string{"b"}})
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '9', Alt: true})
	if got := plainRender(d, 70); !strings.Contains(got, "first?") {
		t.Fatalf("alt+9 moved somewhere in a 2-question set:\n%s", got)
	}
}

// The jump works from inside the answer field too — that is where the
// walk is most tedious, and the editor never sees an alt-digit.
func TestQuestionSetAltDigitJumpsFromTheAnswerField(t *testing.T) {
	d := NewQuestionDialog()
	_ = askN(d,
		core.UserQuestion{Question: "first?"},
		core.UserQuestion{Question: "second?", Options: []string{"b"}})
	typeInto(d, "half an answer")
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '2', Alt: true})
	if got := plainRender(d, 70); !strings.Contains(got, "second?") {
		t.Fatalf("alt+2 did not escape the answer field:\n%s", got)
	}
	// And the half-written answer survived the jump.
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '1', Alt: true})
	if got := plainRender(d, 70); !strings.Contains(got, "half an answer") {
		t.Fatalf("jumping away discarded the draft:\n%s", got)
	}
}

// A single question has nothing to jump between, so alt+N must not be
// advertised or acted on there.
func TestQuestionDialogAltDigitIsSetOnly(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Pick", Options: []string{"a", "b"}})
	if got := legendRow(t, d, 90); strings.Contains(got, "alt+") {
		t.Fatalf("a single question advertises a jump it does not have: %q", got)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: '2', Alt: true})
	if !d.Active() {
		t.Fatal("alt+2 resolved a single question")
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, resp); ans.Answer != "a" {
		t.Fatalf("alt+2 moved the option cursor: %+v", ans)
	}
}
