// Package contextpressure tells the model how full its context window is, on
// the ephemeral tail, graduated by band and repeated on an interval.
//
// The note used to be a LEVEL trigger: past the warning fraction it rode every
// single request until a compaction dropped the transcript back under the line.
// Measured on a real session, that was 74 of 407 requests — 18% — carrying the
// same 229-byte text with only the percentage moving, and the model started
// narrating its context budget back at the user and spending turns polling
// terva_status.
//
// It also FLAPPED. The gauge reads the most recent request's input count, which
// does not move monotonically, so a transcript hovering near the threshold
// crossed it repeatedly and the note appeared, vanished and returned — the
// least useful possible cadence, since a warning that comes and goes reads as
// noise rather than a state.
//
// What the model actually needs is: tell me when I enter a new band, remind me
// occasionally while I stay there, and stop entirely once a compaction has
// genuinely relieved the pressure.
//
// The band ladder fixed the CADENCE and left two halves of the same symptom
// standing, both closed later:
//
//   - The note said the same thing at every height. Entering at 70% read as
//     urgently as arriving at 86%, so it overstated the early case and
//     understated the late one, and a model cannot calibrate against a warning
//     that never changes. The host's words are graduated per band now, which is
//     why a Noter is told the band.
//   - It had no do-not-reply guard, and "narrating its context budget back at
//     the user" is that failure exactly: a last-in-turn ephemeral note winning
//     the reply away from the user's question. Its sibling — the inactive-groups
//     note — measured 0-of-20 final answers before prohibition-first wording and
//     20-of-20 after. The note leads with the same guard now.
//
// The third half was never in this package at all: terva_status's own
// description and the system prompt's status hint both told the model to call
// the tool to decide whether to summarize, from the CACHED prefix, on 100% of
// requests. The note going quiet could not stop a model that had standing
// instructions to poll. See StatusTool.Description and system.status_tool_hint.
//
// It is a component, not part of the engine (decision 0021). A host that wants
// it holds a Tracker and connects it in three places, as with shellresult:
//
//   - Its assembler adds Segment() to every frame.
//   - Its assembler implements core.TailDeliveryObserver and forwards the IDs to
//     TailDelivered, which advances the cadence.
//   - As a component, Bind gives the tracker the agent, whose ContextUsage is
//     the gauge and whose CompactionPolicy words the note.
package contextpressure

import (
	"sync"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/compactprose"
)

// ID is the segment's tag, and so its tail block ID in events and recorded
// tail rows. It was core.TailPressure while this lived in the engine, and kept
// its value so recorded rows read the same.
const ID = "pressure"

// WarnFraction is the context-window fraction at which the model is first
// warned. Earlier than core.AutoCompactThreshold on purpose: the band between
// the two is the model's window to wrap up or get economical before the
// harness force-compacts.
const WarnFraction = 0.70

// State is what the tracker knows when the note is due. The tracker decides
// WHEN the note rides a request; the host decides what it says.
type State struct {
	// Fraction is the share of the window the last request used.
	Fraction float64
	// Band is the rung of the ladder the fraction sits on, from 1 (just past
	// WarnFraction) to 4 (close to the wall). The third rung is
	// core.AutoCompactThreshold.
	Band int
	// Used and Window are token counts.
	Used, Window int
	// Compacts reports whether the compaction policy compacts on its own at any
	// automatic point, so the note can say whether relief is coming.
	Compacts bool
}

// Noter is implemented by a core.CompactionPolicy that words its own
// context-pressure note. An empty note sends nothing. A policy that does not
// implement it gets compactprose.PressureNote.
type Noter interface {
	PressureNote(State) string
}

// band is one rung of the ladder: the fraction that enters it, and how often
// to say so again while the model sits there.
type band struct {
	at float64
	// repeatEvery re-issues the note after this many requests inside the SAME
	// band. Without it a long stretch at 72% would be warned about exactly once
	// and then never again, and a model many turns past that reminder behaves
	// like one that was never told.
	//
	// It TIGHTENS as the ceiling approaches, because the interval is really a
	// bet on how long the model has to react: at 0.70 that is a whole phase of
	// work, at 0.92 it is a few requests. A flat interval spends the same words
	// on both and is wrong at one end or the other.
	repeatEvery int
}

// bands is the ladder. Crossing a rung is news; sitting on one is not.
//
// The rungs are closer together than they look like they should be at the
// bottom for a reason: the old ladder ran 0.70 then 0.80, and a model climbing
// 71% to 79% burned eight points of window — the single largest stretch in the
// table — hearing nothing but the flat reminder. 0.78 splits it.
//
// Two rungs are load-bearing and pinned by TestLadderMatchesPolicy: the first
// must be WarnFraction, since TailDelivered clears against it, and 0.85 must be
// core.AutoCompactThreshold, the point the harness intervenes on its own. With
// auto-compaction ON the top rung is nearly unreachable — the harness compacts
// at 0.85 when the turn ends — so 0.92 is really the AutoCompactOff ladder,
// where nothing intervenes and the model self-manages all the way to the wall.
var bands = []band{
	{at: 0.70, repeatEvery: 15},
	{at: 0.78, repeatEvery: 12},
	{at: 0.85, repeatEvery: 8},
	{at: 0.92, repeatEvery: 5},
}

// repeatEvery is b's reminder interval. b is 1-based, as bandOf returns it; 0
// is below the ladder entirely and has no interval to give.
func repeatEvery(b int) int {
	if b <= 0 || b > len(bands) {
		return 0
	}
	return bands[b-1].repeatEvery
}

// clearMargin is the hysteresis below WarnFraction that the gauge must fall
// past before the tracker forgets what it has announced. Clearing exactly at
// the threshold is what let a jittering gauge re-announce the same band over
// and over; a compaction moves the number far more than this margin, so a real
// recovery still resets cleanly.
const clearMargin = 0.03

// bandOf reports how many rungs f has reached; 0 is "below the warning line
// entirely".
func bandOf(f float64) int {
	n := 0
	for _, b := range bands {
		if f >= b.at {
			n++
		}
	}
	return n
}

// Tracker is the cadence: which band has been announced, and how long since.
// It is safe for concurrent use; the engine calls it from the turn loop, and a
// frame preview may assemble from another goroutine.
type Tracker struct {
	mu    sync.Mutex
	agent *core.Agent
	// band is the highest band already announced; 0 means nothing has been.
	band int
	// sinceLast counts requests reported since the note last rode one, so a
	// model sitting in one band still gets an occasional reminder.
	sinceLast int
}

// Bind implements core.Binder: it gives the tracker the agent it reports on,
// and until then Segment is empty. A host passes the tracker to
// core.WithComponent, which calls Bind. A tracker serves one agent.
func (t *Tracker) Bind(a *core.Agent) {
	t.mu.Lock()
	t.agent = a
	t.mu.Unlock()
}

var _ core.Binder = (*Tracker)(nil)

// Segment is the Volatile segment for the next request: the note, or empty.
// Side-effect free, because the engine re-assembles per retry attempt and a
// retried request must carry what the attempt it replaces did — spending the
// cadence here would let a retry storm burn the reminder budget on requests
// the model never saw.
//
// It reads the agent, so the host must call it outside the agent's lock, as
// the engine calls Assemble.
func (t *Tracker) Segment() core.Segment {
	seg := core.Segment{Stability: core.Volatile, Tag: ID}
	t.mu.Lock()
	a, announced, since := t.agent, t.band, t.sinceLast
	t.mu.Unlock()
	if a == nil {
		return seg
	}
	used, window := a.ContextUsage()
	f, ok := fraction(used, window)
	if !ok {
		return seg
	}
	b := bandOf(f)
	if b == 0 {
		return seg
	}
	// A newly crossed band is always worth saying; otherwise only on the
	// repeat interval. This compares against the highest band ANNOUNCED, so a
	// gauge that dips and recovers within the same band stays quiet.
	//
	// The interval is the CURRENT band's, not the announced one's: a transcript
	// that fell back to 71% after a partial relief is a 71% situation, and
	// reminding it at 0.92's urgent cadence would describe a high-water mark
	// rather than where the model actually is.
	if b <= announced && since < repeatEvery(b) {
		return seg
	}
	policy := a.CompactionPolicy()
	seg.Content = note(policy, State{Fraction: f, Band: b, Used: used, Window: window, Compacts: compactsOnItsOwn(policy)})
	return seg
}

// TailDelivered advances the cadence from what the request actually carried,
// rather than by re-deciding, so the bookkeeping cannot disagree with what the
// model was shown. A continue turn, which suppresses the whole tail, counts as
// not delivered instead of silently spending the interval.
func (t *Tracker) TailDelivered(ids []string) {
	t.mu.Lock()
	a := t.agent
	t.mu.Unlock()
	if a == nil {
		return
	}
	f, ok := fraction(a.ContextUsage())
	if !ok {
		return
	}
	delivered := false
	for _, id := range ids {
		if id == ID {
			delivered = true
			break
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// Genuinely relieved — a compaction landed, or the window grew. Forget the
	// announced band so the next climb is reported from scratch.
	if f < WarnFraction-clearMargin {
		t.band, t.sinceLast = 0, 0
		return
	}
	if delivered {
		t.band = bandOf(f)
		t.sinceLast = 0
		return
	}
	t.sinceLast++
}

// fraction is the gauge. ok is false when the window is unknown or nothing has
// been measured yet, which must not be mistaken for "plenty of room".
func fraction(used, window int) (float64, bool) {
	if window <= 0 || used <= 0 {
		return 0, false
	}
	return float64(used) / float64(window), true
}

// compactsOnItsOwn reports whether p would compact at any automatic point that
// fires on a full window: before, during or after a turn. The note tells the
// model whether anything will relieve the window for it, and a policy that
// compacts only mid-turn still does.
func compactsOnItsOwn(p core.CompactionPolicy) bool {
	for _, point := range []core.CompactPoint{core.CompactBeforeTurn, core.CompactMidTurn, core.CompactAfterTurn} {
		if p.Decide(core.CompactionState{Point: point, Fraction: 1, Messages: int(^uint(0) >> 1)}).Compact {
			return true
		}
	}
	return false
}

// note words the note through the policy, or the neutral default.
func note(p core.CompactionPolicy, s State) string {
	if n, ok := p.(Noter); ok {
		return n.PressureNote(s)
	}
	return compactprose.PressureNote(int(s.Fraction*100), s.Used, s.Window, s.Compacts)
}
