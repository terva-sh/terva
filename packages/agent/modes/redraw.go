package modes

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"terva.sh/terva/packages/envcompat"
)

// idleRedrawInterval is the floor between repaints for input-driven
// redraws (keystrokes, scrolling). Kept tight so echo at an idle prompt
// stays instant; the streaming cap (busyRedrawInterval) layers on top of
// it only while a turn is busy.
const idleRedrawInterval = 16 * time.Millisecond

// resolveBusyRedrawInterval returns the minimum interval between repaints
// during a busy (streaming/animating) turn, plus a one-line note when the
// env var overrode the build default (else "").
//
// While a turn streams, the typewriter pacer can drive ~60 paints/sec —
// and each paint costs CPU not just here but in the terminal emulator
// (often more), plus bytes over SSH. LLM text reveals at reading speed,
// so painting it 30x/sec instead of 60x is visually identical while
// halving those frequency-bound costs. The pacer is left alone (reveal
// rate is unchanged); only the repaint frequency is capped.
//
// TERVA_REDRAW_FPS overrides the cap (frames/sec; 0 = uncapped). The
// build default is defaultRedrawFPS: 30 normally, 0 under the terva_pprof
// tag so a profile shows every redundant draw.
func resolveBusyRedrawInterval() (time.Duration, string) {
	fps := defaultRedrawFPS
	note := ""
	if v := envcompat.Get("REDRAW_FPS"); v != "" {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		switch {
		case err != nil || n < 0:
			note = fmt.Sprintf("redraw cap: ignoring invalid TERVA_REDRAW_FPS=%q; using %d fps", v, fps)
		case n == 0:
			fps = 0
			note = "redraw cap: off (TERVA_REDRAW_FPS=0) — every frame paints"
		default:
			fps = n
			note = fmt.Sprintf("redraw cap: %d fps (TERVA_REDRAW_FPS override)", fps)
		}
	}
	return busyIntervalForFPS(fps), note
}

// busyIntervalForFPS maps a target fps to a min repaint interval, never
// tighter than the idle floor — a paint can't outrun the pacer's ~16ms
// cadence anyway, so fps==0 and fps>=~60 both mean "effectively uncapped".
func busyIntervalForFPS(fps int) time.Duration {
	if fps <= 0 {
		return idleRedrawInterval
	}
	d := time.Second / time.Duration(fps)
	if d < idleRedrawInterval {
		return idleRedrawInterval
	}
	return d
}
