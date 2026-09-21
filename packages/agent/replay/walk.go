package replay

import "time"

// The walk: how long a client takes to play a resolution as keystrokes.
//
// A replay resolution names the option that won, and the TUI plays it as a
// cursor walk (packages/agent/modes/interactive_walk.go). The player has to
// hold the next frame until that walk is done, or the tool result lands while
// the cursor is still moving. The timing lives here, on the player's side, so
// the two agree by construction: the client paces its keys by these constants
// and the synth adds the same total to the frame after a resolution.

// WalkStep is the pause between played keys at 1x.
const WalkStep = 320 * time.Millisecond

// WalkTypeStep is the pause between typed runes of a note at 1x.
const WalkTypeStep = 45 * time.Millisecond

// WalkLength is the number of cursor keys that move from row 1 to option
// (1-based) in a list of n rows: down to it, then one past and back when there
// is a row past, else one back and forward, so a first-row answer still shows
// a glance at the second. Zero for a list of one.
func WalkLength(option, n int) int {
	if option <= 0 || n <= 1 {
		return 0
	}
	keys := option - 1
	if option < n || option > 1 {
		keys += 2
	}
	return keys
}

// WalkDuration is how long the client's walk to option takes at 1x, Enter
// included, plus typing a note when there is one. Zero when no option is
// named, which is when the client dismisses instead of walking.
func WalkDuration(option, n int, note string) time.Duration {
	if option <= 0 {
		return 0
	}
	d := time.Duration(WalkLength(option, n)+1) * WalkStep
	if note != "" {
		d += WalkStep + time.Duration(len([]rune(note)))*WalkTypeStep
	}
	return d
}
