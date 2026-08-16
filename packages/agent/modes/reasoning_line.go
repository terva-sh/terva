package modes

import (
	"strings"

	"terva.sh/terva/packages/tui"
)

// The live reasoning row: one muted line above the status bar carrying what the
// model says it is doing right now.
//
// It sits beside the spinner rather than inside it. The spinner's message is
// terva's own voice and is deliberately ONE phrase per turn — a rotating quip
// implies progress that is not happening (see widgets.Spinner.Start). This row
// is the model's voice and DOES change as the work changes, so putting it in
// the spinner's slot would have traded a stable label for a churning one and
// re-made the mistake that comment records.

// reasoningLineText renders the accumulated reasoning summary as the single
// line to display, or "" when there is nothing worth showing.
//
// Only the CURRENT section survives. Providers separate one summary section
// from the next with a blank line, and each section supersedes the last: the
// model has moved on from "reading the config" to "editing the handler", and
// showing both would grow a log in a slot that is one row tall.
//
// 🪤 The result is squashed to a single line on purpose. A summary is usually a
// short headline, but the same field carries multi-paragraph prose from other
// providers (measured: a ~1.5k-char median on deepseek, and a 48k outlier on
// codex). Anything that reaches the renderer with newlines still in it would
// push the status bar and editor off the screen.
func reasoningLineText(acc string) string {
	if i := strings.LastIndex(acc, "\n\n"); i >= 0 {
		acc = acc[i+2:]
	}
	// Bold markers are how the openai backends head each section; they are
	// markup for a document, and this row is not one.
	acc = strings.ReplaceAll(acc, "**", "")
	acc = strings.ReplaceAll(acc, "\r", "")
	acc = strings.ReplaceAll(acc, "\n", " ")
	return strings.Join(strings.Fields(acc), " ")
}

// reasoningRows is the rendered row, or nil when there is nothing to show.
//
// Takes the already-shaped text rather than reading i.reasoning: the snapshot
// captures it under i.mu and the render runs unlocked, the same split every
// other row in the bottom band uses.
func reasoningRows(th tui.Theme, text string, cols int) []string {
	if text == "" {
		return nil
	}
	// Width budget matches the sibling rows: two-space indent, a two-cell
	// marker, and a margin so a full-width line cannot wrap into a second row.
	return []string{
		th.FG256(th.Accent, "  · ") + th.FG256(th.Muted, truncateLine(text, cols-8)),
	}
}
