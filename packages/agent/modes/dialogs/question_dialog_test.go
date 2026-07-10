package dialogs

import (
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

func enqueue(d *QuestionDialog, q *QuestionRequest) chan core.UserAnswer {
	q.Resp = make(chan core.UserAnswer, 1)
	d.Enqueue(q)
	return q.Resp
}

func TestQuestionDialogSelect(t *testing.T) {
	d := NewQuestionDialog()
	resp := enqueue(d, &QuestionRequest{Question: "Which DB?", Options: []string{"Postgres", "SQLite"}})
	if !d.Active() {
		t.Fatal("dialog should be active")
	}
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // cursor -> SQLite
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	ans := <-resp
	if ans.Declined || ans.Answer != "SQLite" {
		t.Fatalf("want SQLite, got %+v", ans)
	}
	if d.Active() {
		t.Error("dialog should be empty after answering")
	}
}

func TestQuestionDialogDecline(t *testing.T) {
	d := NewQuestionDialog()
	resp := enqueue(d, &QuestionRequest{Question: "x?", Options: []string{"a"}})
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if ans := <-resp; !ans.Declined {
		t.Fatalf("esc should decline, got %+v", ans)
	}
}

func TestQuestionDialogFreeText(t *testing.T) {
	d := NewQuestionDialog()
	resp := enqueue(d, &QuestionRequest{Question: "Name?"}) // no options -> typing
	for _, r := range "Ada" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyBackspace}) // -> "Ad"
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'e'})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := <-resp; ans.Answer != "Ade" {
		t.Fatalf("want Ade, got %+v", ans)
	}
}

func TestQuestionDialogCustomRow(t *testing.T) {
	d := NewQuestionDialog()
	resp := enqueue(d, &QuestionRequest{Question: "Pick", Options: []string{"a"}, AllowCustom: true})
	// Rows: ["a", "Type my own answer…"]. Move to the custom row, enter
	// to switch to typing, then type an answer.
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	for _, r := range "zed" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := <-resp; ans.Answer != "zed" {
		t.Fatalf("want zed, got %+v", ans)
	}
}

func TestQuestionDialogEmptyFreeTextStaysOpen(t *testing.T) {
	d := NewQuestionDialog()
	_ = enqueue(d, &QuestionRequest{Question: "Name?"})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // empty submit ignored
	if !d.Active() {
		t.Error("empty free-text submit should keep the dialog open")
	}
}
