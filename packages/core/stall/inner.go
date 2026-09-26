package stall

import (
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// A tool may call other host tools on its own behalf: code_execution's
// read/grep/glob bindings, code_execution_mutating's write/edit, a disclosed
// tool reached through call(). Those are INNER calls. The model never issued
// them and never sees their results — it sees only what the outer tool
// returned, which for a script is whatever the program printed.
//
// That blinds the churn axis. unproductiveResult classifies the OUTER result,
// so a script whose host calls fail identically on every attempt looks
// productive from outside as long as the program catches the error and prints
// something. The recorded case is
// docs/reviews/2026-08-29-local-model-harness-friction-review.md F3: six
// consecutive code_execution calls whose inner reads each returned the same
// unproductive result, with the outer arguments differing (a different script
// each time) and the outer results differing (a different printed message
// each time). Neither axis could see it, no nudge fired, and the loop ran for
// 28 turns.
//
// The fix is this file: an inner call reports its outcome against the outer
// call that caused it (core.ReportInnerCall, which hands it to the step gate's
// Inner), and record() folds the result into that outer step's
// churn key. Two rules keep it from manufacturing false positives:
//
//   - One outer call contributes AT MOST ONE churn step, never one per inner
//     call. A script that probes fifty paths and finds ten missing is doing
//     its job; counting ten would trip the threshold inside a single call.
//   - An outer result that is ITSELF unproductive keeps precedence. The inner
//     signal is consulted only when the outer result looked productive, so no
//     existing classification changes and every pre-existing signature is
//     unaffected.

// maxInnerClasses bounds how many DISTINCT unproductive classes one outer call
// retains. The dominant class is the only one that can be used, so a script
// failing in many different ways past this bound loses nothing that would have
// been reported; the cap exists so a pathological program cannot grow the map
// without limit.
const maxInnerClasses = 16

// innerCall is one unproductive inner result, reduced to the same (class,
// detail) pair unproductiveResult produces for an outer one.
type innerCall struct {
	tool   string
	class  string
	detail string
}

// innerChurn accumulates the unproductive inner calls of ONE outer call.
// It stores counts per class rather than a list of calls: the dominant class
// is all record() needs, and a count map is bounded by the number of distinct
// failures rather than by how many host calls the script made.
type innerChurn struct {
	counts map[string]int
	first  map[string]innerCall
	order  []string // first-seen class order, so ties resolve deterministically
}

func (c *innerChurn) add(ic innerCall) {
	if c.counts == nil {
		c.counts = map[string]int{}
		c.first = map[string]innerCall{}
	}
	if _, seen := c.counts[ic.class]; !seen {
		if len(c.order) >= maxInnerClasses {
			return
		}
		c.order = append(c.order, ic.class)
		c.first[ic.class] = ic
	}
	c.counts[ic.class]++
}

// dominant reports the class this outer call failed on most often, and the
// first call that produced it. Ties go to the class seen first, so the answer
// does not depend on map iteration order.
func (c *innerChurn) dominant() (innerCall, bool) {
	best, bestN := "", 0
	for _, class := range c.order {
		if n := c.counts[class]; n > bestN {
			best, bestN = class, n
		}
	}
	if bestN == 0 {
		return innerCall{}, false
	}
	return c.first[best], true
}

// recordInner is the detector's StepGate.Inner, which core.ReportInnerCall
// reaches. It keeps only unproductive results, classified by the SAME
// unproductiveResult the outer axis uses, so an inner failure and an outer one
// are judged by one rule rather than two that can drift.
func (d *Detector) recordInner(outer, tool string, res core.ToolResult) {
	class, detail, ok := unproductiveResult(provider.ToolResultBlock{
		Content: res.Content,
		IsError: res.IsError,
	})
	if !ok {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.on {
		return
	}
	d.t.recordInner(outer, innerCall{tool: tool, class: class, detail: detail})
}

// innerDetail renders an inner failure for the nudge. The nudge template says
// "you have called <outer> N times with the same result: <detail>", and the
// outer tool is the only name the model can act on — so the detail has to name
// the INNER tool itself, or the model reads an error it never saw, attributed
// to a call it did not make.
func innerDetail(ic innerCall) string {
	d := ic.detail
	if d == "" {
		d = ic.class
	}
	return clip(ic.tool+" → "+d, stallDetailMax)
}

// innerCalls is the tracker's side table: outer call id -> the unproductive
// inner calls made while it ran. observe() drains it.
//
// It is written by whatever goroutine the executing TOOL is on, which is the
// turn goroutine today but is not the tracker's promise to make on a tool's
// behalf. The Detector's lock covers it, as it covers the rest of the tracker.
type innerCalls struct {
	m map[string]*innerChurn
}

func (t *tracker) recordInner(outerID string, ic innerCall) {
	if t.inner.m == nil {
		t.inner.m = map[string]*innerChurn{}
	}
	c := t.inner.m[outerID]
	if c == nil {
		c = &innerChurn{}
		t.inner.m[outerID] = c
	}
	c.add(ic)
}

// takeInner removes and returns the inner churn for one outer call. Removing
// on read keeps the table the size of one turn's in-flight calls rather than
// the whole session, and means a call can never be folded in twice.
func (t *tracker) takeInner(outerID string) (innerCall, bool) {
	c := t.inner.m[outerID]
	if c == nil {
		return innerCall{}, false
	}
	delete(t.inner.m, outerID)
	return c.dominant()
}

// clearInner drops every pending attribution. Called from reset() at the turn
// boundary: an inner call whose outer step was never observed (a cancelled
// turn, a refused call) has nothing left to attach to.
func (t *tracker) clearInner() {
	t.inner.m = nil
}
