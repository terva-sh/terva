package dialogs

import (
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// answerLines returns the rendered rows between the "your answer:" label
// and the blank that separates the field from the key legend — i.e. the
// answer field itself. The field's own rows always carry the indent, so
// the bare "" that ends them is unambiguous.
func answerLines(t *testing.T, d *QuestionDialog, width int) []string {
	t.Helper()
	return listLinesAfter(t, d, width, "your answer:")
}

// listLinesAfter is the shared body-slicer: the rows between the label
// row containing marker and the blank row that ends the list.
func listLinesAfter(t *testing.T, d *QuestionDialog, width int, marker string) []string {
	t.Helper()
	rows := d.Render(tui.Theme{}, width)
	start := -1
	for i, r := range rows {
		if strings.Contains(widgets.StripANSIBytes(r), marker) {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("no %q label in render: %q", marker, rows)
	}
	var out []string
	for _, r := range rows[start:] {
		if r == "" || isFrameRuleLine(r) {
			break
		}
		out = append(out, widgets.StripANSIBytes(r))
	}
	return out
}

// A typed answer longer than the dialog used to render as one line that
// ran off the right edge, taking the text the user was typing out of
// sight. It must wrap instead.
func TestQuestionDialogAnswerWraps(t *testing.T) {
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Name?"})
	const width = 40
	for _, r := range strings.Repeat("abcde ", 20) {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}

	rows := answerLines(t, d, width)
	if len(rows) < 2 {
		t.Fatalf("answer did not wrap: %d row(s) %q", len(rows), rows)
	}
	for _, r := range rows {
		if got := len([]rune(r)); got > width {
			t.Fatalf("row overflows the dialog: %d cells > %d — %q", got, width, r)
		}
	}
}

// The caret has to follow the text onto the wrapped rows, and the host
// has to be told where it is — otherwise the real terminal cursor stays
// parked on the hidden main editor.
func TestQuestionDialogCursorTracksWrappedAnswer(t *testing.T) {
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Name?"})
	const width = 40

	row0, col0 := d.CursorPos(width)
	if row0 < 0 {
		t.Fatal("CursorPos reported no caret while typing an answer")
	}
	if col0 != len(answerIndent) {
		t.Fatalf("empty answer caret col = %d, want %d", col0, len(answerIndent))
	}

	for _, r := range strings.Repeat("abcde ", 20) {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	row1, _ := d.CursorPos(width)
	if row1 <= row0 {
		t.Fatalf("caret row did not follow the wrap: %d -> %d", row0, row1)
	}
	// The caret must sit on the field's last row, not past it.
	rows := answerLines(t, d, width)
	if want := row0 + len(rows) - 1; row1 != want {
		t.Fatalf("caret row = %d, want %d (field has %d rows)", row1, want, len(rows))
	}
}

// The field lost its painted "█" when the real terminal caret took over,
// so an empty answer must still occupy exactly one row — otherwise the
// caret CursorPos reports lands on the frame rule below it.
func TestQuestionDialogEmptyAnswerKeepsItsRow(t *testing.T) {
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Name?"})
	if rows := answerLines(t, d, 60); len(rows) != 1 {
		t.Fatalf("empty answer field rendered %d rows %q, want exactly 1", len(rows), rows)
	}
	row, _ := d.CursorPos(60)
	full := PadDialogFrame(d.Render(tui.Theme{}, 60))
	if row < 0 || row >= len(full) {
		t.Fatalf("caret row %d outside the %d rendered rows", row, len(full))
	}
	if isFrameRuleLine(full[row]) {
		t.Fatalf("caret row %d is the frame rule, not the answer field", row)
	}
}

// The answer field used to be exactly one row, so the body's height was
// bounded by the question alone. A wrapped answer is not: without a
// budget a long one pushes the frame — and the chat above it — off the
// terminal.
func TestQuestionDialogBodyRespectsMaxRows(t *testing.T) {
	const width, maxRows = 40, 8
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: strings.Repeat("a long question. ", 30)})
	d.MaxRows = maxRows
	for _, r := range strings.Repeat("answer text ", 60) {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}

	// Body = everything between the frame header and the frame rule.
	rows := d.Render(tui.Theme{}, width)
	if body := len(rows) - 2; body > maxRows {
		t.Fatalf("body is %d rows, want <= %d:\n%s", body, maxRows, strings.Join(rows, "\n"))
	}
	// The caret has to stay inside the block that is actually painted.
	row, _ := d.CursorPos(width)
	full := PadDialogFrame(rows)
	if row < 0 || row >= len(full) {
		t.Fatalf("caret row %d outside the %d painted rows", row, len(full))
	}
	if isFrameRuleLine(full[row]) || isFrameHeaderLine(full[row]) {
		t.Fatalf("caret row %d landed on the frame, not the answer field", row)
	}
}

// Typing forward past the field's budget must scroll the field, not hide
// the caret: the window follows it and the caret stays on the last row.
func TestQuestionDialogFieldScrollsWithTheCaret(t *testing.T) {
	const width, maxRows = 40, 8
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Short?"})
	d.MaxRows = maxRows

	var lastTail string
	for range 40 {
		for _, r := range "wwww " {
			d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
		}
		rows := answerLines(t, d, width)
		row, _ := d.CursorPos(width)
		full := PadDialogFrame(d.Render(tui.Theme{}, width))
		if row < 0 || row >= len(full) {
			t.Fatalf("caret row %d outside the %d painted rows", row, len(full))
		}
		// The caret is always on the field's own last row while typing
		// forward, and the field keeps scrolling as text is added.
		// answerLines already carries the indent.
		want := rows[len(rows)-1]
		if got := widgets.StripANSIBytes(full[row]); got != want {
			t.Fatalf("caret row %q is not the field's last row %q", got, want)
		}
		lastTail = rows[len(rows)-1]
	}
	if !strings.Contains(lastTail, "w") {
		t.Fatalf("field tail %q lost the typed text", lastTail)
	}
}

// The multiple-choice view has no caret of its own; reporting one would
// draw a stray block over the option list.
func TestQuestionDialogNoCursorWhileChoosing(t *testing.T) {
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Pick", Options: []string{"a", "b"}})
	if row, _ := d.CursorPos(40); row >= 0 {
		t.Fatalf("CursorPos = %d while choosing; want -1", row)
	}
}

// Choice mode shares the same budget, and its list windows around the
// selection — so a long option list stays inside the band and the
// highlighted row is always one of the rows actually drawn.
func TestQuestionDialogOptionListRespectsMaxRows(t *testing.T) {
	const width, maxRows = 40, 8
	opts := make([]string, 20)
	for i := range opts {
		opts[i] = fmt.Sprintf("option number %d", i)
	}
	d := NewQuestionDialog()
	_ = ask1(d, core.UserQuestion{Question: "Pick one", Options: opts})
	d.MaxRows = maxRows
	for range 15 {
		d.HandleKey(tui.Key{Kind: tui.KeyDown})
	}

	rows := d.Render(tui.Theme{}, width)
	if body := len(rows) - 2; body > maxRows {
		t.Fatalf("body is %d rows, want <= %d:\n%s", body, maxRows, strings.Join(rows, "\n"))
	}
	// Row 15 is selected; the window must have followed it.
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "option number 15") {
		t.Fatalf("selected option scrolled out of the window:\n%s", joined)
	}
}

// The buffer version understood only rune/backspace/enter, so the cursor
// could never leave the end of the text. Arrow keys, word motions, and
// the line-kill chords all have to edit in place now.
func TestQuestionDialogAnswerEditingKeys(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Name?"})
	for _, r := range "hello world" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	// Back over "world", insert in the middle.
	d.HandleKey(tui.Key{Kind: tui.KeyLeft, Alt: true})
	for _, r := range "big " {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	// Home, then delete forward one grapheme: "hello big world" -> "ello…".
	d.HandleKey(tui.Key{Kind: tui.KeyHome})
	d.HandleKey(tui.Key{Kind: tui.KeyDelete})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})

	if ans := one(t, resp); ans.Answer != "ello big world" {
		t.Fatalf("want %q, got %+v", "ello big world", ans)
	}
}

// A pasted answer arrives as one KeyPaste, which the buffer version
// dropped on the floor.
func TestQuestionDialogAnswerAcceptsPaste(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Name?"})
	d.HandleKey(tui.Key{Kind: tui.KeyPaste, Paste: "pasted answer"})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, resp); ans.Answer != "pasted answer" {
		t.Fatalf("want pasted answer, got %+v", ans)
	}
}

// Esc still declines the whole question rather than clearing the draft:
// the agent goroutine is blocked on the answer and has to unblock. This
// question has no options, so there is no list for esc to fall back to —
// it arms the skip, and the second press answers.
func TestQuestionDialogEscDeclinesWhileTyping(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Name?"})
	for _, r := range "half" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if !d.Active() {
		t.Fatal("a single esc answered the ask; it should arm the skip and wait")
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEsc})
	if ans := one(t, resp); !ans.Declined {
		t.Fatalf("esc esc while typing should decline, got %+v", ans)
	}
}

// A modified Enter inserts a newline in the answer, matching the main
// composer; only a bare Enter submits.
func TestQuestionDialogMultiLineAnswer(t *testing.T) {
	d := NewQuestionDialog()
	resp := ask1(d, core.UserQuestion{Question: "Name?"})
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'a'})
	d.HandleKey(tui.Key{Kind: tui.KeyEnter, Ctrl: true})
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'b'})
	if !d.Active() {
		t.Fatal("ctrl+enter submitted the answer; want a newline")
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, resp); ans.Answer != "a\nb" {
		t.Fatalf("want %q, got %+v", "a\nb", ans)
	}
}

// Each queued question gets a clean field; the previous answer must not
// bleed into the next one.
func TestQuestionDialogAnswerResetsBetweenRequests(t *testing.T) {
	d := NewQuestionDialog()
	first := ask1(d, core.UserQuestion{Question: "one?"})
	second := ask1(d, core.UserQuestion{Question: "two?"})
	for _, r := range "aaa" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, first); ans.Answer != "aaa" {
		t.Fatalf("first answer = %+v", ans)
	}
	for _, r := range "bbb" {
		d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnter})
	if ans := one(t, second); ans.Answer != "bbb" {
		t.Fatalf("second answer = %+v (leaked the first?)", ans)
	}
}
