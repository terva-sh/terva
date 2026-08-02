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

// contextPressureBands are the fractions that each warrant saying something
// again. Crossing one is news; sitting inside one is not. 0.85 is
// AutoCompactThreshold, the point the harness intervenes on its own.
var contextPressureBands = []float64{0.70, 0.80, 0.85, 0.90, 0.95}

// contextPressureRepeatEvery re-issues the note after this many requests inside
// the SAME band. Without it a long stretch at 72% would be warned about exactly
// once and then never again, and a model many turns past that reminder behaves
// like one that was never told.
const contextPressureRepeatEvery = 10

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
	for _, t := range contextPressureBands {
		if f >= t {
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
	if band <= a.pressure.band && a.pressure.sinceLast < contextPressureRepeatEvery {
		return ""
	}
	return a.contextPressureText(f)
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
