package dialogs

import (
	"fmt"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// JumpPurpose says why the picker is open, and therefore which index domain the
// slice handed to Open belongs to. It travels back out on the selection, so the
// caller acts on the domain it opened with rather than on separately-held state.
//
// The two are not interchangeable. /jump is opened with what the chat PAINTS —
// history paged back in through a compaction divider, prepended, and tool-image
// mirrors dropped. /fork is opened with the effective transcript, which has
// neither. The same conversation gives a message different indices in each, so
// the pairing of "which slice" with "what to do about the pick" is a correctness
// property, not a convenience.
type JumpPurpose int

const (
	// JumpScroll scrolls the chat viewport to the picked turn.
	JumpScroll JumpPurpose = iota
	// JumpFork branches the session at the picked turn.
	JumpFork
)

// jumpTarget describes one "turn" in the current session — a user
// prompt plus the preview metadata the picker renders. MessageIdx
// indexes THE SLICE PASSED TO Open, which differs by purpose; see
// JumpPurpose. For JumpScroll that resolves to a row via
// view.BuildWithAnchors, for JumpFork to a BranchSession cut point.
type jumpTarget struct {
	MessageIdx int    // index into the slice Open was given
	TurnNo     int    // 1-based turn number in session order
	Preview    string // first ~60 chars of the user prompt (one line)
	ToolCount  int    // tools invoked by the assistant in this turn
}

// JumpDialog is the picker shown when the user runs /jump. Rows are
// turns: one per user message. Filtering happens as the user types;
// arrow keys move within the filtered set.
type JumpDialog struct {
	active  bool
	purpose JumpPurpose
	all     []jumpTarget
	visible []jumpTarget // filtered subset
	cursor  int
	vp      Viewport
	// MaxRows is the body height the host budgets from the terminal
	// (dialogs.BodyBudget). 0 falls back to jumpFallbackRows.
	MaxRows int
	filter  string
}

// jumpDialogAction is returned by HandleKey. Purpose is the one Open was given,
// carried back so the consumer cannot pair a MessageIdx with an action from the
// other index domain.
type jumpDialogAction struct {
	Select     bool
	Purpose    JumpPurpose
	MessageIdx int
	TurnNo     int
	Close      bool
}

func NewJumpDialog() *JumpDialog { return &JumpDialog{} }

// Open scans the current transcript for user-message anchor points,
// applies the optional initial filter, and makes the dialog visible.
// If the filter already narrows the list to exactly one match the
// caller should check len(d.visible)==1 and treat that as an
// immediate select rather than opening the full picker.
//
// purpose is taken here rather than held by the caller because msgs and purpose
// have to agree: the indices this produces mean nothing without knowing which
// slice they index. See JumpPurpose.
func (d *JumpDialog) Open(msgs []provider.Message, initialFilter string, purpose JumpPurpose) {
	d.all = buildJumpTargets(msgs)
	d.purpose = purpose
	d.filter = initialFilter
	d.applyFilter()
	// Start on the last (most recent) target so enter-without-typing
	// goes to the newest turn, which is almost never what you want
	// for /jump — flip to the oldest filtered match instead so the
	// picker opens at the top of the list.
	d.cursor = 0
	d.active = true
}

// Close hides the dialog.
func (d *JumpDialog) Close() { d.active = false }

// Active reports whether the dialog is visible and consumes input.
func (d *JumpDialog) Active() bool { return d != nil && d.active }

// Targets returns the current filtered target slice. Interactive uses
// this right after Open() to detect the "one unique match" shortcut
// and jump without showing the picker.
func (d *JumpDialog) Targets() []jumpTarget {
	if d == nil {
		return nil
	}
	return d.visible
}

// applyFilter re-computes d.visible from d.all + d.filter. Matching
// is case-insensitive substring on the preview. An empty filter
// returns all targets.
func (d *JumpDialog) applyFilter() {
	if d.filter == "" {
		d.visible = append(d.visible[:0], d.all...)
	} else {
		q := strings.ToLower(d.filter)
		d.visible = d.visible[:0]
		for _, t := range d.all {
			if strings.Contains(strings.ToLower(t.Preview), q) {
				d.visible = append(d.visible, t)
			}
		}
	}
	if d.cursor >= len(d.visible) {
		d.cursor = len(d.visible) - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
}

// Render draws the dialog.
const jumpFallbackRows = 12

// ChromeRows is the non-body rows Render emits at their worst case.
// Verified by TestEveryDialogFitsItsOwnBudget rather than counted by eye.
func (d *JumpDialog) ChromeRows() int { return 5 }

func (d *JumpDialog) Render(th tui.Theme, width int) []string {
	if !d.Active() {
		return nil
	}
	var lines []string
	lines = append(lines, FrameHeader(th, i18n.T("jump to turn"), width))
	if len(d.all) == 0 {
		lines = append(lines, th.FG256(th.Muted, i18n.T("no turns in this session yet")))
		lines = append(lines, th.FG256(th.Muted, i18n.T("press esc to close")))
		lines = append(lines, FrameRule(th, width))
		return lines
	}

	// Status line: shows the active filter, visible count, and hints.
	hint := "↑/↓ pick - enter jump - esc cancel - type to filter"
	if d.filter != "" {
		hint = i18n.T("filter: %q - %d match - ", d.filter, len(d.visible)) + hint
	}
	lines = append(lines, th.FG256(th.Muted, hint))

	if len(d.visible) == 0 {
		lines = append(lines, th.FG256(th.Muted, "  "+i18n.T("(nothing matches; backspace to widen)")))
		lines = append(lines, FrameRule(th, width))
		return lines
	}

	// Cap the visible window so a 200-turn session doesn't push the
	// editor off screen. Center around the cursor.
	maxRows := d.MaxRows
	if maxRows <= 0 {
		maxRows = jumpFallbackRows
	}
	// Centred: this list is filtered and rebuilt under the cursor, so
	// holding the cursor still and moving the content reads better than
	// scrolling only at the edges. Named here rather than implied by
	// whichever windowing helper was reached for.
	d.vp.Fit(len(d.visible), maxRows)
	d.vp.Center(d.cursor)
	start, end := d.vp.Window()
	if start > 0 {
		lines = append(lines, WindowMoreAbove(th, start))
	}
	for i := start; i < end; i++ {
		t := d.visible[i]
		plain := "  " + formatJumpRowPlain(t, width-2)
		if i == d.cursor {
			lines = append(lines, th.PadHighlight(plain, width))
		} else {
			lines = append(lines, th.FG256(th.Muted, plain))
		}
	}
	if end < len(d.visible) {
		lines = append(lines, WindowMoreBelow(th, len(d.visible), end))
	}

	lines = append(lines, FrameRule(th, width))
	return lines
}

// formatJumpRowPlain renders one target as a single line with turn
// number, tool count, and the prompt preview trimmed to fit.
func formatJumpRowPlain(t jumpTarget, maxWidth int) string {
	left := fmt.Sprintf("#%-3d  %s  ", t.TurnNo, toolBadge(t.ToolCount))
	room := maxWidth - len(left)
	if room < 10 {
		room = 10
	}
	preview := t.Preview
	if len(preview) > room {
		if room <= 3 {
			preview = "..."[:room]
		} else {
			preview = preview[:room-3] + "..."
		}
	}
	return left + preview
}

func toolBadge(n int) string {
	if n <= 0 {
		return "      "
	}
	if n > 99 {
		return " 99+t "
	}
	return fmt.Sprintf(" %2dt  ", n)
}

// HandleKey advances the dialog state and returns an action to apply.
// Runes are added to the filter; backspace removes the last rune.
func (d *JumpDialog) HandleKey(k tui.Key) jumpDialogAction {
	switch k.Kind {
	case tui.KeyUp:
		if d.cursor > 0 {
			d.cursor--
		}
	case tui.KeyDown:
		if d.cursor < len(d.visible)-1 {
			d.cursor++
		}
	case tui.KeyPageUp:
		d.cursor -= 5
		if d.cursor < 0 {
			d.cursor = 0
		}
	case tui.KeyPageDown:
		d.cursor += 5
		if d.cursor >= len(d.visible) {
			d.cursor = len(d.visible) - 1
		}
	case tui.KeyBackspace:
		if len(d.filter) > 0 {
			runes := []rune(d.filter)
			d.filter = string(runes[:len(runes)-1])
			d.applyFilter()
		}
	case tui.KeyRune:
		// Any printable rune extends the filter.
		d.filter += string(k.Rune)
		d.applyFilter()
	case tui.KeyEsc:
		d.Close()
		return jumpDialogAction{Close: true}
	case tui.KeyEnter:
		if len(d.visible) == 0 {
			return jumpDialogAction{}
		}
		t := d.visible[d.cursor]
		d.Close()
		return jumpDialogAction{Select: true, Purpose: d.purpose, MessageIdx: t.MessageIdx, TurnNo: t.TurnNo}
	}
	return jumpDialogAction{}
}

// buildJumpTargets walks the session transcript and produces one
// jumpTarget per user message, enriched with the tool count from
// the following assistant messages up to the next user boundary.
func buildJumpTargets(msgs []provider.Message) []jumpTarget {
	var out []jumpTarget
	turn := 0
	for i, m := range msgs {
		// core.IsUserTurn, not "role is user": four machine-authored messages
		// also carry RoleUser, and the picker offered every one of them as a
		// turn — a tool-image mirror previewing as the wire prefix, a /clear
		// divider as "(empty)", a compaction summary as its own first line, and
		// a host nudge as itself. They took turn NUMBERS too, so a transcript
		// with any of them misnumbered every real turn after it.
		//
		// i still indexes msgs, so skipping a row shifts nothing: the cut point
		// a fork resolves to is unaffected by what the picker chooses to show.
		if !core.IsUserTurn(m) {
			continue
		}
		turn++
		t := jumpTarget{
			MessageIdx: i,
			TurnNo:     turn,
			Preview:    firstLineOfUserMessage(m),
			ToolCount:  countToolsUntilNextUser(msgs, i),
		}
		out = append(out, t)
	}
	return out
}

// firstLineOfUserMessage returns the first non-empty text line of a
// user message, trimmed to a single visible line, for use as a row
// preview. Non-text blocks (images) are summarised.
func firstLineOfUserMessage(m provider.Message) string {
	for _, c := range m.Content {
		switch b := c.(type) {
		case provider.TextBlock:
			for _, line := range strings.Split(b.Text, "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					return line
				}
			}
		case provider.ImageBlock:
			return i18n.T("[image - %s - %d bytes]", b.MimeType, len(b.Data))
		}
	}
	return "(empty)"
}

// countToolsUntilNextUser totals the tool calls emitted by assistant
// messages between msgs[i] (exclusive) and the next user message
// (exclusive). Tool-result messages are ignored because they mirror
// the call count 1:1.
func countToolsUntilNextUser(msgs []provider.Message, i int) int {
	n := 0
	for j := i + 1; j < len(msgs); j++ {
		if msgs[j].Role == provider.RoleUser {
			break
		}
		if msgs[j].Role == provider.RoleAssistant {
			for _, c := range msgs[j].Content {
				if _, ok := c.(provider.ToolCallBlock); ok {
					n++
				}
			}
		}
	}
	return n
}
