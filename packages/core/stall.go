package core

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// A stall is repetition without progress: the same call, or the same failure,
// over and over. stallTracker watches the (tool call, tool result) steps of a
// turn and, when the model is provably spinning, produces a one-turn nudge the
// harness rides on the ephemeral tail. It is the detector half of
// docs/proposals/stuck-loop-escalation.md; the escalation rungs build on it.
//
// Tripping is a within-turn judgement, but a loop that outlives the turn it
// started in does not start over: a signature still recurring at the boundary
// carries its run length forward (see carried), so the ladder resumes at the
// rung it reached instead of being refunded once per turn.
//
// It is deliberately generic — no tool is special-cased — because the models that
// stall do so on whatever tool traps them (built-in, extension, or MCP), and even
// frontier models loop. Grounded in a real session where a small local model
// repeated one failing task_update 18 times before the operator hand-swapped to a
// stronger model: catching that at the third identical result instead of the
// eighteenth would have saved ~15 dispatches and ~270k tokens.
//
// Two axes catch two different loops, and their union trips:
//
//   - spin: the same (tool, canonical args), regardless of result. Canonical args
//     drop free-text "thought" fields so cosmetic prose churn can't hide a
//     structural repeat. Catches a file re-read four times over.
//   - error-churn: the same (tool, normalized error), regardless of args. Catches
//     the death loop, whose args varied per call (a different `evidence` string
//     each time) while the error was identical — the case an args-inclusive key
//     misses, which is why hashing (tool, args, error) *together* would never trip
//     on the very loop it targets.
//
// The tracker touches only turn-goroutine state (runLoop observes, oneTurn reads
// the nudge), so it carries no lock of its own; the on/off flag it gates behind
// does (Agent.stallDetect).

// stallThreshold is how many times a signature must recur, within the recent
// window, to count as a stall. Three: two identical results can be coincidence or
// a legitimate re-read; a third is a pattern.
const stallThreshold = 3

// stallEscalateAfterNudge is how many MORE times a nudged signature must recur
// before the tracker raises an escalation request (rung 3). The nudge (rung 1)
// gets the model one chance to self-correct; if it keeps looping past that, a
// harness-level intervention is warranted. Escalate at stallThreshold + this = 5
// identical results; the invariant below keeps that inside the window.
const stallEscalateAfterNudge = 2

// stallThrashThreshold is the SECOND escalation trigger, beside the monotonous
// same-signature watermark above: how many DISTINCT signatures may be nudged in
// one turn before the tracker escalates. A model that keeps failing in NEW ways —
// three different loops, each already nudged and un-heeded — is as stuck as one
// repeating a single call, and the same-signature count never catches it because
// each fresh failure resets which signature is counting. Grounded in a dogfood
// run where a small model thrashed across four distinct loops (spin-read,
// churn-read-guard, two churn-edits): four nudges, zero escalation, because no
// single signature reached five. Three distinct nudges (>= 9 unproductive calls
// across >= 3 loops) in a turn is a thrash worth escalating.
const stallThrashThreshold = 3

// stallWindow bounds how far back a recurrence still counts, so a signature seen
// once long ago and once more now does not trip. Wide enough to catch an
// oscillation between two failing calls (A,B,A,B,A → three A's in five steps) AND
// to reach the escalate watermark (stallThreshold + stallEscalateAfterNudge),
// narrow enough that unrelated earlier work falls off. Invariant:
// stallWindow >= stallThreshold + stallEscalateAfterNudge, or escalation could
// never fire because the count would fall out of the window first.
const stallWindow = 8

// stallDetailMax clips the error slice shown to the model in the nudge.
const stallDetailMax = 120

type stallStep struct {
	spinKey  string // tool + canonical args
	churnKey string // tool + normalized result-class; "" when the result was productive
	detail   string // readable error/guard slice for the nudge
}

type stallTracker struct {
	steps   []stallStep     // recent, bounded to stallWindow
	nudged  map[string]bool // signature -> nudge already emitted (once per stall per turn)
	pending string          // nudge/handoff riding the next ephemeral tail; cleared after a successful dispatch

	// escalate holds a raised-but-not-yet-acted-on escalation request (rung 3);
	// escalated records that an escalation was already offered this turn, so the
	// harness offers it at most once even if the loop continues.
	escalate  *stallEscalation
	escalated bool

	// declines counts how many times the user answered "keep trying" to an
	// escalation offer this turn. forgive() bumps it and wipes the loop state so
	// the model gets a fresh window (breathing room); both escalate triggers back
	// off by it, so each decline raises the bar for the next offer and a hopeless
	// case is not re-asked every few calls.
	declines int

	// seen counts every recurrence of each signature THIS turn, unwindowed —
	// distinct from the windowed counts record() derives from steps, which govern
	// locality within a turn. It exists only to build carried at the turn
	// boundary, so what crosses is the true run length rather than the last
	// stallWindow of it.
	seen map[string]int

	// carried is how many times each signature recurred in EARLIER turns, and is
	// the one piece of loop state that survives reset(). Without it a loop that
	// spans a turn boundary restarts the ladder: the same signature, still
	// failing the same way, is nudged again at its third recurrence as though the
	// previous turn had not happened, and the escalation watermark it had already
	// crossed has to be crossed again from zero.
	//
	// reset() rebuilds it from seen and keeps ONLY signatures that recurred in the
	// turn just ended, so it cannot accumulate: a signature the model stopped
	// repeating is forgotten at the next boundary. It deliberately does not affect
	// whether a nudge trips — that stays a within-turn judgement (see record) — so
	// a fresh turn still needs a real local pattern before anything fires.
	carried map[string]int
}

// stallEscalation is the tracker's internal signal that a loop has persisted past
// the nudge. The harness turns it into a public EscalationRequest (escalate.go).
type stallEscalation struct {
	tool      string
	reason    string // "stuck on task_update ×5: <error>" — for the ask and handoff marker
	signature string // the tripped signature, opaque to the host
}

// stallAxis names which detector axis tripped.
const (
	stallAxisSpin  = "spin"  // the same call, repeated
	stallAxisChurn = "churn" // the same failure, repeated
)

// stallEvent is one nudge the detector fired on a single observe call — the
// tracker's internal signal, which runLoop maps to a public StallRecord. Emitted
// once per signature per turn (the trip dedups), so it marks the moment a loop
// was first caught, not every recurrence.
type stallEvent struct {
	axis   string // stallAxisSpin | stallAxisChurn
	tool   string
	detail string // the error/guard slice; empty for spin
}

// StallRecord is what a detector nudge produced, handed to stall observers
// (observers.go). Hosts persist it as a "stall" session row, so rung 1 of the
// stuck-loop hatch (the nudge) is visible after the fact the way an escalation
// (rung 3) is via EscalationRecord. Written whenever detection nudges: unlike
// escalation it needs no configured target, so it records for every session that
// leaves the (default-on) detector running and sees a model repeat itself.
type StallRecord struct {
	Axis   string // "spin" (same call repeated) | "churn" (same failure repeated)
	Tool   string // the tool the model looped on
	Detail string // the repeated error/guard slice; empty for spin
}

// reset clears the tracker for a new turn. A repeat across turns is usually the
// user asking again, not the model stuck, so counting starts over — with one
// exception: a signature that was ALREADY recurring when the turn ended keeps
// its run length (see carried), because that is precisely the case the "user
// asking again" reading does not cover.
func (t *stallTracker) reset() {
	// Fold this turn's recurrences into carried BEFORE clearing, keeping only
	// signatures that actually RECURRED — seen at least twice, here or earlier.
	// That prune is what bounds the map: a signature the model stopped repeating
	// does not survive the boundary, so only a live loop is remembered, and only
	// for as long as it stays live.
	//
	// The >= 2 test matters more than it looks. A churn loop varies its arguments
	// (that is what makes it churn), so every call also mints a BRAND NEW spin
	// signature seen exactly once; carrying those would move a whole turn's worth
	// of one-shot keys across every boundary to be dropped at the next one. A
	// single occurrence is not a loop, and nothing downstream can act on it: a
	// trip needs three recurrences within the new turn regardless.
	var carry map[string]int
	for sig, n := range t.seen {
		total := t.carried[sig] + n
		if total < 2 {
			continue
		}
		if carry == nil {
			carry = make(map[string]int, len(t.seen))
		}
		carry[sig] = total
	}
	t.carried = carry
	t.seen = nil

	t.steps = t.steps[:0]
	t.nudged = nil
	t.pending = ""
	t.escalate = nil
	t.escalated = false
	t.declines = 0
}

// forgive gives the model a fresh window after the user declines an escalation
// ("keep trying"): it wipes the loop-detection state so counts restart and the
// nudge can fire again — another chance to self-correct on its own — and re-arms
// escalation (clears escalated) so a model that stays stuck can be offered again.
// declines climbs so both triggers back off (record()'s watermark and the thrash
// threshold both add it), which keeps a hopeless case from re-asking every few
// calls. It deliberately leaves pending alone: a handoff never rides a decline,
// and the nudge already fired. Reset zeroes declines for the next turn.
func (t *stallTracker) forgive() {
	t.steps = t.steps[:0]
	t.nudged = nil
	t.escalate = nil
	t.escalated = false
	t.declines++
	// Cross-turn history goes too, and for the same reason as everything above:
	// "keep trying" is the user overruling the ladder, so the ladder starts over.
	// Leaving carried in place would re-cross the escalation watermark on the
	// first recurrence and re-offer immediately — the exact re-asking that
	// declines exists to prevent.
	t.seen = nil
	t.carried = nil
}

// observe records one turn step: pair each tool call in the assistant message
// with its result and update the tracker. Called from runLoop after executeTools.
// Returns the nudges that newly fired this call (usually none or one), which
// runLoop persists as stall records; the return is safe to ignore where only the
// nudge/escalation side effects matter (the tracker's own tests do).
func (t *stallTracker) observe(call, result provider.Message) []stallEvent {
	results := make(map[string]provider.ToolResultBlock, len(result.Content))
	for _, c := range result.Content {
		if tr, ok := c.(provider.ToolResultBlock); ok {
			results[tr.CallID] = tr
		}
	}
	var events []stallEvent
	for _, c := range call.Content {
		tc, ok := c.(provider.ToolCallBlock)
		if !ok {
			continue
		}
		tr, ok := results[tc.ID]
		if !ok {
			continue // dispatched with no result — nothing to judge
		}
		if ev, ok := t.record(tc, tr); ok {
			events = append(events, ev)
		}
	}
	return events
}

// record folds one (call, result) step into the tracker and reports the nudge it
// newly triggered, if any. ok is true only on the first trip of a signature this
// turn (the recurrences after it are the next rung's concern), so at most one
// record per distinct loop reaches the session log.
func (t *stallTracker) record(tc provider.ToolCallBlock, tr provider.ToolResultBlock) (stallEvent, bool) {
	step := stallStep{spinKey: tc.Name + "\x00" + canonicalArgs(tc.Arguments)}
	if class, detail, ok := unproductiveResult(tr); ok {
		step.churnKey = tc.Name + "\x00" + class
		step.detail = detail
	}
	t.steps = append(t.steps, step)
	if len(t.steps) > stallWindow {
		t.steps = t.steps[len(t.steps)-stallWindow:]
	}

	spinN, churnN := 0, 0
	for _, s := range t.steps {
		if s.spinKey == step.spinKey {
			spinN++
		}
		if step.churnKey != "" && s.churnKey == step.churnKey {
			churnN++
		}
	}

	// The signature strings trip() and raiseEscalation() key on; also how this
	// step is counted for the turn, and looked up in what earlier turns saw.
	spinSig := "spin\x00" + step.spinKey
	churnSig := "churn\x00" + step.churnKey
	t.note(spinSig)
	if step.churnKey != "" {
		t.note(churnSig)
	}
	// Recurrences from earlier turns count toward how far along the ladder this
	// signature is — the escalation watermark, and the number the nudge reports —
	// but NOT toward whether it trips at all. Tripping stays a within-window
	// judgement so a new turn still needs a real local pattern (three
	// recurrences) before anything fires: carrying it into the trip test would
	// nudge on the first repeat after any boundary, which is the behaviour
	// TestStallResetsPerTurn exists to forbid.
	spinRung := spinN + t.carried[spinSig]
	churnRung := churnN + t.carried[churnSig]
	// Prefer the churn signal when both fire: it carries the error the model keeps
	// hitting, which makes for a more useful nudge than "same call again".
	//
	// The escalate watermark backs off by past declines (each "keep trying" raises
	// the bar), capped at the window — beyond it the count could never accumulate.
	esc := stallThreshold + stallEscalateAfterNudge + t.declines
	if esc > stallWindow {
		esc = stallWindow
	}
	switch {
	case churnN >= stallThreshold:
		tripped := t.trip(churnSig, tc.Name, churnRung, step.detail)
		if churnRung >= esc {
			t.raiseEscalation(tc.Name, churnRung, step.detail, churnSig)
		}
		if tripped {
			t.raiseThrashEscalation(tc.Name) // a new distinct loop; escalate if enough have nudged
			return stallEvent{axis: stallAxisChurn, tool: tc.Name, detail: step.detail}, true
		}
	case spinN >= stallThreshold:
		tripped := t.trip(spinSig, tc.Name, spinRung, "")
		if spinRung >= esc {
			t.raiseEscalation(tc.Name, spinRung, "", spinSig)
		}
		if tripped {
			t.raiseThrashEscalation(tc.Name)
			return stallEvent{axis: stallAxisSpin, tool: tc.Name}, true
		}
	}
	return stallEvent{}, false
}

// note counts one more recurrence of sig in the current turn. Only reset() reads
// the result, to decide what crosses the turn boundary.
func (t *stallTracker) note(sig string) {
	if t.seen == nil {
		t.seen = map[string]int{}
	}
	t.seen[sig]++
}

// raiseEscalation stages an escalation request, at most once per turn (escalated)
// and never over an already-pending one.
func (t *stallTracker) raiseEscalation(tool string, count int, detail, sig string) {
	if t.escalated || t.escalate != nil {
		return
	}
	t.escalate = &stallEscalation{tool: tool, reason: stallReason(tool, count, detail), signature: sig}
}

// raiseThrashEscalation is the thrash trigger: it escalates once the number of
// DISTINCT signatures nudged this turn reaches stallThrashThreshold. Called after
// each fresh nudge; a no-op until the count is reached, and idempotent after
// (raiseEscalation's guard is shared via the escalate field). tool is the current
// loop's tool, named only so the request carries something concrete.
func (t *stallTracker) raiseThrashEscalation(tool string) {
	// Backs off by past declines: each "keep trying" demands more distinct loops
	// before the next offer.
	if len(t.nudged) < stallThrashThreshold+t.declines || t.escalated || t.escalate != nil {
		return
	}
	t.escalate = &stallEscalation{tool: tool, reason: stallThrashReason(len(t.nudged)), signature: "thrash"}
}

func stallThrashReason(n int) string {
	return i18n.T("stuck across %d different failing loops this turn — the nudges aren't breaking it", n)
}

// escalation returns a raised escalation request without consuming it. The
// harness consumes it by acting (markEscalated), not by clearing a field.
func (t *stallTracker) escalation() (*stallEscalation, bool) {
	return t.escalate, t.escalate != nil
}

// markEscalated records that this turn's one escalation offer has been made
// (accepted, declined, or failed) so rung 3 never fires twice in a turn.
func (t *stallTracker) markEscalated() {
	t.escalate = nil
	t.escalated = true
}

// stageHandoff puts the post-escalation handoff marker on the ephemeral tail,
// riding the same one-turn vehicle as the nudge.
func (t *stallTracker) stageHandoff(text string) { t.pending = text }

func stallReason(tool string, count int, detail string) string {
	if detail != "" {
		return i18n.T("stuck on %s ×%d: %q", tool, count, detail)
	}
	return i18n.T("stuck on %s ×%d (the same call, repeated)", tool, count)
}

// trip stages the one-turn nudge for a signature and reports whether this was its
// first trip this turn (false on a re-trip, which changes nothing). The bool is
// what tells record a fresh nudge fired, so exactly one stall record is emitted
// per distinct loop.
func (t *stallTracker) trip(sig, tool string, count int, detail string) bool {
	if t.nudged[sig] {
		return false // one nudge per signature per turn; a determined loop is the next rung's job
	}
	if t.nudged == nil {
		t.nudged = map[string]bool{}
	}
	t.nudged[sig] = true
	t.pending = stallNudge(tool, count, detail)
	return true
}

// nudge returns the pending one-turn nudge without consuming it: oneTurn is
// re-entered per retry attempt, so the note must ride every attempt until one
// lands. clearNudge drops it once a request actually reaches the provider.
func (t *stallTracker) nudge() string { return t.pending }

func (t *stallTracker) clearNudge() { t.pending = "" }

// stallThoughtKeys are argument fields that carry the model's reasoning rather
// than what the call *does*. Dropping them before hashing keeps a structural
// repeat visible when only the prose changed — exactly the death loop's shape.
var stallThoughtKeys = map[string]bool{
	"evidence": true, "reasoning": true, "reason": true, "thought": true,
	"thoughts": true, "rationale": true, "note": true, "notes": true,
	"comment": true, "explanation": true, "summary": true,
}

// canonicalArgs renders a tool call's arguments as a stable key: a JSON object
// with thought fields dropped and keys sorted, so two calls that differ only in
// reasoning prose or key order collapse to the same string. Non-object or
// unparseable arguments fall back to their trimmed raw form.
func canonicalArgs(raw json.RawMessage) string {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || len(obj) == 0 {
		return strings.TrimSpace(string(raw))
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		if stallThoughtKeys[strings.ToLower(k)] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.Write(obj[k])
		b.WriteByte('\x00')
	}
	return b.String()
}

// stallGuardPhrases are harness-emitted "this did nothing" results that are not
// errors but are just as unproductive to repeat — the read-dedup guard is the
// canonical one.
var stallGuardPhrases = []string{"unchanged since you read it earlier"}

// unproductiveResult classifies a tool result for the churn axis. An error
// contributes its normalized text as the class; a harness "did nothing" guard
// contributes a stable guard class; a productive result contributes nothing.
func unproductiveResult(tr provider.ToolResultBlock) (class, detail string, ok bool) {
	text := toolResultText(tr)
	if tr.IsError {
		return normalizeError(text), clip(strings.TrimSpace(text), stallDetailMax), true
	}
	low := strings.ToLower(text)
	for _, p := range stallGuardPhrases {
		if strings.Contains(low, p) {
			return "guard\x00" + p, p, true
		}
	}
	return "", "", false
}

func toolResultText(tr provider.ToolResultBlock) string {
	var b strings.Builder
	for _, c := range tr.Content {
		if t, ok := c.(provider.TextBlock); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

var (
	stallExitRe = regexp.MustCompile(`\[exit \d+\]`)
	stallTookRe = regexp.MustCompile(`took [\d.]+\s*m?s`)
	stallWsRe   = regexp.MustCompile(`\s+`)
)

// normalizeError reduces an error string to a stable class: lowercased,
// whitespace-collapsed, with volatile tails (exit codes, durations) stripped and
// the result clipped. Two identical validation errors collapse; two failing bash
// commands with different command text stay distinct, so churn does not fire on a
// model genuinely trying different things.
func normalizeError(s string) string {
	s = strings.ToLower(s)
	s = stallExitRe.ReplaceAllString(s, "")
	s = stallTookRe.ReplaceAllString(s, "")
	s = stallWsRe.ReplaceAllString(s, " ")
	return clip(strings.TrimSpace(s), stallDetailMax)
}

func stallNudge(tool string, count int, detail string) string {
	if detail != "" {
		return i18n.P("stall.nudge.error",
			"[loop check] You have called `%s` %d times with the same result: %q. Retrying it unchanged will not help — read the current state (e.g. task_list) or take a different action.",
			tool, count, detail)
	}
	return i18n.P("stall.nudge.repeat",
		"[loop check] You have called `%s` with the same arguments %d times. You already have the result; use what you have or do something different rather than repeat it.",
		tool, count)
}

// SetStallDetection toggles the stuck-loop detector (engine feature
// stuck_loop_detection). Off by default at the core zero value; the shipped
// default lives at the build funnel.
func (a *Agent) SetStallDetection(on bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stallDetect = on
}

// StallDetectionEnabled reports whether the detector is armed. Exported so a host
// can show it and so the build funnel's default is testable — the only place the
// shipped default actually lives (core's zero value is off).
func (a *Agent) StallDetectionEnabled() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.stallDetect
}

func (a *Agent) stallDetectionOn() bool { return a.StallDetectionEnabled() }
