package dialogs

import (
	"strings"

	"github.com/mattn/go-runewidth"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/tui"
)

// FrameHeader returns a full-width rule with a small title at the left.
// Matches the thin rule style used for code blocks and tool results so
// every dialog in the TUI looks the same.
//
//	─── title ────────────────────────────────
func FrameHeader(th tui.Theme, title string, width int) string {
	label := "── " + title + " "
	if width <= 0 {
		return th.FG256(th.Muted, label)
	}
	padLen := width - runewidth.StringWidth(label)
	if padLen < 0 {
		padLen = 0
	}
	return th.FG256(th.Muted, label+strings.Repeat("─", padLen))
}

// FrameRule returns a full-width horizontal rule in the muted color.
func FrameRule(th tui.Theme, width int) string {
	if width <= 0 {
		width = 1
	}
	return th.FG256(th.Muted, strings.Repeat("─", width))
}

// PadDialogFrame inserts breathing room between the shared dialog frame
// chrome and its body while keeping frameHeader/frameRule as single-row
// primitives for callers that need exact row accounting.
func PadDialogFrame(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}

	out := append([]string(nil), lines...)
	if HeaderPadRows(out) > 0 {
		out = append(out[:1], append([]string{""}, out[1:]...)...)
	}

	last := len(out) - 1
	if last > 0 && isFrameRuleLine(out[last]) && strings.TrimSpace(widgets.StripANSIBytes(out[last-1])) != "" {
		out = append(out[:last], append([]string{""}, out[last:]...)...)
	}
	return out
}

// HeaderPadRows reports how many rows PadDialogFrame will insert after
// the frame header — 1 when the body doesn't already start blank, else 0.
//
// Exported because a dialog that owns a caret has to answer "which row
// is my caret on, in the block the host actually paints", and the host
// pads that block after the dialog has rendered it. Hand-copying the
// condition into each such dialog is how the caret ends up one row off.
func HeaderPadRows(lines []string) int {
	if len(lines) == 0 || !isFrameHeaderLine(lines[0]) {
		return 0
	}
	if len(lines) > 1 && strings.TrimSpace(widgets.StripANSIBytes(lines[1])) == "" {
		return 0
	}
	return 1
}

func isFrameHeaderLine(line string) bool {
	return strings.HasPrefix(widgets.StripANSIBytes(line), "── ")
}

func isFrameRuleLine(line string) bool {
	plain := widgets.StripANSIBytes(line)
	if plain == "" {
		return false
	}
	for _, r := range plain {
		if r != '─' {
			return false
		}
	}
	return true
}

// FrameHeaderColor is like frameHeader but renders in a caller-supplied
// 256-color code. Used by the update-available banner which wants a
// yellow accent on the rules and title.
func FrameHeaderColor(th tui.Theme, title string, width, color int) string {
	label := "── " + title + " "
	if width <= 0 {
		return th.FG256(color, label)
	}
	padLen := width - runewidth.StringWidth(label)
	if padLen < 0 {
		padLen = 0
	}
	return th.FG256(color, label+strings.Repeat("─", padLen))
}

// FrameRuleColor is like frameRule in an explicit color.
func FrameRuleColor(th tui.Theme, width, color int) string {
	if width <= 0 {
		width = 1
	}
	return th.FG256(color, strings.Repeat("─", width))
}
