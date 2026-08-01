package dialogs

import (
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// ask1 enqueues a one-question ask, the shape almost every test wants.
func ask1(d *QuestionDialog, q core.UserQuestion) chan []core.UserAnswer {
	return askN(d, q)
}

// one receives a single-question ask's reply. The channel carries a set
// now, so every single-question assertion goes through here rather than
// indexing inline.
func one(t *testing.T, resp chan []core.UserAnswer) core.UserAnswer {
	t.Helper()
	ans := <-resp
	if len(ans) != 1 {
		t.Fatalf("want exactly 1 answer, got %d: %+v", len(ans), ans)
	}
	return ans[0]
}

// askN enqueues a question set.
func askN(d *QuestionDialog, qs ...core.UserQuestion) chan []core.UserAnswer {
	resp := make(chan []core.UserAnswer, 1)
	d.Enqueue(&QuestionRequest{Questions: qs, Resp: resp})
	return resp
}

func TestQuestionDialogSelect(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Which DB?", Options: []string{"Postgres", "SQLite"}})
	if !d.Active() {
		t.Fatal("dialog should be active")
	}
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // cursor -> SQLite
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	ans := one(t, resp)
	if ans.Declined || ans.Answer != "SQLite" {
		t.Fatalf("want SQLite, got %+v", ans)
	}
	if d.Active() {
		t.Error("dialog should be empty after answering")
	}
}

func TestQuestionDialogDecline(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "x?", Options: []string{"a"}})
	d.HandleKey(tui.Key{Kind: tui.KeyEsc}) // arms the skip; does not answer
	if !d.Active() {
		t.Fatal("a single esc answered the ask; it should arm the skip and wait")
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if ans := one(t, resp); !ans.Declined {
		t.Fatalf("esc esc should decline, got %+v", ans)
	}
}

func TestQuestionDialogFreeText(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Name?"}) // no options -> typing
	for _, r := range "Ada" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyBackspace}) // -> "Ad"
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'e'})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, resp); ans.Answer != "Ade" {
		t.Fatalf("want Ade, got %+v", ans)
	}
}

func TestQuestionDialogCustomRow(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Pick", Options: []string{"a"}, AllowCustom: true})
	// Rows: ["a", "Type my own answer…"]. Move to the custom row, enter
	// to switch to typing, then type an answer.
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	for _, r := range "zed" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, resp); ans.Answer != "zed" {
		t.Fatalf("want zed, got %+v", ans)
	}
}

func TestQuestionDialogEmptyFreeTextStaysOpen(t *testing.T) {
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Name?"})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // empty submit ignored
	if !d.Active() {
		t.Error("empty free-text submit should keep the dialog open")
	}
}
