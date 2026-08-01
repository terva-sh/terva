package dialogs

import (
	"strconv"
	"strings"
	"sync"

	"github.com/mattn/go-runewidth"

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

	// confirmSkip is armed by an esc that would decline the ask, and
	// disarmed by any other key. Skipping is the one action here that
	// cannot be undone — the agent has already been told to proceed
	// without you by the time you notice — and esc is also the key
	// people press to back out of a text field, so a single press was
	// answering the whole ask by reflex. The second press is the
	// answer; the first one only says what it would do. Mirrors the
	// memory dialog's press-c-again clear.
	confirmSkip bool
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
	// noting is true when the open field is a NOTE on the highlighted
	// option rather than an answer replacing it. The two are separate
	// editors, not one with a flag: a user who types a custom answer,
	// goes back, and then annotates an option must not find the custom
	// text sitting in the note field, and a note must not be lost by
	// visiting the custom row.
	noting bool
	// note holds the addendum, and noteFor is the option index it was
	// written against. Bound to an option rather than to the question
	// because a note is about a specific choice — "Postgres, but watch
	// the connection limits" says nothing useful once the cursor moves
	// to SQLite, so it applies only while that option is the selection.
	note    *tui.Editor
	noteFor int
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

// optionIndent is the left pad an option row renders under.
// optionContIndent is the deeper pad a wrapped option's continuation
// rows take, so a fold reads as more of the same option rather than as
// the next one.
const (
	optionIndent     = "  "
	optionContIndent = "    "
)

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
	d.confirmSkip = false
	d.drafts = make([]questionDraft, len(req.Questions))
	for i, q := range req.Questions {
		d.drafts[i] = questionDraft{
			input:   tui.NewEditor(""),
			note:    tui.NewEditor(""),
			noteFor: -1,
			typing:  len(q.Options) == 0,
		}
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
	d.confirmSkip = false
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
	d.confirmSkip = false
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
			d.confirmSkip = false
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

	// Any key that is not another esc disarms a pending skip. A
	// confirmation that survives an arrow key confirms nothing.
	armed := d.confirmSkip
	if k.Kind != tui.KeyEsc {
		d.confirmSkip = false
	}

	// Esc declines the WHOLE ask, from any tab, so the agent always
	// unblocks. There is deliberately no per-question skip: a free-text
	// answer left empty is the way to decline just one.
	if k.Kind == tui.KeyEsc || k.Kind == tui.KeyCtrlC {
		// First, though: esc inside free-text entry is far more likely to
		// mean "get me out of this field" than "answer nothing at all".
		// When the question has options, that is where it goes — back to
		// the list it was reached from, cursor still on the custom row,
		// draft kept so returning resumes it. Without this the custom row
		// was a trapdoor: nothing on screen offered a way back, and the
		// key everyone tries first ended the ask.
		if k.Kind == tui.KeyEsc && !d.onSubmitTabLocked(req) {
			q := req.Questions[d.tab]
			if dr := &d.drafts[d.tab]; dr.typing && len(q.Options) > 0 {
				dr.typing, dr.noting = false, false
				d.confirmSkip = false
				d.mu.Unlock()
				return false
			}
		}
		// A bare esc arms; the second one skips. Ctrl+c is left unguarded
		// — it is the deliberate abort, and a dialog the agent is blocked
		// on must keep one key that always ends it.
		if k.Kind == tui.KeyEsc && !armed {
			d.confirmSkip = true
			d.mu.Unlock()
			return false
		}
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
		// alt+1..9 jumps straight to a question, so question 5 of 8 is one
		// keypress rather than four tabs. Alt-qualified because the bare
		// digits already pick an OPTION, and a dialog where 3 means one
		// thing in the list and another in the strip is worse than no
		// shortcut. It works from the answer field too — the editor never
		// sees an alt-digit — which is where the walk is most tedious.
		if k.Kind == tui.KeyRune && k.Alt && k.Rune >= '1' && k.Rune <= '9' {
			if n := int(k.Rune - '1'); n < len(req.Questions) {
				d.gotoTabLocked(n)
			}
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
		ed := dr.editor()
		// The editor owns every editing key, including the modified-Enter
		// newline chords; it reports submit only for a bare Enter.
		if !ed.HandleKey(k) {
			d.mu.Unlock()
			return false
		}
		// A note commits back to the option it annotates rather than
		// standing in for it, so the answer is resolved the same way the
		// review tab resolves it.
		if dr.noting {
			dr.typing = false
			if len(req.Questions) == 1 {
				ans := dr.answer(q)
				d.advance()
				d.mu.Unlock()
				req.Resp <- []core.UserAnswer{ans}
				return true
			}
			d.gotoTabLocked(d.tab + 1)
			d.mu.Unlock()
			return false
		}
		answer := strings.TrimSpace(ed.SubmitValue())
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

	// Digit shortcuts: 1-9 jump straight to that row, the way the confirm
	// gate's do. Free only in choice mode — the answer editor above owns
	// every rune while typing — and deliberately a JUMP, not a jump-and-
	// answer, so a mistyped digit costs a cursor move rather than an
	// answer sent. Enter is still the commit, from every route in.
	// A BARE digit. Alt qualifies the same digits for moving between
	// questions, so without this check alt+2 also picked option 2 — and
	// in a single-question ask, where there is nothing to jump to, that
	// was the only thing it did.
	if k.Kind == tui.KeyRune && !k.Alt && k.Rune >= '1' && k.Rune <= '9' {
		if idx := int(k.Rune - '1'); idx < pickable(q) {
			dr.cursor = idx
		}
		d.mu.Unlock()
		return false
	}

	// n annotates the highlighted option: "this one, but …". Not offered
	// on the custom row, which already IS free text — a note on a note is
	// nothing. The note is bound to the option it was written against, so
	// moving the cursor elsewhere leaves it behind rather than quietly
	// attaching it to a different answer.
	if k.Kind == tui.KeyRune && !k.Alt && (k.Rune == 'n' || k.Rune == 'N') && !customRow(q, dr.cursor) && len(q.Options) > 0 {
		if dr.noteFor != dr.cursor {
			dr.note = tui.NewEditor("")
			dr.noteFor = dr.cursor
		}
		dr.typing, dr.noting = true, true
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
			dr.typing, dr.noting = true, false
			d.mu.Unlock()
			return false
		}
		if len(req.Questions) == 1 {
			// Through answer(), not by reading q.Options here: this was a
			// second copy of the same resolution, and the copy is what
			// dropped the note — the review tab composed choice + note
			// while enter sent the bare option.
			ans := dr.answer(q)
			d.advance()
			d.mu.Unlock()
			req.Resp <- []core.UserAnswer{ans}
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
	// A note is an addendum to a choice, so it never puts the draft into
	// the free-text branch: the option is still the answer.
	if (dr.typing && !dr.noting) || len(q.Options) == 0 {
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
	return core.UserAnswer{Answer: q.Options[dr.cursor], Note: dr.noteText()}
}

// noteText is the note as it applies right now: empty unless one was
// written against the option the cursor is on. A note left behind on
// another option is kept in the draft — going back to that option brings
// it back — but it is not sent with a different answer.
func (dr questionDraft) noteText() string {
	if dr.note == nil || dr.noteFor != dr.cursor {
		return ""
	}
	return strings.TrimSpace(dr.note.SubmitValue())
}

// addendumFor is the text hanging under row i of the option list: the
// note written against that option, or the answer parked on the custom
// row. Both are KEPT when the user steps away, and both are now shown
// where they were left.
//
// They used to be invisible from the list — the note only rendered while
// its own option was selected, and a typed custom answer left no trace at
// all once you escaped back to the options. Text you wrote and cannot see
// reads as text you lost, which is the thing that makes someone type it
// twice. What applies at submit is a separate question, answered by
// noteText and by answer(); this is only about being able to see it.
func (dr questionDraft) addendumFor(q core.UserQuestion, i int) string {
	if customRow(q, i) {
		if dr.input == nil {
			return ""
		}
		return strings.TrimSpace(dr.input.SubmitValue())
	}
	if dr.note == nil || dr.noteFor != i {
		return ""
	}
	return strings.TrimSpace(dr.note.SubmitValue())
}

// editor is the field the open entry belongs to. Nil-safe: a draft that
// was never armed still has to take a keypress without panicking.
func (dr *questionDraft) editor() *tui.Editor {
	target := &dr.input
	if dr.noting {
		target = &dr.note
	}
	if *target == nil {
		*target = tui.NewEditor("")
	}
	return *target
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
		strip = append(strip, answerIndent+d.stripLocked(th, req, width), "")
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
	//
	// focus/focusEnd is that pointed-at row span: one row for the caret,
	// but every row of the highlighted option once options wrap.
	var (
		label    string
		listRows []string
		owner    []int // choice mode: the option each list row belongs to
		focus    int
		focusEnd int
	)
	if dr.typing {
		label = i18n.T("your answer:")
		field := dr.input
		if dr.noting {
			// Naming the option the note is attached to, because a note
			// field that says only "note:" leaves the user to remember
			// which row they were on — and the row is off screen now.
			label = i18n.T("note on “%s”:", clipLine(optionAt(q, dr.noteFor), max(width/2, 12)))
			field = dr.note
		}
		listRows, focus, caretCol = answerRowsOf(field, width)
		caretCol += len(answerIndent)
		focusEnd = focus + 1
	} else {
		label = i18n.T("choose an answer:")
		listRows, owner = optionRowsOf(q, width, dr)
		focus, focusEnd = ownerSpan(owner, dr.cursor)
	}

	// The keys sit under the list, where the submit tab has always put
	// them, rather than in the label above it. There are more of them than
	// one line above the options could hold without dropping the two —
	// tab and ctrl+u — that nothing else advertises.
	keyRows, keyColour := d.keyRows(req, q, dr, width), th.Muted
	if d.confirmSkip {
		// The warning takes the legend's place rather than a row of its
		// own: it appears on a keypress, and a body that grows under the
		// user shifts everything below it mid-read. It WRAPS where the
		// legend clips, because it is not chrome — it is the sentence
		// someone reads before doing the one irreversible thing here, and
		// a clip would take the consequence off the end of it.
		keyRows, keyColour = wrapIndented(d.skipWarning(len(req.Questions)), width), th.Warning
	}

	// A wrapped warning is several rows, and on a short terminal those
	// rows are not free: chrome that outgrows MaxRows cannot be paid for
	// by shrinking the list, which has a floor of its own. So the legend
	// yields too — last, after the question, and never below one row,
	// because the key it names is on that row. The ellipsis truncatedRows
	// leaves says the sentence was cut.
	if d.MaxRows > 0 {
		if room := max(d.MaxRows-(len(strip)+3)-2, 1); len(keyRows) > room {
			keyRows = truncatedRows(keyRows, room)
		}
	}

	// Everything in the body that is neither the question nor the list:
	// the strip and its blank, the blank under the question, the label,
	// the blank above the legend, and the legend itself.
	chrome := len(strip) + 3 + len(keyRows)
	qRows, budget := d.budgetLocked(qRows, len(listRows), chrome)
	start, end := windowSpan(len(listRows), focus, focusEnd, budget)
	listRows = listRows[start:end]
	if owner != nil {
		owner = owner[start:end]
	}
	focus -= start

	lines = append(lines, strip...)
	for _, wl := range qRows {
		lines = append(lines, "  "+th.FG256(th.Tool, wl))
	}
	lines = append(lines, "")
	lines = append(lines, "  "+th.FG256(th.Muted, clipLine(label, width-2)))
	listTop := len(lines)
	for i, l := range listRows {
		if dr.typing {
			lines = append(lines, answerIndent+l)
			continue
		}
		// The highlight follows the OPTION, not one row: a wrapped
		// option is selected across every row it folded onto.
		if i < len(owner) && owner[i] == dr.cursor {
			lines = append(lines, th.PadHighlight(l, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, l))
		}
	}
	lines = append(lines, "")
	for _, kr := range keyRows {
		lines = append(lines, th.FG256(keyColour, kr))
	}
	if !dr.typing {
		return lines, -1, -1
	}
	return lines, listTop + focus, caretCol
}

// keyRows is the legend under the list: what you can press, from here,
// right now. It is mode-specific because a key that does nothing in the
// mode you are in is worse than one you were never told about — it is
// the same reason the tab hints only appear for a set.
//
// One clipped row. The keys are ordered by how often they are wanted,
// so a clip on a narrow terminal takes esc (which every dialog here
// shares) before it takes tab (which nothing else advertises).
func (d *QuestionDialog) keyRows(req *QuestionRequest, q core.UserQuestion, dr questionDraft, width int) []string {
	var parts []string
	set := len(req.Questions) > 1
	switch {
	case dr.typing:
		if set {
			parts = append(parts, i18n.T("enter next"))
		} else {
			parts = append(parts, i18n.T("enter submits"))
		}
		parts = append(parts, i18n.T("ctrl+u clears"))
	default:
		if n := pickable(q); n > 0 {
			parts = append(parts, i18n.T("↑/↓ or 1-%d pick", n))
		} else {
			parts = append(parts, i18n.T("↑/↓ pick"))
		}
		if set {
			parts = append(parts, i18n.T("enter next"))
		} else {
			parts = append(parts, i18n.T("enter answers"))
		}
		// Only where it does something: the custom row is already free
		// text, so there is nothing there for a note to add.
		if len(q.Options) > 0 && !customRow(q, dr.cursor) {
			if dr.noteText() != "" {
				parts = append(parts, i18n.T("n edits note"))
			} else {
				parts = append(parts, i18n.T("n adds note"))
			}
		}
	}
	if set {
		// alt+N is named beside tab rather than on its own: they are the
		// same job, and the strip above already shows the numbers it
		// refers to.
		parts = append(parts, i18n.T("tab or alt+1-%d question", min(len(req.Questions), 9)))
	}
	switch {
	case dr.typing && len(q.Options) > 0:
		parts = append(parts, i18n.T("esc back to options"))
	case set:
		parts = append(parts, i18n.T("esc skips all"))
	default:
		parts = append(parts, i18n.T("esc skips"))
	}
	return []string{"  " + clipLine(strings.Join(parts, " · "), width-2)}
}

// wrapIndented folds s under the body's two-column indent, so a wrapped
// line stays inside the frame the indent narrowed.
func wrapIndented(s string, width int) []string {
	rows := wrapPlain(s, width-2)
	for i, r := range rows {
		rows[i] = "  " + r
	}
	return rows
}

// skipWarning is the line the first esc puts up in place of the legend. It
// names what would be skipped and what the agent does next, because
// "skipped" on its own reads as "nothing happens" — the agent in fact
// proceeds on its own judgment, which is the part worth a second press.
// It also says how to get OUT of it. The warning takes the key legend's
// row, so while it is up the only keys named on screen are the one that
// skips — and someone who hit esc by reflex needs the other one. "Any
// other key" rather than a specific key because that is literally the
// rule, and naming one would imply the rest do something else.
func (d *QuestionDialog) skipWarning(n int) string {
	if n > 1 {
		return i18n.T("esc again skips ALL %d questions — the agent proceeds without your answers · any other key carries on", n)
	}
	return i18n.T("esc again skips this question — the agent proceeds without your answer · any other key carries on")
}

// stripLocked renders the tab strip: one numbered chip per question,
// ticked once it has an answer, then the submit chip. Numbers rather
// than truncated question text — a question clipped to eight columns
// tells you less than its position does, and numbers never overflow.
// Caller holds mu.
func (d *QuestionDialog) stripLocked(th tui.Theme, req *QuestionRequest, width int) string {
	// Three tiers, most informative first: name every question, then name
	// only the one you are on, then bare numbers. A tier is used only if
	// it FITS — a slug clipped to eight columns tells you less than the
	// position does, which is why this was numbers-only to begin with.
	// Questions without a slug stay numbers in every tier; the model is
	// not obliged to name them, and one named chip beside three numbered
	// ones still says more than four numbers.
	for _, named := range []func(i int) bool{
		func(int) bool { return true },
		func(i int) bool { return i == d.tab },
		func(int) bool { return false },
	} {
		styled, plain := d.stripTextLocked(th, req, named)
		if runewidth.StringWidth(plain) <= width-len(answerIndent) {
			return styled
		}
	}
	// Even bare numbers overflow (a set of eight on a very narrow
	// terminal). Clip the PLAIN text rather than break the frame: clipping
	// a styled string can cut an escape sequence in half.
	_, plain := d.stripTextLocked(th, req, func(int) bool { return false })
	return clipLine(plain, width-len(answerIndent))
}

// stripTextLocked builds the strip, naming the chips `named` selects, and
// returns it both styled and plain.
//
// Plain is not "the styled one with the theme left zero": a zero theme
// still emits escape sequences, and measuring those as if they were
// visible columns rejected every tier that actually fit. The width test
// above needs text with no escapes in it at all, so both are built here
// in one pass rather than derived from each other. Caller holds mu.
func (d *QuestionDialog) stripTextLocked(th tui.Theme, req *QuestionRequest, named func(i int) bool) (styled, plain string) {
	var s, p strings.Builder
	write := func(raw, coloured string) {
		p.WriteString(raw)
		s.WriteString(coloured)
	}
	for i, q := range req.Questions {
		if i > 0 {
			write("  ", "  ")
		}
		chip := strconv.Itoa(i + 1)
		if slug := core.SanitizeSlug(q.Slug); slug != "" && named(i) {
			chip += " " + slug
		}
		if d.drafts[i].visited && !d.drafts[i].answer(q).Declined {
			chip += "✓"
		}
		if i == d.tab {
			write("▸"+chip, th.FG256(th.Accent, "▸"+chip))
		} else {
			write(" "+chip, " "+th.FG256(th.Muted, chip))
		}
	}
	write("  │ ", th.FG256(th.Muted, "  │ "))
	submit := i18n.T("submit")
	if d.onSubmitTabLocked(req) {
		write("▸"+submit, th.FG256(th.Accent, "▸"+submit))
	} else {
		write(" "+submit, " "+th.FG256(th.Muted, submit))
	}
	return s.String(), p.String()
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
		// The arrow marks the answer once; a wrapped one continues under
		// it rather than sprouting a second arrow mid-sentence.
		for n, wl := range wrapPlain(label, width-8) {
			pad := "     → "
			if n > 0 {
				pad = "       "
			}
			lines = append(lines, pad+th.FG256(colour, wl))
		}
		// The note is listed under its answer, not merged into it: this
		// tab is where the set is checked before it is sent, and merging
		// them would hide the one part the user wrote by hand.
		for _, wl := range wrapPlain(ans.Note, width-10) {
			if ans.Note == "" {
				break
			}
			lines = append(lines, "       ↳ "+th.FG256(th.Muted, wl))
		}
	}
	// Same substitution as the question tabs: esc arms here too, so the
	// warning replaces the hint rather than being one the review tab
	// never shows. Wrapped, and counted against the budget, for the same
	// reason it is over there.
	hintRows := []string{"  " + clipLine(i18n.T("enter sends · tab/⇧tab to revisit · esc skips all"), width-2)}
	hintColour := th.Muted
	if d.confirmSkip {
		hintRows, hintColour = wrapIndented(d.skipWarning(len(req.Questions)), width), th.Warning
	}
	// The legend yields under pressure here too, and for the reason it
	// does on the question tabs: a wrapped warning is several rows, and
	// clipping only the review below it cannot pay for chrome that has
	// already outgrown MaxRows. Reserve the blank and one review row.
	if d.MaxRows > 0 {
		if room := max(d.MaxRows-chrome-2, 1); len(hintRows) > room {
			hintRows = truncatedRows(hintRows, room)
		}
	}
	// Budget the review against the same MaxRows the question tabs use:
	// the strip above it and the blank + hint below it are already spent.
	if avail := d.MaxRows - chrome - 1 - len(hintRows); d.MaxRows > 0 && len(lines) > avail {
		lines = truncatedRows(lines, max(avail, 1))
	}
	lines = append(lines, "")
	for _, hr := range hintRows {
		lines = append(lines, th.FG256(hintColour, hr))
	}
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
// the caret or the selection always has somewhere to sit.
//
// chrome is every body row that is neither the question nor the list —
// the tab strip, the blanks, the label, the key legend. It used to be a
// partial count with the rest hardcoded here, which silently overran the
// budget by a row the first time one of those wrapped. Caller holds mu.
func (d *QuestionDialog) budgetLocked(qRows []string, listRows, chrome int) (question []string, listBudget int) {
	if d.MaxRows <= 0 || listRows <= 0 {
		return qRows, listRows
	}
	avail := d.MaxRows - chrome
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

// windowSpan clips a list of total rows to at most n, keeping the rows
// in [from,to) visible — the caret's row while typing, every row of the
// highlighted option while choosing — and biasing toward the tail,
// where a caret typing forward and a selection walking down both live.
// A span taller than the window shows its head. Returns the window's
// bounds, so a caller windowing a parallel slice can cut it alike.
func windowSpan(total, from, to, n int) (start, end int) {
	if n <= 0 || total <= n {
		return 0, total
	}
	if to > n {
		start = to - n
	}
	if start > from {
		start = from
	}
	if start > total-n {
		start = total - n
	}
	if start < 0 {
		start = 0
	}
	return start, start + n
}

// optionWidth is the column budget one option's text wraps to: the
// dialog width less the frame margin and the deepest indent an option
// row carries, so a continuation row still fits inside the frame.
func optionWidth(width int) int {
	w := width - 2 - len(optionContIndent)
	if w < 8 {
		w = 8
	}
	return w
}

// optionRowsOf renders q's selectable rows, folding any option wider
// than the dialog onto continuation rows, and reports which option each
// rendered row belongs to.
//
// Options used to be truncated at the right edge, which hid the tail of
// exactly the long answers a user most needs to read before choosing —
// and unlike a truncated question, there was no way to see the rest.
// The question text and the typed answer already wrap; the option list
// wraps the same way, and the highlight covers every row of the
// selected option so a fold still reads as one choice.
func optionRowsOf(q core.UserQuestion, width int, dr questionDraft) (rows []string, owner []int) {
	all := questionRows(q)
	// Every row is numbered, and 1-9 of them are also shortcuts. Numbering
	// past 9 rather than stopping there because the number is a LABEL
	// first — it is how the row below the fold is referred to, and a list
	// that stops counting halfway reads as broken rather than as a hint
	// about which ones are typeable.
	lead := len(strconv.Itoa(len(all))) + 2 // "12. "
	limit := optionWidth(width) - lead
	if limit < 8 {
		limit = 8
	}
	for i, row := range all {
		num := strconv.Itoa(i + 1)
		for n, wl := range wrapPlain(row, limit) {
			// Continuations align under the text, not under the number:
			// a fold that lines up with the digits reads as another
			// option whose number went missing.
			pad := optionIndent + strings.Repeat(" ", lead)
			if n == 0 {
				pad = optionIndent + num + "." + strings.Repeat(" ", lead-len(num)-1)
			}
			rows = append(rows, pad+wl)
			owner = append(owner, i)
		}
		// A note — or a parked custom answer — rides under the row it
		// belongs to, owned by it, so it travels with the selection
		// through the window and wears the same highlight. A row the
		// user has written something against must not look identical to
		// one they merely moved past.
		if add := dr.addendumFor(q, i); add != "" {
			for n, wl := range wrapPlain(add, limit-2) {
				mark := "↳ "
				if n > 0 {
					mark = "  "
				}
				rows = append(rows, optionIndent+strings.Repeat(" ", lead)+mark+wl)
				owner = append(owner, i)
			}
		}
	}
	return rows, owner
}

// optionAt names option i, or "" when i is out of range — the label the
// note field puts in its own heading.
func optionAt(q core.UserQuestion, i int) string {
	rows := questionRows(q)
	if i < 0 || i >= len(rows) {
		return ""
	}
	return rows[i]
}

// pickable is how many of q's rows have a digit shortcut: all of them up
// to 9. Zero when the question has no rows at all, so the legend can say
// "↑/↓ pick" rather than promising a range that does not exist.
func pickable(q core.UserQuestion) int {
	return min(len(questionRows(q)), 9)
}

// ownerSpan reports the row range [from,to) that option occupies in the
// rows optionRowsOf returned. An option with no rows (an out-of-range
// cursor) windows from the top rather than reporting a bogus span.
func ownerSpan(owner []int, option int) (from, to int) {
	from = -1
	for i, o := range owner {
		if o != option {
			continue
		}
		if from < 0 {
			from = i
		}
		to = i + 1
	}
	if from < 0 {
		return 0, 0
	}
	return from, to
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

// wrapPlain folds s to width COLUMNS, one paragraph per newline, using
// the same visible-width wrap the rest of the TUI folds with.
//
// It used to count bytes and break only on spaces, which is wrong in
// the two ways that matter to text a model wrote: multibyte runes
// measured wide and wrapped early, and a single unbroken token — a
// path, a URL, an id — was emitted whole and ran off the right edge no
// matter how narrow the frame. tui.WrapANSILine measures columns and
// splits an over-long token, so every row it returns fits.
func wrapPlain(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		out = append(out, tui.WrapANSILine(para, width)...)
	}
	return out
}
