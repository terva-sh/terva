package dialogs

import (
	"strconv"
	"strings"
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// QuestionRequest is one pending ask_user_question — a SET of questions
// posed together. The agent goroutine enqueues it and blocks on Resp
// until the user submits or the turn is cancelled. Resp carries one
// answer per question, in the order asked. Mirrors confirmRequest.
type QuestionRequest struct {
	Questions []core.UserQuestion
	Resp      chan []core.UserAnswer
}

// QuestionDialog renders the ask_user_question prompt: a multiple-choice
// list when options were given, plus a free-text entry when there are no
// options or allow_custom is set. It is the question analog of
// confirmDialog and is an agent-blocking overlay (the tool call waits on
// the answers).
//
// A set of questions renders as tabs with a trailing submit tab, so the
// user can move between them, revise an answer, and send when they are
// ready — one interruption for the whole set instead of one dialog per
// question, each answered blind to the rest. A single question keeps the
// original shape exactly: no tab strip, no submit tab, enter sends.
type QuestionDialog struct {
	mu      sync.Mutex
	pending []*QuestionRequest

	// drafts holds the in-progress answer to each question of the head
	// request, indexed alike. tab is which one is showing; it equals
	// len(drafts) when the submit tab is up (single-question asks have
	// no submit tab, so tab is always 0 there).
	drafts []questionDraft
	tab    int

	// MaxRows budgets the dialog body (everything between the frame
	// header and the frame rule) from the terminal height, the way the
	// session/tasks browsers do. 0 means unbounded. Set by the host
	// ahead of each render, on the main loop, like theirs.
	MaxRows int
}

// questionDraft is the user's working answer to one question.
//
// For a multiple-choice question the highlighted row IS the answer —
// there is no separate commit, the same way a radio group has none — so
// a choice question is always answered, defaulting to the first option.
// Only free text can end up declined, by being left empty at submit.
type questionDraft struct {
	// cursor is the highlighted row: an option index, or the trailing
	// "type my own answer" row when the question allows custom text.
	cursor int
	// visited is true once the tab has been shown. It is what the strip's
	// tick means — "you have looked at this" — rather than "an answer
	// exists", which is true of every choice question from the start and
	// so would tick the whole strip before the user had read any of it.
	// An unvisited default still sends; the submit tab shows what.
	visited bool
	// typing is true while the user is entering free text. Entered
	// directly (a question with no options) or by selecting the custom
	// row.
	typing bool
	// input is a real tui.Editor rather than a []rune buffer. The buffer
	// version rendered as one unwrapped line, so an answer longer than
	// the dialog ran off the right edge and out of sight, and it
	// understood only rune/backspace/enter — no arrows, no word motions,
	// no paste. The editor gives the answer field the same editing the
	// main composer has (wrapping, ←/→, alt+←/→, ctrl+a/e/u/k/w,
	// bracketed paste) and reports a caret position the host can put the
	// real terminal cursor on. Its prompt is empty: the answer field is
	// indented by the dialog, not marked by a glyph.
	input *tui.Editor
}

// answerIndent is the left pad the answer field renders under, and so
// also the column offset CursorPos has to add to the editor's own
// cursor column.
const answerIndent = "  "

func NewQuestionDialog() *QuestionDialog { return &QuestionDialog{} }

// rows returns the selectable rows for question q: one per option, plus
// a trailing "type my own answer" row when allow_custom is set.
func questionRows(q core.UserQuestion) []string {
	rows := append([]string(nil), q.Options...)
	if q.AllowCustom {
		rows = append(rows, i18n.T("Type my own answer…"))
	}
	return rows
}

// customRow reports whether the draft's cursor sits on the "type my own
// answer" affordance rather than a real option.
func customRow(q core.UserQuestion, cursor int) bool {
	return q.AllowCustom && cursor == len(q.Options)
}

func (d *QuestionDialog) Enqueue(req *QuestionRequest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending = append(d.pending, req)
	if len(d.pending) == 1 {
		d.reset(req)
	}
}

// reset arms the drafts for a newly-active request. A question with no
// options goes straight to free-text entry. Caller holds mu.
func (d *QuestionDialog) reset(req *QuestionRequest) {
	d.tab = 0
	d.drafts = make([]questionDraft, len(req.Questions))
	for i, q := range req.Questions {
		d.drafts[i] = questionDraft{input: tui.NewEditor(""), typing: len(q.Options) == 0}
	}
	d.drafts[0].visited = true
}

func (d *QuestionDialog) Active() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pending) > 0
}

// CancelAll declines every pending request. Used when the turn is
// cancelled or the session closes so the agent goroutine unblocks.
func (d *QuestionDialog) CancelAll() {
	d.mu.Lock()
	pending := d.pending
	d.pending = nil
	d.drafts = nil
	d.tab = 0
	d.mu.Unlock()
	for _, req := range pending {
		select {
		case req.Resp <- declineAll(len(req.Questions)):
		default:
		}
	}
}

// declineAll is the answer set for a dismissed ask: one decline per
// question, never a short slice — the Asker contract is positional.
func declineAll(n int) []core.UserAnswer {
	out := make([]core.UserAnswer, n)
	for i := range out {
		out[i] = core.UserAnswer{Declined: true}
	}
	return out
}

// advance pops the head request and arms the next one. Caller holds mu.
func (d *QuestionDialog) advance() {
	d.pending = d.pending[1:]
	d.drafts = nil
	d.tab = 0
	if len(d.pending) > 0 {
		d.reset(d.pending[0])
	}
}

// Remove drops a specific pending request without answering it — the carrier
// path's dismissal when the daemon reports the ask already resolved. Mirrors
// confirmDialog.Remove; a no-op when the request isn't pending.
func (d *QuestionDialog) Remove(req *QuestionRequest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for idx, p := range d.pending {
		if p != req {
			continue
		}
		d.pending = append(d.pending[:idx], d.pending[idx+1:]...)
		if idx == 0 {
			d.drafts = nil
			d.tab = 0
			if len(d.pending) > 0 {
				d.reset(d.pending[0])
			}
		}
		return
	}
}

// HandleKey moves between tabs, edits the showing draft, and submits.
// Returns true when the answer set was sent back to the agent.
func (d *QuestionDialog) HandleKey(k tui.Key) bool {
	d.mu.Lock()
	if len(d.pending) == 0 {
		d.mu.Unlock()
		return false
	}
	req := d.pending[0]
	if len(d.drafts) != len(req.Questions) {
		d.reset(req) // defensive: a request that never got armed
	}

	// Esc declines the WHOLE ask, from any tab, so the agent always
	// unblocks. There is deliberately no per-question skip: a free-text
	// answer left empty is the way to decline just one.
	if k.Kind == tui.KeyEsc || k.Kind == tui.KeyCtrlC {
		n := len(req.Questions)
		d.advance()
		d.mu.Unlock()
		req.Resp <- declineAll(n)
		return true
	}

	// Tab / shift+tab walk the strip. Chosen over ←/→ because the
	// free-text editor owns those for cursor movement, and a chord that
	// works on some tabs but not others is worse than no chord.
	if len(req.Questions) > 1 {
		switch k.Kind {
		case tui.KeyTab:
			d.gotoTabLocked((d.tab + 1) % (len(req.Questions) + 1))
			d.mu.Unlock()
			return false
		case tui.KeyShiftTab:
			d.gotoTabLocked((d.tab + len(req.Questions)) % (len(req.Questions) + 1))
			d.mu.Unlock()
			return false
		}
	}

	// The submit tab: enter sends everything.
	if d.onSubmitTabLocked(req) {
		if k.Kind == tui.KeyEnter {
			answers := d.answersLocked(req)
			d.advance()
			d.mu.Unlock()
			req.Resp <- answers
			return true
		}
		d.mu.Unlock()
		return false
	}

	q := req.Questions[d.tab]
	dr := &d.drafts[d.tab]

	if dr.typing {
		if dr.input == nil {
			dr.input = tui.NewEditor("")
		}
		// The editor owns every editing key, including the modified-Enter
		// newline chords; it reports submit only for a bare Enter.
		if !dr.input.HandleKey(k) {
			d.mu.Unlock()
			return false
		}
		answer := strings.TrimSpace(dr.input.SubmitValue())
		if len(req.Questions) == 1 {
			if answer == "" {
				// Empty submission: keep the prompt open rather than
				// sending a blank answer.
				d.mu.Unlock()
				return false
			}
			d.advance()
			d.mu.Unlock()
			req.Resp <- []core.UserAnswer{{Answer: answer}}
			return true
		}
		// In a set, enter commits this answer and moves on; an empty one
		// is allowed and reads as declining that single question.
		d.gotoTabLocked(d.tab + 1)
		d.mu.Unlock()
		return false
	}

	switch k.Kind {
	case tui.KeyUp:
		if dr.cursor > 0 {
			dr.cursor--
		}
	case tui.KeyDown:
		if dr.cursor < len(questionRows(q))-1 {
			dr.cursor++
		}
	case tui.KeyEnter:
		// The trailing row is the custom-answer affordance when allowed.
		if customRow(q, dr.cursor) {
			dr.typing = true
			d.mu.Unlock()
			return false
		}
		if len(req.Questions) == 1 {
			answer := q.Options[dr.cursor]
			d.advance()
			d.mu.Unlock()
			req.Resp <- []core.UserAnswer{{Answer: answer}}
			return true
		}
		// In a set the highlight already IS the answer, so enter just
		// moves on — to the next question, or to submit.
		d.gotoTabLocked(d.tab + 1)
	}
	d.mu.Unlock()
	return false
}

// gotoTabLocked moves to tab n and marks it seen. Caller holds mu.
func (d *QuestionDialog) gotoTabLocked(n int) {
	d.tab = n
	if n >= 0 && n < len(d.drafts) {
		d.drafts[n].visited = true
	}
}

// onSubmitTabLocked reports whether the trailing submit tab is showing.
// Single-question asks have no submit tab. Caller holds mu.
func (d *QuestionDialog) onSubmitTabLocked(req *QuestionRequest) bool {
	return len(req.Questions) > 1 && d.tab >= len(req.Questions)
}

// answersLocked reads the drafts out as the answer set. Caller holds mu.
func (d *QuestionDialog) answersLocked(req *QuestionRequest) []core.UserAnswer {
	out := make([]core.UserAnswer, len(req.Questions))
	for i, q := range req.Questions {
		out[i] = d.drafts[i].answer(q)
	}
	return out
}

// answer resolves one draft against its question. A choice lands on the
// highlighted option; free text lands on what was typed, and an empty
// box is a decline for that question alone.
func (dr questionDraft) answer(q core.UserQuestion) core.UserAnswer {
	if dr.typing || len(q.Options) == 0 {
		text := ""
		if dr.input != nil {
			text = strings.TrimSpace(dr.input.SubmitValue())
		}
		if text == "" {
			return core.UserAnswer{Declined: true}
		}
		return core.UserAnswer{Answer: text}
	}
	if dr.cursor < 0 || dr.cursor >= len(q.Options) {
		return core.UserAnswer{Declined: true}
	}
	return core.UserAnswer{Answer: q.Options[dr.cursor]}
}

// Render draws the dialog for the head request.
func (d *QuestionDialog) Render(th tui.Theme, width int) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	lines, _, _ := d.renderLocked(th, width)
	return lines
}

// CursorPos reports where the caret sits inside the dialog's rendered
// block so the host can place the real terminal cursor there. Returns
// (-1, -1) unless a free-text answer is being typed — with no caret of
// its own neither the multiple-choice view nor the submit tab needs one,
// and the fallback stands.
//
// It reads the caret off the same renderLocked call Render draws from,
// so the two cannot drift as the layout changes; only the host's
// post-header pad is added on top. The theme is irrelevant to row
// geometry, so a zero one is passed rather than threading the host's
// through a cursor query.
func (d *QuestionDialog) CursorPos(width int) (row, col int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	lines, caretRow, caretCol := d.renderLocked(tui.Theme{}, width)
	if caretRow < 0 {
		return -1, -1
	}
	return caretRow + HeaderPadRows(lines), caretCol
}

// renderLocked wraps the body in the frame chrome and reports the caret
// row within the returned block (-1 when there is none). Caller holds mu.
func (d *QuestionDialog) renderLocked(th tui.Theme, width int) (lines []string, caretRow, caretCol int) {
	body, bRow, bCol := d.bodyLocked(th, width)
	if body == nil {
		return nil, -1, -1
	}
	lines = make([]string, 0, len(body)+2)
	lines = append(lines, FrameHeader(th, d.titleLocked(), width))
	lines = append(lines, body...)
	lines = append(lines, FrameRule(th, width))
	if bRow < 0 {
		return lines, -1, -1
	}
	return lines, bRow + 1 /* frame header */, bCol
}

// titleLocked names the frame. A set says where you are in it, because
// the tab strip alone does not say how much is left. Caller holds mu.
func (d *QuestionDialog) titleLocked() string {
	req := d.pending[0]
	n := len(req.Questions)
	if n <= 1 {
		return i18n.T("question")
	}
	if d.onSubmitTabLocked(req) {
		return i18n.T("questions — ready to send")
	}
	return i18n.T("question %d of %d", d.tab+1, n)
}

// bodyLocked builds everything between the frame header and the frame
// rule, and reports where the caret lands within it (-1 when there is
// none). Returns a nil body when nothing is pending.
//
// One function for both the pixels and the caret: the previous split had
// CursorPos re-deriving Render's row arithmetic by hand, which is a
// standing invitation to drift the moment either side gains a row.
// Caller holds mu.
func (d *QuestionDialog) bodyLocked(th tui.Theme, width int) (lines []string, caretRow, caretCol int) {
	if len(d.pending) == 0 {
		return nil, -1, -1
	}
	req := d.pending[0]
	if len(d.drafts) != len(req.Questions) {
		return nil, -1, -1 // not armed yet; the next frame has it
	}

	// The tab strip, for a set only.
	var strip []string
	if len(req.Questions) > 1 {
		strip = append(strip, answerIndent+d.stripLocked(th, req), "")
	}
	if d.onSubmitTabLocked(req) {
		return append(strip, d.submitBodyLocked(th, req, width, len(strip))...), -1, -1
	}

	q := req.Questions[d.tab]
	dr := d.drafts[d.tab]
	qRows := wrapPlain(q.Question, width-2)

	// Both modes are question block + blank + hint + a variable-height
	// list, so both share one budget: build the list, let the question
	// yield rows to it, then window the list around whatever the user is
	// pointing at (the caret while typing, the selection while choosing).
	var (
		hint     string
		listRows []string
		focus    int
	)
	if dr.typing {
		hint = d.typingHint(len(req.Questions))
		listRows, focus, caretCol = answerRowsOf(dr.input, width)
		caretCol += len(answerIndent)
	} else {
		hint = d.chooseHint(len(req.Questions))
		focus = dr.cursor
		for _, row := range questionRows(q) {
			plain := "  " + row
			if width > 5 {
				// Rune-safe: the byte slice this replaced cut multibyte
				// text mid-rune, and the built-in "Type my own answer…"
				// row ends in one.
				plain = truncateLineSafe(plain, width-2)
			}
			listRows = append(listRows, plain)
		}
	}
	qRows, budget := d.budgetLocked(qRows, len(listRows), len(strip))
	listRows, focus = windowRows(listRows, focus, budget)

	lines = append(lines, strip...)
	for _, wl := range qRows {
		lines = append(lines, "  "+th.FG256(th.Tool, wl))
	}
	lines = append(lines, "")
	lines = append(lines, th.FG256(th.Muted, hint))
	listTop := len(lines)
	for i, l := range listRows {
		if dr.typing {
			lines = append(lines, answerIndent+l)
			continue
		}
		if i == focus {
			lines = append(lines, th.PadHighlight(l, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, l))
		}
	}
	if !dr.typing {
		return lines, -1, -1
	}
	return lines, listTop + focus, caretCol
}

func (d *QuestionDialog) typingHint(n int) string {
	if n > 1 {
		return i18n.T("type your answer · enter for next · tab moves · esc skips all:")
	}
	return i18n.T("type your answer, enter to submit, esc to skip:")
}

func (d *QuestionDialog) chooseHint(n int) string {
	if n > 1 {
		return i18n.T("choose ↑/↓ · enter for next · tab moves · esc skips all:")
	}
	return i18n.T("choose (↑/↓, enter to pick, esc to skip):")
}

// stripLocked renders the tab strip: one numbered chip per question,
// ticked once it has an answer, then the submit chip. Numbers rather
// than truncated question text — a question clipped to eight columns
// tells you less than its position does, and numbers never overflow.
// Caller holds mu.
func (d *QuestionDialog) stripLocked(th tui.Theme, req *QuestionRequest) string {
	var b strings.Builder
	for i, q := range req.Questions {
		if i > 0 {
			b.WriteString("  ")
		}
		chip := strconv.Itoa(i + 1)
		if d.drafts[i].visited && !d.drafts[i].answer(q).Declined {
			chip += "✓"
		}
		if i == d.tab {
			b.WriteString(th.FG256(th.Accent, "▸"+chip))
		} else {
			b.WriteString(" " + th.FG256(th.Muted, chip))
		}
	}
	b.WriteString(th.FG256(th.Muted, "  │ "))
	submit := i18n.T("submit")
	if d.onSubmitTabLocked(req) {
		b.WriteString(th.FG256(th.Accent, "▸"+submit))
	} else {
		b.WriteString(" " + th.FG256(th.Muted, submit))
	}
	return b.String()
}

// submitBodyLocked renders the review tab: every question with the
// answer as it stands, so the user confirms the set rather than
// remembering it. Caller holds mu.
func (d *QuestionDialog) submitBodyLocked(th tui.Theme, req *QuestionRequest, width, chrome int) []string {
	var lines []string
	for i, q := range req.Questions {
		ans := d.drafts[i].answer(q)
		label := ans.Answer
		colour := th.Tool
		if ans.Declined {
			label = i18n.T("(no answer — will be skipped)")
			colour = th.Muted
		}
		for n, wl := range wrapPlain(strconv.Itoa(i+1)+". "+q.Question, width-4) {
			pad := "  "
			if n > 0 {
				pad = "     "
			}
			lines = append(lines, pad+th.FG256(th.Muted, wl))
		}
		for _, wl := range wrapPlain(label, width-8) {
			lines = append(lines, "     → "+th.FG256(colour, wl))
		}
	}
	// Budget the review against the same MaxRows the question tabs use:
	// the strip above it and the blank + hint below it are already spent.
	if avail := d.MaxRows - chrome - 2; d.MaxRows > 0 && len(lines) > avail {
		lines = truncatedRows(lines, max(avail, 1))
	}
	lines = append(lines, "")
	lines = append(lines, th.FG256(th.Muted, i18n.T("enter sends · tab to revisit · esc skips all")))
	return lines
}

// budgetLocked splits MaxRows between the question block and the list
// below it (the answer field, or the option rows), returning the
// question rows to draw and the list's row allowance. With MaxRows unset
// (0) nothing is capped. chrome is any rows already spent above the
// question (the tab strip and its blank).
//
// The answer field is why this exists: it used to be exactly one row, so
// the body's height was bounded by the question alone. A wrapped answer
// is not, and a body taller than the terminal pushes the frame — and the
// chat above it — off screen. The question yields first (it is static
// text the user has already read); the list keeps at least a few rows so
// the caret or the selection always has somewhere to sit. Caller holds mu.
func (d *QuestionDialog) budgetLocked(qRows []string, listRows, chrome int) (question []string, listBudget int) {
	if d.MaxRows <= 0 || listRows <= 0 {
		return qRows, listRows
	}
	// Fixed body chrome in either mode: the blank after the question and
	// the hint line below it, plus anything the tab strip already spent.
	avail := d.MaxRows - 2 - chrome
	if avail < 2 {
		avail = 2
	}
	if len(qRows)+listRows <= avail {
		return qRows, listRows
	}
	// Leave the list at least a third of the body, and never fewer than
	// three rows when the budget can afford them.
	minList := min(max(avail/3, 3), avail-1)
	if minList < 1 {
		minList = 1
	}
	if qMax := avail - minList; len(qRows) > qMax {
		qRows = truncatedRows(qRows, qMax)
	}
	return qRows, avail - len(qRows)
}

// truncatedRows clips rows to n, marking the cut with an ellipsis on the
// last kept row so a shortened question doesn't read as the whole one.
// The marker replaces a character rather than extending the row: these
// rows were wrapped to fit and must still fit.
func truncatedRows(rows []string, n int) []string {
	if n <= 0 {
		return nil
	}
	if len(rows) <= n {
		return rows
	}
	out := append([]string(nil), rows[:n]...)
	last := []rune(out[n-1])
	if len(last) > 1 {
		out[n-1] = strings.TrimRight(string(last[:len(last)-1]), " ") + "…"
	} else {
		out[n-1] = "…"
	}
	return out
}

// windowRows clips rows to at most n, keeping the row the caret is on
// visible and biasing toward the tail — where a caret that is simply
// typing forward lives. Returns the window and the caret's row inside it.
func windowRows(rows []string, caret, n int) ([]string, int) {
	if n <= 0 || len(rows) <= n {
		return rows, caret
	}
	start := caret - n + 1
	if start < 0 {
		start = 0
	}
	if start > len(rows)-n {
		start = len(rows) - n
	}
	return rows[start : start+n], caret - start
}

// answerWidth is the column budget the answer editor wraps to: the
// dialog width less the indent on both sides, so a long answer folds
// onto more rows instead of running off the right edge.
func answerWidth(width int) int {
	w := width - 2*len(answerIndent)
	if w < 8 {
		w = 8
	}
	return w
}

// answerRowsOf renders an answer editor's wrapped lines and its caret
// position within them.
func answerRowsOf(ed *tui.Editor, width int) (rows []string, caretRow, caretCol int) {
	if ed == nil {
		return []string{""}, 0, 0
	}
	return ed.Render(answerWidth(width))
}

// wrapPlain hard-wraps s to width columns on spaces (cheap; the question
// text is plain at this point so byte length tracks columns closely
// enough for the dialog).
func wrapPlain(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			if line == "" {
				line = word
			} else if len(line)+1+len(word) <= width {
				line += " " + word
			} else {
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}
