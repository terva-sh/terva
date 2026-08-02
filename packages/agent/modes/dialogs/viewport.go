package dialogs

import "terva.sh/terva/packages/tui"

// Viewport is the scroll state of a dialog body: an offset into a slice of
// pre-rendered lines, plus the geometry the last render used.
//
// It exists because nine dialogs had each grown their own. Every one carried a
// bare `scroll int`, clamped it by hand inside Render, and picked a page size
// out of the air — 5 lines in tasks, 8 in changelog and skills, 14 in context —
// none of which had anything to do with how many rows that dialog actually
// showed. So PgDn moved a different distance than the eye did, differently per
// dialog, and Home/End existed in two dialogs out of nine.
//
// Three of the hand-rolled clamps stopped at len(body)-1 rather than
// len(body)-rows, which let the body scroll until a single line sat alone in an
// otherwise empty pane. That is the kind of bug a copy per dialog produces and a
// shared implementation cannot: the clamp is written once, here, in Fit.
//
// The zero value is a viewport at the top with no geometry yet, which is the
// correct state for a dialog that has not rendered.
type Viewport struct {
	// off is the first visible line. Kept in [0, max] by Fit, never by the
	// callers — a caller that clamps is a caller that can disagree.
	off int
	// rows and total are the last render's geometry. They are what let a KEY
	// clamp itself: a key arrives between renders, when only the previous
	// frame's shape is known, and the alternative (clamping in Render, as every
	// dialog used to) means the offset is briefly wrong and any code reading it
	// before the next paint reads a lie.
	rows, total int
}

// Reset returns the viewport to the top. Call it when the body is replaced by
// something unrelated — a new tab, a different record — where holding position
// would land the reader in the middle of content they have not seen.
func (v *Viewport) Reset() { v.off = 0 }

// Offset is the first visible line.
func (v *Viewport) Offset() int { return v.off }

// SetOffset places the offset directly, clamped. It exists for the two dialogs
// whose list view derives the top from a cursor with its own padding rule
// (clampViewTop) rather than from scroll keys — they own the arithmetic and
// borrow only the storage and the clamp.
func (v *Viewport) SetOffset(off int) {
	v.off = off
	v.clamp()
}

// Fit records the geometry the renderer is about to use and clamps the offset
// into it. Every Render calls it before Window or Rows.
//
// It is a separate call rather than a parameter to Window because the geometry
// has to outlive the render: HandleKey needs it, and the next key arrives long
// after Render has returned.
func (v *Viewport) Fit(total, rows int) {
	// rows <= 0 means "no cap" — a picker whose host has not sized it yet, which
	// the superseded CursorWindow answered by returning the whole list. Treating
	// it as a zero-height pane instead would render an empty one, so the pane
	// becomes the body and Max/Scrollable both fall out as "nothing to scroll".
	if rows <= 0 {
		rows = total
	}
	v.total, v.rows = total, rows
	v.clamp()
}

// Max is the largest offset that still fills the pane — total minus one
// screenful, floored at zero. Scrolling past it would trade visible content for
// blank rows, which is what the len(body)-1 clamps used to do.
func (v *Viewport) Max() int {
	if v.total <= v.rows {
		return 0
	}
	return v.total - v.rows
}

// Scrollable reports whether the body is taller than the pane. Dialogs use it to
// decide whether to spend a row on a scroll hint.
func (v *Viewport) Scrollable() bool { return v.total > v.rows }

// Window returns the [start, end) bounds of the visible slice.
func (v *Viewport) Window() (start, end int) {
	end = v.off + v.rows
	if end > v.total {
		end = v.total
	}
	if end < v.off {
		end = v.off
	}
	return v.off, end
}

func (v *Viewport) up() {
	if v.off > 0 {
		v.off--
	}
}

func (v *Viewport) down() {
	if v.off < v.Max() {
		v.off++
	}
}

// page is one screenful, which is the whole point of paging. A page smaller
// than the pane leaves the reader re-reading lines; a page larger skips some.
// One row of overlap would be defensible and is deliberately not done — none of
// the nine dialogs had it, and inventing it here would make the shared version
// behave unlike every version it replaces.
func (v *Viewport) page() int {
	if v.rows > 0 {
		return v.rows
	}
	return 1
}

func (v *Viewport) pageUp() {
	v.off -= v.page()
	if v.off < 0 {
		v.off = 0
	}
}

func (v *Viewport) pageDown() {
	v.off += v.page()
	if v.off > v.Max() {
		v.off = v.Max()
	}
}

// Top and Bottom are the jumps. Bottom lands on Max, so the last line sits at
// the bottom of a FULL pane rather than alone at the top of an empty one.
func (v *Viewport) Top()    { v.off = 0 }
func (v *Viewport) Bottom() { v.off = v.Max() }

// HandleKey applies the standard body-scroll keys and reports whether it
// consumed one. A dialog routes its unclaimed keys here:
//
//	if d.vp.HandleKey(k) {
//		return false
//	}
//
// Returning false for anything else is what lets a dialog keep its own meaning
// for ↑/↓ (a cursor list) while still inheriting PgUp/PgDn/Home/End — it simply
// handles those keys before calling this.
func (v *Viewport) HandleKey(k tui.Key) bool {
	switch k.Kind {
	// The wheel is the same intent as ↑/↓ and is bound here rather than per
	// dialog, which is why the tasks panel was the only one that scrolled on a
	// wheel event and the other eight ignored it.
	case tui.KeyUp, tui.KeyMouseWheelUp:
		v.up()
	case tui.KeyDown, tui.KeyMouseWheelDown:
		v.down()
	case tui.KeyPageUp:
		v.pageUp()
	case tui.KeyPageDown:
		v.pageDown()
	case tui.KeyHome:
		v.Top()
	case tui.KeyEnd:
		v.Bottom()
	default:
		return false
	}
	return true
}

// A cursor-following viewport has to choose HOW the offset follows, and the two
// answers below were, until they moved here, two separate helpers applied by
// whichever one an author happened to find. `CursorWindow` re-centred the pane on
// every cursor move; `clampViewTop` scrolled only when the cursor neared an edge.
// The model picker jumped, the session list did not, and nothing recorded either
// as a decision.
//
// Both survive, because both are defensible — a list you arrow through wants the
// cursor to stay put (Reveal), while a list that is being filtered under you
// reads better centred (Center). What changes is that the choice is now named at
// the call site instead of implied by a helper's name.

// Reveal scrolls the minimum distance that brings line into view.
func (v *Viewport) Reveal(line int) { v.RevealPadded(line, 0) }

// RevealPadded is Reveal keeping pad rows of context beyond the cursor, so a
// list scrolls a little BEFORE the cursor hits the edge and you can see where
// you are heading.
//
// pad is dropped on a pane too short to honour it: with fewer than 3*pad rows
// the padding would consume the whole window and the cursor could never sit
// anywhere but the middle, which is the opposite of the intent.
func (v *Viewport) RevealPadded(line, pad int) {
	if v.rows <= 0 {
		return
	}
	if v.rows < 3*pad {
		pad = 0
	}
	if line < v.off+pad {
		v.off = line - pad
	}
	if line >= v.off+v.rows-pad {
		v.off = line - v.rows + pad + 1
	}
	v.clamp()
}

// Center puts line in the middle of the pane, so the cursor holds still and the
// content moves under it. Clamped at the ends, so the first and last screenfuls
// are not centred — there is nothing to scroll past.
func (v *Viewport) Center(line int) {
	if v.rows <= 0 {
		return
	}
	v.off = line - v.rows/2
	v.clamp()
}

func (v *Viewport) clamp() {
	if v.off > v.Max() {
		v.off = v.Max()
	}
	if v.off < 0 {
		v.off = 0
	}
}

// Rows renders the visible slice with the standard more-above / more-below
// indicators around it.
//
// The indicators cost a row each and are drawn OUTSIDE the window rather than
// replacing its first and last lines, which matches what every dialog already
// did. A dialog that cannot spare the rows should call Window and slice itself.
func (v *Viewport) Rows(th tui.Theme, body []string) []string {
	start, end := v.Window()
	out := make([]string, 0, (end-start)+2)
	if start > 0 {
		out = append(out, WindowMoreAbove(th, start))
	}
	for i := start; i < end && i < len(body); i++ {
		out = append(out, body[i])
	}
	if end < len(body) {
		out = append(out, WindowMoreBelow(th, len(body), end))
	}
	return out
}
