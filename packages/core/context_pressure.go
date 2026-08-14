package core

// The context-pressure note used to be a LEVEL trigger: past
// ContextWarnFraction it rode every single request until a compaction dropped
// the transcript back under the line. Measured on a real session, that was 74
// of 407 requests — 18% — carrying the same 229-byte text with only the
// percentage moving, and the model started narrating its context budget back at
// the user and spending turns polling terva_status.
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
//     that never changes. The advice is graduated per band now — see
//     contextPressureAdvice in tail.go.
//   - It had no do-not-reply guard, and "narrating its context budget back at
//     the user" is that failure exactly: a last-in-turn ephemeral note winning
//     the reply away from the user's question. Its sibling — the inactive-groups
//     note — measured 0-of-20 final answers before prohibition-first wording and
//     20-of-20 after. The note leads with the same guard now.
//
// The third half was never in this file at all: terva_status's own description
// and the system prompt's status hint both told the model to call the tool to
// decide whether to summarize, from the CACHED prefix, on 100% of requests. The
// note going quiet could not stop a model that had standing instructions to
// poll. See StatusTool.Description and system.status_tool_hint.

// pressureBand is one rung of the ladder: the fraction that enters it, and how
// often to say so again while the model sits there.
type pressureBand struct {
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

// contextPressureBands is the ladder. Crossing a rung is news; sitting on one is
// not.
//
// The rungs are closer together than they look like they should be at the
// bottom for a reason: the old ladder ran 0.70 then 0.80, and a model climbing
// 71% to 79% burned eight points of window — the single largest stretch in the
// table — hearing nothing but the flat reminder. 0.78 splits it.
//
// Two rungs are load-bearing and pinned by TestLadderMatchesPolicy: the first
// must be ContextWarnFraction, since commitContextPressure clears against it,
// and 0.85 must be AutoCompactThreshold, the point the harness intervenes on
// its own. With auto-compaction ON the top rung is nearly unreachable — the
// harness compacts at 0.85 when the turn ends — so 0.92 is really the
// AutoCompactOff ladder, where nothing intervenes and the model self-manages
// all the way to the wall.
var contextPressureBands = []pressureBand{
	{at: 0.70, repeatEvery: 15},
	{at: 0.78, repeatEvery: 12},
	{at: 0.85, repeatEvery: 8},
	{at: 0.92, repeatEvery: 5},
}

// contextPressureRepeatEvery is band's reminder interval. band is 1-based, as
// contextPressureBand returns it; 0 is below the ladder entirely and has no
// interval to give.
func contextPressureRepeatEvery(band int) int {
	if band <= 0 || band > len(contextPressureBands) {
		return 0
	}
	return contextPressureBands[band-1].repeatEvery
}

// contextPressureClearMargin is the hysteresis below ContextWarnFraction that
// the gauge must fall past before the tracker forgets what it has announced.
// Clearing exactly at the threshold is what let a jittering gauge re-announce
// the same band over and over; a compaction moves the number far more than this
// margin, so a real recovery still resets cleanly.
const contextPressureClearMargin = 0.03

// pressureTracker is the cadence state. It carries no lock for the same reason
// stallTracker does not: it is touched only on the turn goroutine — composeTail
// peeks it while composing, and oneTurn commits after the request is built.
type pressureTracker struct {
	// band is the highest band already announced; 0 means nothing has been.
	band int
	// sinceLast counts requests composed since the note last rode one, so a
	// model sitting in one band still gets an occasional reminder.
	sinceLast int
}

// contextPressureBand reports how many thresholds f has reached; 0 is "below
// the warning line entirely".
func contextPressureBand(f float64) int {
	band := 0
	for _, b := range contextPressureBands {
		if f >= b.at {
			band++
		}
	}
	return band
}

// contextFraction is the gauge both peek and commit read. Returns ok=false when
// the window is unknown or nothing has been measured yet, which must not be
// mistaken for "plenty of room".
func (a *Agent) contextFraction() (float64, bool) {
	used, window := a.ContextUsage()
	if window <= 0 || used <= 0 {
		return 0, false
	}
	return float64(used) / float64(window), true
}

// peekContextPressureNote returns the note this request should carry, or "".
// Side-effect free, because oneTurn is re-entered per retry attempt and a
// retried request must carry the same tail as the attempt it replaces —
// spending the cadence here would let a retry storm burn the reminder budget on
// requests the model never saw.
func (a *Agent) peekContextPressureNote() string {
	f, ok := a.contextFraction()
	if !ok {
		return ""
	}
	band := contextPressureBand(f)
	if band == 0 {
		return ""
	}
	// A newly crossed band is always worth saying; otherwise only on the
	// repeat interval. Note this compares against the highest band ANNOUNCED,
	// so a gauge that dips and recovers within the same band stays quiet.
	//
	// The interval is the CURRENT band's, not the announced one's: a transcript
	// that fell back to 71% after a partial relief is a 71% situation, and
	// reminding it at 0.92's urgent cadence would describe a high-water mark
	// rather than where the model actually is.
	if band <= a.pressure.band && a.pressure.sinceLast < contextPressureRepeatEvery(band) {
		return ""
	}
	return a.contextPressureText(f, band)
}

// commitContextPressure records what the composed request actually carried.
// delivered comes from the tail itself rather than from re-deciding, so the
// bookkeeping can never disagree with what the model was shown.
func (a *Agent) commitContextPressure(delivered bool) {
	f, ok := a.contextFraction()
	if !ok {
		return
	}
	// Genuinely relieved — a compaction landed, or the window grew. Forget the
	// announced band so the next climb is reported from scratch.
	if f < ContextWarnFraction-contextPressureClearMargin {
		a.pressure = pressureTracker{}
		return
	}
	if delivered {
		a.pressure.band = contextPressureBand(f)
		a.pressure.sinceLast = 0
		return
	}
	a.pressure.sinceLast++
}
