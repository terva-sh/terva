package dialogs

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// CopyDialog is the two-stage picker behind /copy and ctrl+y.
//
// Stage one lists turns, because that is how a person remembers an
// exchange: "the one where I asked about the wrap bug". Stage two lists
// the PARTS of that turn -- each paragraph, fence, list and table
// separately -- because the common need is a piece out of the body of a
// reply, not the whole reply.
//
// It is an overlay rather than a cursor that walks the scrollback. The
// main screen cannot edit emitted rows, so an in-place cursor would have
// to Clear() and replay the entire transcript on every move. A dialog
// draws over it and costs nothing per keystroke.
//
// What lands on the clipboard is markdown source, sliced from the
// original message by tui.SplitBlocks. Never the painted text: that
// carries wrap points, gutters and ANSI that paste as garbage.
type CopyDialog struct {
	active bool
	stage  copyStage

	msgs []provider.Message

	// Stage one.
	turns       []jumpTarget
	turnFilter  string
	visibleTurn []jumpTarget

	// Stage two, rebuilt on every descent.
	turnNo      int
	parts       []Part
	partFilter  string
	visiblePart []Part
	ordinals    map[int]int // MsgIdx -> 1-based reply ordinal, when a turn has several

	cursor int
	vp     Viewport
	// MaxRows is the body height the host budgets from the terminal
	// (dialogs.BodyBudget). 0 falls back to copyFallbackRows.
	MaxRows int
}

type copyStage int

const (
	copyStageTurns copyStage = iota
	copyStageParts
)

// copyDialogAction is returned by HandleKey. Text is the exact markdown
// to put on the clipboard; the caller owns the clipboard mechanism
// (OSC 52 or a local helper) and the notice it shows afterwards.
type copyDialogAction struct {
	Copy   bool
	Text   string
	Kind   tui.BlockKind
	Whole  bool // the turn's whole reply rather than one part
	TurnNo int
	Close  bool
}

const (
	copyFallbackRows = 12
	// copyPreviewRows is the height of the pane that shows the
	// highlighted part in full. Three lines is enough to tell two
	// similar paragraphs apart, which is the only job it has.
	copyPreviewRows = 3
)

func NewCopyDialog() *CopyDialog { return &CopyDialog{} }

// Open builds the turn list and shows the picker. msgs is the slice the
// chat paints, so a MessageIdx here means what it means for /jump.
func (d *CopyDialog) Open(msgs []provider.Message, initialFilter string) {
	d.msgs = msgs
	// buildJumpTargets, not a second walk of the transcript: it already
	// filters machine-authored RoleUser messages through core.IsUserTurn
	// and numbers turns accordingly. A private copy here would drift and
	// misnumber turns exactly as the jump picker once did.
	d.turns = buildJumpTargets(msgs)
	d.stage = copyStageTurns
	d.turnFilter = initialFilter
	d.partFilter = ""
	d.cursor = 0
	d.applyFilter()
	d.active = true
}

func (d *CopyDialog) Close()       { d.active = false }
func (d *CopyDialog) Active() bool { return d != nil && d.active }

// applyFilter recomputes the visible slice for the current stage.
// Matching is case-insensitive substring, over the preview plus the kind
// and role names, so "fence" or "think" narrow the list as readily as a
// word from the text does.
func (d *CopyDialog) applyFilter() {
	if d.stage == copyStageTurns {
		d.visibleTurn = d.visibleTurn[:0]
		q := strings.ToLower(d.turnFilter)
		for _, t := range d.turns {
			if q == "" || strings.Contains(strings.ToLower(t.Preview), q) {
				d.visibleTurn = append(d.visibleTurn, t)
			}
		}
		d.clampCursor(len(d.visibleTurn))
		return
	}
	d.visiblePart = d.visiblePart[:0]
	q := strings.ToLower(d.partFilter)
	for _, p := range d.parts {
		if q == "" || strings.Contains(strings.ToLower(partHaystack(p)), q) {
			d.visiblePart = append(d.visiblePart, p)
		}
	}
	d.clampCursor(len(d.visiblePart))
}

func partHaystack(p Part) string {
	return p.Preview + " " + p.Kind.String() + " " + p.Role.String()
}

func (d *CopyDialog) clampCursor(n int) {
	if d.cursor >= n {
		d.cursor = n - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
}

// descend moves to stage two for the highlighted turn.
func (d *CopyDialog) descend() {
	if len(d.visibleTurn) == 0 {
		return
	}
	t := d.visibleTurn[d.cursor]
	d.turnNo = t.TurnNo
	d.parts = partsForTurn(d.msgs, t.MessageIdx)
	d.ordinals = replyOrdinals(d.parts)
	d.stage = copyStageParts
	d.partFilter = ""
	d.cursor = 0
	d.vp.Reset()
	d.applyFilter()
}

// ascend returns to stage one, restoring the turn filter that was in
// force. Escape backs out one level rather than closing, so a wrong
// descent costs one keystroke instead of reopening the dialog.
func (d *CopyDialog) ascend() {
	d.stage = copyStageTurns
	d.cursor = 0
	d.vp.Reset()
	d.applyFilter()
}

// partsForTurn collects the copyable parts of one turn: the user's
// prompt, any recorded thinking, and the assistant's prose. It stops at
// the next real user turn.
//
// Tool calls and tool results are skipped entirely. They are the bulk of
// a transcript and nobody copies them out of a picker; offering them
// would bury the three things people do copy.
func partsForTurn(msgs []provider.Message, from int) []Part {
	if from < 0 || from >= len(msgs) {
		return nil
	}
	var out []Part
	out = append(out, partsOfMessage(msgs[from], from, RoleUser)...)
	for j := from + 1; j < len(msgs); j++ {
		if core.IsUserTurn(msgs[j]) {
			break
		}
		if msgs[j].Role != provider.RoleAssistant {
			continue // tool results
		}
		out = append(out, partsOfMessage(msgs[j], j, RoleReply)...)
	}
	return out
}

// partsOfMessage splits one message's text blocks, and its reasoning
// when present. fallback is the role for text blocks: a user message
// yields user parts, an assistant message reply parts.
func partsOfMessage(m provider.Message, idx int, fallback PartRole) []Part {
	var out []Part
	for _, c := range m.Content {
		switch b := c.(type) {
		case provider.ReasoningBlock:
			// Only recorded thinking can be copied. A provider that
			// sends no summary, or a session saved before thinking was
			// retained, simply contributes no thinking parts.
			if b.Summary != "" {
				out = append(out, SplitParts(RoleThinking, idx, b.Summary)...)
			}
		case provider.TextBlock:
			out = append(out, SplitParts(fallback, idx, b.Text)...)
		}
	}
	return out
}

// replyOrdinals numbers the assistant messages of a turn, but only when
// there are several. A turn broken by tool work produces two or three
// separate replies, and without a number the rows read as one run of
// prose from a single answer.
func replyOrdinals(parts []Part) map[int]int {
	var order []int
	seen := map[int]bool{}
	for _, p := range parts {
		if p.Role != RoleReply || seen[p.MsgIdx] {
			continue
		}
		seen[p.MsgIdx] = true
		order = append(order, p.MsgIdx)
	}
	if len(order) < 2 {
		return nil
	}
	out := make(map[int]int, len(order))
	for i, idx := range order {
		out[idx] = i + 1
	}
	return out
}

// wholeReply joins every reply part of the turn back into one markdown
// document. This is the "I did want the whole thing" escape hatch, and
// it reproduces what /copy last gives for the newest turn.
func wholeReply(parts []Part) string {
	var chunks []string
	for _, p := range parts {
		if p.Role == RoleReply {
			chunks = append(chunks, p.Text)
		}
	}
	return strings.Join(chunks, "\n\n")
}

// ChromeRows is the non-body rows Render emits at their worst case.
// Verified by TestEveryDialogFitsItsOwnBudget rather than counted by eye.
// Stage two costs more because of the preview pane and its rule; the
// host recomputes the budget every frame, so the two answers are safe.
func (d *CopyDialog) ChromeRows() int {
	if d != nil && d.stage == copyStageParts {
		return 5 + 1 + copyPreviewRows
	}
	return 5
}

func (d *CopyDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	if d.stage == copyStageParts {
		return d.renderParts(th, width)
	}
	return d.renderTurns(th, width)
}

func (d *CopyDialog) renderTurns(th tui.Theme, width int) []string {
	var lines []string
	lines = append(lines, FrameHeader(th, i18n.T("copy"), width))
	if len(d.turns) == 0 {
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("nothing to copy yet — this session has no turns")))
		lines = append(lines, FrameRule(th, width))
		return lines
	}
	hint := i18n.T("↑/↓ pick - enter open turn - ctrl+y whole reply - esc cancel")
	if d.turnFilter != "" {
		hint = i18n.T("filter: %q - %d match - ", d.turnFilter, len(d.visibleTurn)) + hint
	}
	lines = append(lines, th.FG256(th.Muted, hint))
	if len(d.visibleTurn) == 0 {
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("(nothing matches; backspace to widen)")))
		lines = append(lines, FrameRule(th, width))
		return lines
	}
	body := make([]string, len(d.visibleTurn))
	for i, t := range d.visibleTurn {
		body[i] = "  " + formatJumpRowPlain(t, width-2)
	}
	lines = append(lines, d.windowed(th, body, width)...)
	lines = append(lines, FrameRule(th, width))
	return lines
}

func (d *CopyDialog) renderParts(th tui.Theme, width int) []string {
	var lines []string
	lines = append(lines, FrameHeader(th, i18n.T("copy - turn %d", d.turnNo), width))
	if len(d.parts) == 0 {
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("nothing to copy in this turn — it is all tool work")))
		lines = append(lines, FrameRule(th, width))
		return lines
	}
	hint := i18n.T("↑/↓ pick - enter copy - ctrl+y whole reply - esc back")
	if d.partFilter != "" {
		hint = i18n.T("filter: %q - %d match - ", d.partFilter, len(d.visiblePart)) + hint
	}
	lines = append(lines, th.FG256(th.Muted, hint))
	if len(d.visiblePart) == 0 {
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("(nothing matches; backspace to widen)")))
		lines = append(lines, FrameRule(th, width))
		return lines
	}
	body := make([]string, len(d.visiblePart))
	for i, p := range d.visiblePart {
		body[i] = "  " + formatCopyPartRow(p, d.ordinals[p.MsgIdx], width-2)
	}
	lines = append(lines, d.windowed(th, body, width)...)

	// The preview pane: what you are about to copy, in full, so two
	// similar paragraphs are told apart before the clipboard is set.
	lines = append(lines, FrameRule(th, width))
	for _, l := range previewPaneRows(d.visiblePart[d.cursor].Text, width-4, copyPreviewRows) {
		lines = append(lines, th.FG256(th.Muted, "  "+l))
	}
	lines = append(lines, FrameRule(th, width))
	return lines
}

// windowed renders the cursor's neighbourhood of body with the standard
// more-above / more-below markers.
func (d *CopyDialog) windowed(th tui.Theme, body []string, width int) []string {
	maxRows := d.MaxRows
	if maxRows <= 0 {
		maxRows = copyFallbackRows
	}
	d.vp.Fit(len(body), maxRows)
	// Centred, matching /jump: the list is rebuilt under the cursor as
	// the filter changes, and moving the content under a still cursor
	// reads better than scrolling only at the edges.
	d.vp.Center(d.cursor)
	start, end := d.vp.Window()
	var out []string
	if start > 0 {
		out = append(out, WindowMoreAbove(th, start))
	}
	for i := start; i < end; i++ {
		if i == d.cursor {
			out = append(out, th.PadHighlight(body[i], width))
		} else {
			out = append(out, th.FG256(th.Muted, body[i]))
		}
	}
	if end < len(body) {
		out = append(out, WindowMoreBelow(th, len(body), end))
	}
	return out
}

// formatCopyPartRow renders one part as a single line: role, an ordinal
// when the turn had several replies, a sigil for the kind, and the
// preview trimmed to fit.
func formatCopyPartRow(p Part, ordinal, maxWidth int) string {
	role := roleLabel(p.Role)
	if ordinal > 0 && p.Role == RoleReply {
		role = fmt.Sprintf("%s %d", role, ordinal)
	}
	left := fmt.Sprintf("%-9s %s  ", role, kindSigil(p.Kind))
	room := maxWidth - len([]rune(left))
	if room < 10 {
		room = 10
	}
	return left + truncateRunes(p.Preview, room)
}

// roleLabel is shown in the picker, so it is translated. PartRole.String
// stays an untranslated identifier for tests and logs.
func roleLabel(r PartRole) string {
	switch r {
	case RoleUser:
		return i18n.T("you")
	case RoleThinking:
		return i18n.T("think")
	default:
		return i18n.T("reply")
	}
}

// kindSigil is a one-rune badge for the block kind, so the eye can find
// the code in a list of prose without reading any of it.
func kindSigil(k tui.BlockKind) string {
	switch k {
	case tui.BlockHeading:
		return "❯"
	case tui.BlockList:
		return "⌗"
	case tui.BlockFence:
		return "▭"
	case tui.BlockTable:
		return "▤"
	case tui.BlockQuote:
		return "❝"
	default:
		return "¶"
	}
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if max <= 0 {
		return ""
	}
	if len(r) <= max {
		return s
	}
	if max == 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// previewPaneRows hard-wraps text into at most maxRows lines. It is a
// plain wrap on runes: the pane shows source, and source is what will be
// copied, so styling it would misrepresent the payload.
func previewPaneRows(text string, width, maxRows int) []string {
	if width <= 0 || maxRows <= 0 {
		return nil
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if len(out) >= maxRows {
			return out
		}
		r := []rune(line)
		if len(r) == 0 {
			out = append(out, "")
			continue
		}
		for len(r) > 0 && len(out) < maxRows {
			n := width
			if n > len(r) {
				n = len(r)
			}
			out = append(out, string(r[:n]))
			r = r[n:]
		}
	}
	return out
}

// HandleKey advances the dialog and returns an action to apply. Runes
// extend the filter of the current stage, which is why copy-whole is
// ctrl+y and not a bare "y": every printable key belongs to the filter.
func (d *CopyDialog) HandleKey(k tui.Key) copyDialogAction {
	n := d.visibleLen()
	switch k.Kind {
	case tui.KeyUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown:
		if d.cursor < n-1 {
			d.cursor++
		}
	case tui.KeyPageUp:
		d.cursor -= 5
		if d.cursor < 0 {
			d.cursor = 0
		}
	case tui.KeyPageDown:
		d.cursor += 5
		if d.cursor >= n {
			d.cursor = n - 1
		}
		if d.cursor < 0 {
			d.cursor = 0
		}
	case tui.KeyBackspace:
		d.trimFilter()
	case tui.KeyRune:
		d.extendFilter(k.Rune)
	case tui.KeyCtrlY:
		return d.copyWhole()
	case tui.KeyEsc:
		if d.stage == copyStageParts {
			d.ascend()
			return copyDialogAction{}
		}
		d.Close()
		return copyDialogAction{Close: true}
	case tui.KeyEnter:
		if d.stage == copyStageTurns {
			d.descend()
			return copyDialogAction{}
		}
		if len(d.visiblePart) == 0 {
			return copyDialogAction{}
		}
		p := d.visiblePart[d.cursor]
		d.Close()
		return copyDialogAction{Copy: true, Text: p.Text, Kind: p.Kind, TurnNo: d.turnNo}
	}
	return copyDialogAction{}
}

// copyWhole takes the whole reply of the turn under the cursor, from
// either stage, and closes.
func (d *CopyDialog) copyWhole() copyDialogAction {
	parts, turnNo := d.parts, d.turnNo
	if d.stage == copyStageTurns {
		if len(d.visibleTurn) == 0 {
			return copyDialogAction{}
		}
		t := d.visibleTurn[d.cursor]
		parts, turnNo = partsForTurn(d.msgs, t.MessageIdx), t.TurnNo
	}
	text := wholeReply(parts)
	if text == "" {
		return copyDialogAction{}
	}
	d.Close()
	return copyDialogAction{Copy: true, Text: text, Whole: true, TurnNo: turnNo}
}

func (d *CopyDialog) visibleLen() int {
	if d.stage == copyStageParts {
		return len(d.visiblePart)
	}
	return len(d.visibleTurn)
}

func (d *CopyDialog) extendFilter(r rune) {
	if d.stage == copyStageParts {
		d.partFilter += string(r)
	} else {
		d.turnFilter += string(r)
	}
	d.applyFilter()
}

func (d *CopyDialog) trimFilter() {
	cur := d.turnFilter
	if d.stage == copyStageParts {
		cur = d.partFilter
	}
	if cur == "" {
		return
	}
	runes := []rune(cur)
	cur = string(runes[:len(runes)-1])
	if d.stage == copyStageParts {
		d.partFilter = cur
	} else {
		d.turnFilter = cur
	}
	d.applyFilter()
}
