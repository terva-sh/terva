package modes

// Replay-mode transport: when the interactive TUI is backed by a replay carrier
// (`terva replay <file>`), a handful of keys drive the recording's playhead
// through the ctrlproto ReplayController — the same verbs a serialized web
// player would send. See docs/proposals/session-player.md.

import (
	"context"
	"fmt"
	"strconv"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui"
)

// replayController returns the replay transport if the TUI is playing back a
// recording, or (nil, false) for a live session.
func (i *Interactive) replayController() (ctrlproto.ReplayController, bool) {
	if i.cfg.Carrier == nil {
		return nil, false
	}
	rc, ok := i.cfg.Carrier.(ctrlproto.ReplayController)
	return rc, ok
}

// handleReplayKey maps transport chords onto the replay carrier and reports
// whether it consumed the key. Only the transport chords are claimed — every
// other key (ctrl+c, esc, scroll, slash commands) falls through to the normal
// handlers — and nothing is claimed while a completion popup is open, so
// /exit and /help still type cleanly.
//
//	space          play / pause
//	→ / ←          step one frame forward / back (pauses)
//	shift+→ / ←    jump to the next / previous turn (pauses)
//	] / [          faster / slower (2× steps, 0.25×–8×)
func (i *Interactive) handleReplayKey(ctx context.Context, k tui.Key) bool {
	rc, ok := i.replayController()
	if !ok {
		return false
	}
	if i.suggest.Active(i.ed.Value()) || i.fileSuggest.Active(i.ed.Value()) {
		return false
	}
	sess := i.CarrierSessionID()
	do := func(action string, p ctrlproto.ReplayControlParams) bool {
		p.Action = action
		if _, err := rc.ReplayControl(ctx, sess, p); err == nil {
			i.invalidate()
		}
		return true
	}
	// The carrier is authoritative for the current playhead — read it live
	// rather than the async-stashed replayState so stepping can't drift.
	state := func() ctrlproto.ReplayState {
		st, _ := rc.ReplayState(ctx, sess)
		return st
	}

	switch {
	case k.Kind == tui.KeyRune && k.Rune == ' ':
		if state().Playing {
			return do("pause", ctrlproto.ReplayControlParams{})
		}
		return do("play", ctrlproto.ReplayControlParams{})
	case k.Kind == tui.KeyRight && k.Shift:
		do("pause", ctrlproto.ReplayControlParams{})
		return do("turn", ctrlproto.ReplayControlParams{Position: 1})
	case k.Kind == tui.KeyLeft && k.Shift:
		do("pause", ctrlproto.ReplayControlParams{})
		return do("turn", ctrlproto.ReplayControlParams{Position: -1})
	case k.Kind == tui.KeyRight:
		do("pause", ctrlproto.ReplayControlParams{})
		return do("step", ctrlproto.ReplayControlParams{})
	case k.Kind == tui.KeyLeft:
		do("pause", ctrlproto.ReplayControlParams{})
		pos := max(state().Position-1, 0)
		return do("seek", ctrlproto.ReplayControlParams{Position: pos})
	case k.Kind == tui.KeyRune && k.Rune == ']':
		return do("speed", ctrlproto.ReplayControlParams{Multiplier: nextSpeed(state().Speed, true)})
	case k.Kind == tui.KeyRune && k.Rune == '[':
		return do("speed", ctrlproto.ReplayControlParams{Multiplier: nextSpeed(state().Speed, false)})
	}
	return false
}

// replayScrubber formats the stashed transport state for the status-bar
// segment (e.g. "▶ 38%  2×"), or "" outside replay / before the first state.
func (i *Interactive) replayScrubber() string {
	if _, ok := i.replayController(); !ok {
		return ""
	}
	i.mu.Lock()
	st := i.replayState
	i.mu.Unlock()
	if st.Total <= 0 {
		return ""
	}
	glyph := "⏸"
	switch {
	case st.Position >= st.Total:
		glyph = "⏹"
	case st.Playing:
		glyph = "▶"
	}
	s := fmt.Sprintf("%s %d%%", glyph, st.Position*100/st.Total)
	if st.Speed > 0 && st.Speed != 1 {
		s += " " + strconv.FormatFloat(st.Speed, 'g', -1, 64) + "×"
	}
	return s
}

// nextSpeed steps the playback multiplier by 2×, clamped to [0.25, 8].
func nextSpeed(cur float64, faster bool) float64 {
	if cur <= 0 {
		cur = 1
	}
	if faster {
		cur *= 2
	} else {
		cur /= 2
	}
	if cur > 8 {
		cur = 8
	}
	if cur < 0.25 {
		cur = 0.25
	}
	return cur
}
