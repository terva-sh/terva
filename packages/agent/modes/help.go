package modes

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"

	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// helpKeyRows is the list of keybindings shown by /help. The key names are
// literal; the descriptions are marked with i18n.M (declared here at init,
// translated at render time by renderHelpBlock).
var helpKeyRows = [][2]string{
	{"enter", i18n.M("submit the current input")},
	{"ctrl+j", i18n.M("insert a newline (works in every terminal)")},
	{"shift+enter / alt+enter", i18n.M("insert a newline (needs a terminal that reports modified enter)")},
	{"\\ then enter", i18n.M("insert a newline (the backslash is consumed)")},
	{"tab", i18n.M("complete the highlighted slash command")},
	{"esc", i18n.M("cancel the current turn (while busy) - clear the input (while idle)")},
	{"ctrl+c", i18n.M("exit (while idle) - cancel the current turn (while busy)")},
	{"ctrl+w", i18n.M("delete previous word")},
	{"alt+backspace", i18n.M("delete previous word (same as ctrl+w)")},
	{"ctrl+u / ctrl+k", i18n.M("delete to start / end of line")},
	{"ctrl+a / ctrl+e", i18n.M("jump to start / end of line")},
	{"alt+← / alt+→", i18n.M("jump one word back / forward")},
	{"ctrl+l", i18n.M("redraw the screen")},
	{"ctrl+o", i18n.M("expand / collapse long tool results")},
	{"ctrl+t", i18n.M("cycle tool display (boxes - minimal - grouped - hidden)")},
	{"ctrl+s", i18n.M("set the current draft aside / bring it back (it also returns after you send)")},
	{"ctrl+v", i18n.M("paste a clipboard image into the prompt (also /paste)")},
	{"pgup / pgdn", i18n.M("scroll the chat one page up / down")},
	{"up / down", i18n.M("move within multi-line input - scroll chat at input edge")},
}

// renderHelpBlock builds the friendly /help view. Uses the shared
// frameHeader/frameRule helpers so the rules match every other block
// in the tui (tool results, code fences, dialogs) — full terminal width
// in the muted colour.
//
// Slash commands and keybindings share the same label column width so
// every description starts at the same x-position, regardless of which
// section it lives in. The width is computed from the longest label
// across BOTH lists, with a minimum of 14 cells so changes to either
// list don't compress the column visually.
func renderHelpBlock(th tui.Theme, width int) []string {
	if width < 20 {
		width = 20
	}

	// Label column width uses display cells, not byte length, so
	// single-cell multibyte runes (← → - ...) don't over-count and leave
	// a raggedy right edge. `len("alt+← / alt+→")` is 17 bytes but
	// only 13 cells; padding off byte length would either overshoot
	// (setting labelWidth too high and wasting space on every row)
	// or undershoot (never padding that row because len >= labelWidth
	// already, leaving its description mis-aligned).
	labelWidth := 14
	for _, c := range builtinSlashCatalog() {
		if c.Header {
			continue // section labels don't participate in column sizing
		}
		if n := runewidth.StringWidth(c.Name); n > labelWidth {
			labelWidth = n
		}
	}
	for _, k := range helpKeyRows {
		if n := runewidth.StringWidth(k[0]); n > labelWidth {
			labelWidth = n
		}
	}

	pad := func(s string) string {
		w := runewidth.StringWidth(s)
		if w >= labelWidth {
			return s
		}
		return s + strings.Repeat(" ", labelWidth-w)
	}

	var out []string
	out = append(out, dialogs.FrameHeader(th, i18n.T("terva help"), width), "")

	// commands section. Group headers from the catalog become muted
	// section labels with a blank row above, mirroring the divider rows
	// the autocomplete popup draws for the same groups.
	out = append(out, tui.Bold(i18n.T("slash commands:")))
	for _, c := range builtinSlashCatalog() {
		if c.Header {
			out = append(out, "", "  "+th.FG256(th.Muted, c.Name+":"))
			continue
		}
		out = append(out, fmt.Sprintf("  %s  %s",
			th.FG256(th.Accent, pad(c.Name)),
			th.FG256(th.Muted, c.Desc)))
	}

	// keys section
	out = append(out, "", tui.Bold(i18n.T("keys:")))
	for _, k := range helpKeyRows {
		out = append(out, fmt.Sprintf("  %s  %s",
			th.FG256(th.Accent, pad(k[0])),
			th.FG256(th.Muted, i18n.T(k[1]))))
	}

	out = append(out, "", dialogs.FrameRule(th, width), "")
	return out
}
