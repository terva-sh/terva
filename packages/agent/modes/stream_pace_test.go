package modes

import (
	"strings"
	"testing"
)

// TestPaceRatePolicy pins the three regimes the rate has to cover.
func TestPaceRatePolicy(t *testing.T) {
	tests := []struct {
		name    string
		pending int
		phase   streamPhase
		want    int
	}{
		{"nothing queued paints nothing", 0, streamLive, 0},
		{"a single rune still moves", 1, streamLive, 1},
		{"a short buffer trickles", 20, streamLive, 1},
		{"a fat chunk spreads over the horizon", 117, streamLive, 5},
		{"a deep buffer catches up, capped", 100_000, streamLive, paceMaxRate},
		{"the tail drains at the typewriter rate", 117, streamFlushing, paintPaceRate},
		{"a drained flush reports nothing to paint", 0, streamFlushing, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newStreamState()
			s.phase = tc.phase
			s.pending = make([]rune, tc.pending)
			if got := s.paceRate(); got != tc.want {
				t.Errorf("paceRate() with %d pending in phase %v = %d, want %d",
					tc.pending, tc.phase, got, tc.want)
			}
		})
	}
}

// arrival is one provider delta: how many ticks after the stream opened it
// lands, and how many runes it carries.
type arrival struct {
	tick  int
	runes int
}

// anthropicOAuthArrivals is a real, measured stream: 225 runes of Claude
// Sonnet 4.5 over the subscription OAuth path, delivered in FOUR deltas about
// 460ms apart (~29 ticks at 16ms). Coarse and infrequent — the shape the pacer
// exists to hide.
var anthropicOAuthArrivals = []arrival{{0, 3}, {29, 117}, {58, 56}, {87, 49}}

// codexArrivals is the same prompt on openai-codex: ~155 runes in 31 deltas of
// ~5 runes about 20ms apart. Near-continuous already — the pacer must not make
// this one worse.
func codexArrivals() []arrival {
	out := make([]arrival, 0, 31)
	for i := range 31 {
		out = append(out, arrival{tick: i * 2, runes: 5})
	}
	return out
}

// runPace replays an arrival schedule through the real pacer and reports, for
// every tick, how many runes became visible. Deltas land at the top of their
// tick, then the pacer runs — exactly the order runPacer sees them.
//
// Reveal is counted as what leaves the pending buffer, not what is in `painted`:
// draining the last rune of a flush calls reset(), which clears the painted
// text, so reading it back at the end would report nothing was ever shown.
func runPace(t *testing.T, arrivals []arrival) (painted []int, total int) {
	t.Helper()
	s := newStreamState()
	s.beginTurn()

	byTick := map[int]int{}
	last := 0
	for _, a := range arrivals {
		byTick[a.tick] += a.runes
		total += a.runes
		if a.tick > last {
			last = a.tick
		}
	}

	shown := 0
	// Generous ceiling: enough ticks for the tail to drain at the flush rate.
	for tick := 0; tick < last+total+paceDrainTicks*4; tick++ {
		if n, ok := byTick[tick]; ok {
			s.appendDelta(strings.Repeat("x", n))
		}
		if tick == last {
			// The provider's terminal frame: the message is complete, so the
			// pacer owns the reveal of whatever is still queued.
			s.finishMessage()
		}
		before := len(s.pending)
		s.paceTick(s.paceRate())
		n := before - len(s.pending)
		if n == 0 && s.phase == streamIdle {
			// The terminal tick: the flush drained on the PREVIOUS tick and this
			// one only retires the state. It reveals nothing by construction, so
			// recording it would look like a stall.
			break
		}
		shown += n
		painted = append(painted, n)
	}
	if shown != total {
		t.Fatalf("pacer revealed %d runes, want all %d", shown, total)
	}
	return painted, total
}

// TestPacerBridgesCoarseProviderGaps is the regression test for the lumpy
// Anthropic reveal.
//
// The old pacer drained at a FIXED 6 runes/tick. Against the measured OAuth
// stream that empties a 117-rune chunk in ~20 ticks and then paints nothing at
// all for the ~9 remaining ticks before the next one lands — text, stall, text,
// stall. Here we assert the property directly: once the pacer has a real buffer
// to work with, it paints on EVERY tick until the reply is done. A fixed rate
// cannot satisfy this, so a revert fails the test rather than merely looking
// different.
//
// The very first gap is exempt and always will be: 3 runes arrive, then the
// provider is silent for ~460ms. There is nothing buffered to bridge it with.
func TestPacerBridgesCoarseProviderGaps(t *testing.T) {
	painted, _ := runPace(t, anthropicOAuthArrivals)

	const firstFatChunk = 29 // the tick the buffer first has something to spend
	var stalls []int
	for tick := firstFatChunk; tick < len(painted); tick++ {
		if painted[tick] == 0 {
			stalls = append(stalls, tick)
		}
	}
	if len(stalls) > 0 {
		t.Errorf("pacer stalled on %d tick(s) mid-reveal (ticks %v); a coarse provider must still paint continuously",
			len(stalls), stalls)
	}
}

// TestPacerNeverDumps guards the other direction: smoothing a coarse provider
// must not turn into "paint it all at once". No single tick may reveal more
// than the catch-up cap.
func TestPacerNeverDumps(t *testing.T) {
	for _, tc := range []struct {
		name     string
		arrivals []arrival
	}{
		{"anthropic oauth", anthropicOAuthArrivals},
		{"openai codex", codexArrivals()},
		{"a whole reply in one delta", []arrival{{0, 2000}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			painted, _ := runPace(t, tc.arrivals)
			for tick, n := range painted {
				if n > paceMaxRate {
					t.Errorf("tick %d revealed %d runes at once (cap is %d)", tick, n, paceMaxRate)
				}
			}
		})
	}
}

// TestPacerKeepsUpWithAFineProvider pins the no-regression half: codex already
// drip-streams, and the jitter buffer must not hold it back. The buffer is
// allowed to sit ~one horizon behind the model (that is the trade), but the
// reply must still finish promptly once the last delta lands.
func TestPacerKeepsUpWithAFineProvider(t *testing.T) {
	arrivals := codexArrivals()
	painted, total := runPace(t, arrivals)

	lastArrival := arrivals[len(arrivals)-1].tick
	tail := len(painted) - lastArrival
	// The tail drains at the flush rate, so it is bounded by the buffer the
	// horizon allows (~one horizon of arrivals) plus a tick of slack.
	maxTail := paceDrainTicks + total/paintPaceRate
	if tail > maxTail {
		t.Errorf("pacer took %d ticks past the last delta to finish; want <= %d "+
			"(a fine-grained provider must not be throttled)", tail, maxTail)
	}
}
