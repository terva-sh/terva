// Package replay turns a recorded session transcript into a deterministic,
// paced stream of the same core.AgentEvents a live turn produces — the
// forward-emission twin of the context inspector's state reconstruction. A
// Player drives the stream with transport controls (play/pause/step/seek/
// speed); a carrier wraps it as a ctrlproto.WorkspaceService so every client
// renders a replay for free. See docs/proposals/session-player.md.
package replay

import (
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Frame is one synthesized event plus the wall-clock gap to wait before
// emitting it at 1x speed. The Player scales Delay by its speed multiplier;
// a zero Delay emits back-to-back with the previous frame.
type Frame struct {
	Event core.AgentEvent
	Delay time.Duration
	// Reset, when non-nil, replaces the folded transcript with these messages
	// once this frame plays — the compaction resync (effective mode): the
	// EvCompactEnd frame carries the checkpoint summary, so a snapshot at or
	// past it reflects the collapsed transcript, and the carrier re-broadcasts
	// a snapshot mirroring the live daemon's post-compact behaviour.
	Reset []provider.Message
}

// Pace controls a replay's synthetic cadence. The transcript stores final
// messages, not deltas, so the player re-chunks assistant text and spaces the
// events itself; the defaults mirror the TUI's live typewriter (~375 runes/s)
// so a replayed scene reads like a live turn on any client — including ones
// with no render pacer of their own. The Player's speed multiplier scales all
// of these uniformly.
type Pace struct {
	// TextRunes is the rune window per synthesized text delta.
	TextRunes int
	// TextInterval is the gap before each text delta chunk.
	TextInterval time.Duration
	// Think is the gap before a turn's assistant output begins — the "model
	// thinking" beat after a user prompt or a tool round-trip.
	Think time.Duration
	// Tool is the gap before each tool result lands (an execution facsimile).
	Tool time.Duration
	// Compact is the gap the compaction animation holds.
	Compact time.Duration
}

// DefaultPace mirrors the TUI's live streaming cadence (6 runes / 16 ms).
func DefaultPace() Pace {
	return Pace{
		TextRunes:    6,
		TextInterval: 16 * time.Millisecond,
		Think:        350 * time.Millisecond,
		Tool:         200 * time.Millisecond,
		Compact:      500 * time.Millisecond,
	}
}
