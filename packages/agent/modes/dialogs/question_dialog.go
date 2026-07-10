package dialogs

import (
	"strings"
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// QuestionRequest is one pending ask_user_question. The agent goroutine
// enqueues it and blocks on resp until the user answers or the turn is
// cancelled. Mirrors confirmRequest.
type QuestionRequest struct {
	Question    string
	Options     []string
	AllowCustom bool
	Resp        chan core.UserAnswer
}

// QuestionDialog renders the ask_user_question prompt: a multiple-choice
// list when options were given, plus a free-text entry when there are no
// options or allow_custom is set. It is the question analog of
// confirmDialog and is an agent-blocking overlay (the tool call waits on
// the answer).
type QuestionDialog struct {
	mu      sync.Mutex
	pending []*QuestionRequest
	cursor  int
	// typing is true while the user is entering a free-text answer; input
	// holds the buffer. Entered either directly (no options) or by
	// selecting the "type my own answer" row (allow_custom).
	typing bool
	input  []rune
}

func NewQuestionDialog() *QuestionDialog { return &QuestionDialog{} }

// rows returns the selectable rows for the head request: one per option,
// plus a trailing "type my own answer" row when allow_custom is set.
func (d *QuestionDialog) rows(req *QuestionRequest) []string {
	rows := append([]string(nil), req.Options...)
	if req.AllowCustom {
		rows = append(rows, i18n.T("Type my own answer…"))
	}
	return rows
}

func (d *QuestionDialog) Enqueue(req *QuestionRequest) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pending = append(d.pending, req)
	if len(d.pending) == 1 {
		d.reset(req)
	}
}

// reset positions the cursor/typing state for a newly-active request. A
// question with no options goes straight to free-text entry. Caller holds mu.
func (d *QuestionDialog) reset(req *QuestionRequest) {
	d.cursor = 0
	d.input = nil
	d.typing = len(req.Options) == 0
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
	d.mu.Unlock()
	for _, req := range pending {
		select {
		case req.Resp <- core.UserAnswer{Declined: true}:
		default:
		}
	}
}

// advance pops the head request and arms the next one. Caller holds mu.
func (d *QuestionDialog) advance() {
	d.pending = d.pending[1:]
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
		if idx == 0 && len(d.pending) > 0 {
			d.reset(d.pending[0])
		}
		return
	}
}

// HandleKey advances selection / edits the free-text buffer / resolves
// the dialog. Returns true when an answer was sent back to the agent.
func (d *QuestionDialog) HandleKey(k tui.Key) bool {
	d.mu.Lock()
	if len(d.pending) == 0 {
		d.mu.Unlock()
		return false
	}
	req := d.pending[0]

	// Esc declines in either mode so the agent always unblocks.
	if k.Kind == tui.KeyEsc || k.Kind == tui.KeyCtrlC {
		d.advance()
		d.mu.Unlock()
		req.Resp <- core.UserAnswer{Declined: true}
		return true
	}

	if d.typing {
		switch k.Kind {
		case tui.KeyEnter:
			answer := strings.TrimSpace(string(d.input))
			if answer == "" {
				// Empty submission: keep the prompt open rather than
				// sending a blank answer.
				d.mu.Unlock()
				return false
			}
			d.advance()
			d.mu.Unlock()
			req.Resp <- core.UserAnswer{Answer: answer}
			return true
		case tui.KeyBackspace:
			if n := len(d.input); n > 0 {
				d.input = d.input[:n-1]
			}
			d.mu.Unlock()
			return false
		case tui.KeyRune:
			d.input = append(d.input, k.Rune)
			d.mu.Unlock()
			return false
		}
		d.mu.Unlock()
		return false
	}

	rows := d.rows(req)
	switch k.Kind {
	case tui.KeyUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown:
		if d.cursor < len(rows)-1 {
			d.cursor++
		}
	case tui.KeyEnter:
		// The trailing row is the custom-answer affordance when allowed.
		if req.AllowCustom && d.cursor == len(rows)-1 {
			d.typing = true
			d.mu.Unlock()
			return false
		}
		answer := req.Options[d.cursor]
		d.advance()
		d.mu.Unlock()
		req.Resp <- core.UserAnswer{Answer: answer}
		return true
	}
	d.mu.Unlock()
	return false
}

// Render draws the dialog for the head request.
func (d *QuestionDialog) Render(th tui.Theme, width int) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.pending) == 0 {
		return nil
	}
	req := d.pending[0]

	var lines []string
	lines = append(lines, FrameHeader(th, i18n.T("question"), width))
	for _, wl := range wrapPlain(req.Question, width-2) {
		lines = append(lines, "  "+th.FG256(th.Tool, wl))
	}
	lines = append(lines, "")

	if d.typing {
		lines = append(lines, th.FG256(th.Muted, i18n.T("type your answer, enter to submit, esc to skip:")))
		lines = append(lines, "  "+string(d.input)+th.FG256(th.Accent, "█"))
		lines = append(lines, FrameRule(th, width))
		return lines
	}

	lines = append(lines, th.FG256(th.Muted, i18n.T("choose (↑/↓, enter to pick, esc to skip):")))
	for i, row := range d.rows(req) {
		plain := "  " + row
		if visibleLen(plain) > width-2 && width > 5 {
			plain = plain[:width-5] + "..."
		}
		if i == d.cursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, plain))
		}
	}
	lines = append(lines, FrameRule(th, width))
	return lines
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
