package dialogs

// Height budgeting for dialog bodies.
//
// A dialog is drawn into the band below the transcript, and the host sizes that
// band by what the dialog returns: `chatRows = termRows - len(bottom)`, floored
// at 1. So a dialog that renders more rows than the terminal can spare does not
// get clipped — it SQUEEZES THE TRANSCRIPT, down to a single row in the limit.
// Nothing anywhere refuses to draw it.
//
// Before this, the arithmetic lived in the overlay registry, inline, six times,
// with six different magic numbers: `rows - 10`, `rows - 12`, `rows - 13`, and
// floors of 3 or 4. Each number was a hand-derived "how much chrome does this
// dialog have", written next to the registry entry rather than next to the
// Render that decides it, and nothing checked the two against each other. Nine
// more dialogs skipped the mechanism entirely and hardcoded a body height — 12,
// 14, 16, 18 — which is a height that cannot be right, because the terminal is
// not an input to it. Measured: changelog drew 23 rows, context 22, log 20, on a
// terminal whose band is 24 minus the composer.
//
// So: the band reserve and the floor live here once, and each dialog declares
// its own chrome NEXT TO its Render, where the rows are actually emitted, with a
// test that renders the dialog and checks the declaration against the result.
const (
	// chatBandRows is what sits below a dialog and must survive it: the editor
	// (~3 rows, more when a draft wraps), the status line, and the blank rows
	// separating them. Reserved for every dialog rather than per-dialog, since
	// none of them controls it.
	chatBandRows = 6

	// minChatRows is the transcript's share, and it is the whole point of this
	// file. `chatRows = termRows - len(bottom)` floored at 1, so a dialog that
	// budgets right up to the terminal height leaves the conversation one row —
	// or, once the floor engages, pushes the dialog's own header off the top.
	// Reserving it here is what makes the budget a budget.
	minChatRows = 2

	// minBodyRows is the floor. A dialog showing fewer rows than this is not
	// usable, and at that point the honest outcome is a squeezed transcript
	// rather than an empty dialog — the person opened the dialog on purpose.
	// One floor, not the 3-or-4 that six sites each chose for themselves.
	minBodyRows = 3
)

// BodyBudget is how many BODY rows a dialog may draw on a terminal of termRows,
// given the chrome rows it spends on its own frame.
//
// chrome is the dialog's WORST CASE, not its typical one: several dialogs render
// a row only in some states (a scroll indicator when there is more, a session
// path when one is known), and budgeting for the typical case overflows exactly
// when the extra row appears — which is the state a person is most likely to be
// in when they care.
func BodyBudget(termRows, chrome int) int {
	n := termRows - chatBandRows - minChatRows - chrome
	if n < minBodyRows {
		return minBodyRows
	}
	return n
}
