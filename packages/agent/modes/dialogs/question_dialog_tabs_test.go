package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// A set of questions is one interruption: the user moves between tabs,
// revises, and sends once. Tool calls run strictly one at a time, so
// without this an agent with three things to clarify stalls the turn
// three separate times and the user answers each blind to the rest.

func threeQuestions(d *QuestionDialog) chan []core.UserAnswer {
	return askN(d,
		core.UserQuestion{Question: "Which database?", Options: []string{"Postgres", "SQLite"}, AllowCustom: true},
		core.UserQuestion{Question: "Migrate when?", Options: []string{"Now", "At deploy"}},
		core.UserQuestion{Question: "Name it?"},
	)
}

func plain(rows []string) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(widgets.StripANSIBytes(r))
		b.WriteByte('\n')
	}
	return b.String()
}

func TestQuestionSetSubmitsEveryAnswerAtOnce(t *testing.T) {
	d := NewQuestionDialog()
	resp := threeQuestions(d)

	d.HandleKey(tui.Key{Kind: tui.KeyDown})  // -> SQLite
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // commit, advance to q2
	d.HandleKey(tui.Key{Kind: tui.KeyDown})  // -> At deploy
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // advance to q3 (free text)
	for _, r := range "billing-api" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	// Enter on the last question advances to submit, it does NOT send —
	// the whole point is that the user gets a look before committing.
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if !d.Active() {
		t.Fatal("enter on the last question sent the set; want the submit tab")
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	ans := <-resp
	want := []string{"SQLite", "At deploy", "billing-api"}
	if len(ans) != 3 {
		t.Fatalf("got %d answers, want 3: %+v", len(ans), ans)
	}
	for i, w := range want {
		if ans[i].Declined || ans[i].Answer != w {
			t.Fatalf("answer %d = %+v, want %q", i, ans[i], w)
		}
	}
}

// The value of tabs is going back. An answer changed after the fact must
// be the one that ships.
func TestQuestionSetRevisesAnAnswerBeforeSubmit(t *testing.T) {
	d := NewQuestionDialog()
	resp := threeQuestions(d)

	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // q1 = Postgres, -> q2
	d.HandleKey(tui.Key{Kind: tui.KeyDown})  // q2 -> At deploy
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // -> q3
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // -> submit (q3 left empty)

	// Back two tabs to q2 and change the mind.
	d.HandleKey(tui.Key{Kind: tui.KeyShiftTab})
	d.HandleKey(tui.Key{Kind: tui.KeyShiftTab})
	d.HandleKey(tui.Key{Kind: tui.KeyUp}) // -> Now
	// Forward to submit and send.
	d.HandleKey(tui.Key{Kind: tui.KeyTab})
	d.HandleKey(tui.Key{Kind: tui.KeyTab})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	ans := <-resp
	if ans[1].Answer != "Now" {
		t.Fatalf("revised answer = %+v, want Now", ans[1])
	}
	// q3 was never typed into: a free-text question left empty declines
	// on its own, without declining the rest.
	if !ans[2].Declined {
		t.Fatalf("empty free text = %+v, want declined", ans[2])
	}
	if ans[0].Declined || ans[0].Answer != "Postgres" {
		t.Fatalf("untouched choice = %+v, want its default", ans[0])
	}
}

// Tab wraps through the questions and the submit tab, in both directions.
func TestQuestionSetTabWraps(t *testing.T) {
	d := NewQuestionDialog()
	_ = threeQuestions(d)
	titles := []string{}
	for range 5 { // 3 questions + submit, then back to the first
		titles = append(titles, strings.TrimSpace(widgets.StripANSIBytes(d.Render(tui.Theme{}, 60)[0])))
		d.HandleKey(tui.Key{Kind: tui.KeyTab})
	}
	want := []string{
		"── question 1 of 3", "── question 2 of 3", "── question 3 of 3",
		"── questions — ready to send", "── question 1 of 3",
	}
	for i := range want {
		if !strings.HasPrefix(titles[i], want[i]) {
			t.Fatalf("tab %d title = %q, want prefix %q", i, titles[i], want[i])
		}
	}
	// And shift+tab steps backwards off the first tab onto submit. The
	// loop above left us on tab 2, so walk back to tab 1 explicitly
	// rather than depending on where it stopped.
	d2 := NewQuestionDialog()
	_ = threeQuestions(d2)
	d2.HandleKey(tui.Key{Kind: tui.KeyShiftTab})
	if got := widgets.StripANSIBytes(d2.Render(tui.Theme{}, 60)[0]); !strings.Contains(got, "ready to send") {
		t.Fatalf("shift+tab off tab 1 = %q, want the submit tab", got)
	}
}

// The strip's tick means "you have seen this", not "a default exists" —
// otherwise every choice question is ticked before the user has read any
// of them and the strip says nothing about progress.
func TestQuestionSetStripTicksOnlyVisitedTabs(t *testing.T) {
	d := NewQuestionDialog()
	_ = threeQuestions(d)

	strip := widgets.StripANSIBytes(d.Render(tui.Theme{}, 60)[1])
	if !strings.Contains(strip, "1✓") {
		t.Fatalf("strip %q: the open tab should be ticked", strip)
	}
	if strings.Contains(strip, "2✓") || strings.Contains(strip, "3✓") {
		t.Fatalf("strip %q: unvisited tabs must not be ticked", strip)
	}

	d.HandleKey(tui.Key{Kind: tui.KeyTab})
	if strip = widgets.StripANSIBytes(d.Render(tui.Theme{}, 60)[1]); !strings.Contains(strip, "2✓") {
		t.Fatalf("strip %q: tab 2 is visited now", strip)
	}
	// Tab 3 is free text — visiting it is not answering it.
	d.HandleKey(tui.Key{Kind: tui.KeyTab})
	if strip = widgets.StripANSIBytes(d.Render(tui.Theme{}, 60)[1]); strings.Contains(strip, "3✓") {
		t.Fatalf("strip %q: an empty free-text tab is not answered", strip)
	}
}

// The submit tab is a review: it shows what will actually be sent,
// including the questions the user never opened.
func TestQuestionSetSubmitTabReviewsTheAnswers(t *testing.T) {
	d := NewQuestionDialog()
	_ = threeQuestions(d)
	for range 3 {
		d.HandleKey(tui.Key{Kind: tui.KeyTab})
	}
	body := plain(d.Render(tui.Theme{}, 70))
	for _, want := range []string{"Postgres", "Now", "Which database?", "Migrate when?", "Name it?"} {
		if !strings.Contains(body, want) {
			t.Fatalf("submit review missing %q:\n%s", want, body)
		}
	}
	if !strings.Contains(body, "no answer") {
		t.Fatalf("the unanswered free-text question should say so:\n%s", body)
	}
}

// Esc declines the whole ask from any tab, and the agent must get one
// answer per question rather than a short slice.
func TestQuestionSetEscDeclinesEverything(t *testing.T) {
	d := NewQuestionDialog()
	resp := threeQuestions(d)
	d.HandleKey(tui.Key{Kind: tui.KeyTab}) // somewhere in the middle
	d.HandleKey(tui.Key{Kind: tui.KeyEsc}) // arms
	d.HandleKey(tui.Key{Kind: tui.KeyEsc}) // confirms

	ans := <-resp
	if len(ans) != 3 {
		t.Fatalf("declining gave %d answers, want 3 — the contract is positional", len(ans))
	}
	for i, a := range ans {
		if !a.Declined {
			t.Fatalf("answer %d = %+v, want declined", i, a)
		}
	}
}

func TestQuestionSetCancelAllDeclinesEveryQuestion(t *testing.T) {
	d := NewQuestionDialog()
	resp := threeQuestions(d)
	d.CancelAll()
	if ans := <-resp; len(ans) != 3 || !ans[0].Declined || !ans[2].Declined {
		t.Fatalf("CancelAll gave %+v, want three declines", ans)
	}
}

// A custom answer inside a set reaches the right question, and typing in
// one tab does not leak into another.
func TestQuestionSetCustomAnswerPerTab(t *testing.T) {
	d := NewQuestionDialog()
	resp := threeQuestions(d)

	// q1: pick the "Type my own answer…" row (it is the row after the options).
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // -> typing
	for _, r := range "CockroachDB" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // commit, -> q2
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // q2 default, -> q3
	for _, r := range "svc" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // -> submit
	d.HandleKey(tui.Key{Kind: tui.KeyEnter}) // send

	ans := <-resp
	if ans[0].Answer != "CockroachDB" {
		t.Fatalf("custom answer = %+v", ans[0])
	}
	if ans[1].Answer != "Now" {
		t.Fatalf("q2 = %+v, want its default", ans[1])
	}
	if ans[2].Answer != "svc" {
		t.Fatalf("q3 = %+v", ans[2])
	}
}

// A single-question ask keeps the original shape exactly: no strip, no
// submit tab, enter sends. Tab must not open one either.
func TestSingleQuestionKeepsTheOldShape(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Which DB?", Options: []string{"Postgres", "SQLite"}})

	body := plain(d.Render(tui.Theme{}, 60))
	if strings.Contains(body, "submit") || strings.Contains(body, "1✓") {
		t.Fatalf("a single question grew a tab strip:\n%s", body)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyTab})
	if body := plain(d.Render(tui.Theme{}, 60)); strings.Contains(body, "submit") {
		t.Fatalf("tab opened a submit pane for one question:\n%s", body)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, resp); ans.Answer != "Postgres" {
		t.Fatalf("enter should send immediately, got %+v", ans)
	}
}

// The caret still has to land in the answer field on whichever tab is
// typing, with the tab strip's rows counted in.
func TestQuestionSetCaretTracksTheTypingTab(t *testing.T) {
	const width = 60
	d := NewQuestionDialog()
	_ = threeQuestions(d)
	if row, _ := d.CursorPos(width); row >= 0 {
		t.Fatalf("choice tab reported a caret at row %d, want -1", row)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyTab})
	d.HandleKey(tui.Key{Kind: tui.KeyTab}) // q3, free text
	for _, r := range "some name" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	row, col := d.CursorPos(width)
	full := PadDialogFrame(d.Render(tui.Theme{}, width))
	if row < 0 || row >= len(full) {
		t.Fatalf("caret row %d outside the %d painted rows", row, len(full))
	}
	if got := widgets.StripANSIBytes(full[row]); !strings.Contains(got, "some name") {
		t.Fatalf("caret row %q is not the answer field", got)
	}
	if col <= len(answerIndent) {
		t.Fatalf("caret col = %d, want past the indent and the typed text", col)
	}
	// And the submit tab has no caret of its own.
	d.HandleKey(tui.Key{Kind: tui.KeyTab})
	if row, _ := d.CursorPos(width); row >= 0 {
		t.Fatalf("submit tab reported a caret at row %d, want -1", row)
	}
}

// The set's body is budgeted like the single-question one, tab strip
// included, or a long set pushes the frame off the terminal.
func TestQuestionSetRespectsMaxRows(t *testing.T) {
	const width, maxRows = 50, 10
	qs := make([]core.UserQuestion, 0, core.MaxAskQuestions)
	for i := range core.MaxAskQuestions {
		qs = append(qs, core.UserQuestion{
			Question: strings.Repeat("a wordy question ", 8) + string(rune('a'+i)),
			Options:  []string{"one", "two", "three", "four", "five", "six"},
		})
	}
	d := NewQuestionDialog()
	_ = askN(d, qs...)
	d.MaxRows = maxRows

	for tab := 0; tab <= len(qs); tab++ {
		rows := d.Render(tui.Theme{}, width)
		if body := len(rows) - 2; body > maxRows {
			t.Fatalf("tab %d body is %d rows, want <= %d:\n%s", tab, body, maxRows, plain(rows))
		}
		d.HandleKey(tui.Key{Kind: tui.KeyTab})
	}
}
