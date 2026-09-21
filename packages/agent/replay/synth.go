package replay

import (
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// Mode selects how a replay treats compaction checkpoints.
type Mode string

const (
	// ModeEffective honors checkpoints: the session plays as it actually was,
	// animating each compaction (the live transcript resets to the summary).
	ModeEffective Mode = "effective"
	// ModeRaw ignores checkpoints: the full original history plays
	// continuously, including turns a compaction later summarized away.
	ModeRaw Mode = "raw"
)

// Options configure a replay. Mode and Pace drive synthesis; Autoplay is a
// carrier playback option (Synthesize ignores it).
type Options struct {
	Mode Mode
	Pace Pace
	// Autoplay starts playback automatically when the first client subscribes —
	// the only correct trigger, since frames emitted before any subscriber joins
	// fan out to nobody. Used by `terva replay`, off for a paused scrubber.
	Autoplay bool
	// Speed is the initial playback multiplier (0.5 = half speed). Zero is
	// the player's default of 1. The transport can change it later.
	Speed float64
}

// Synthesize turns recorded transcript rows into the ordered AgentEvent stream
// a live turn produces, paced for playback. The ordering mirrors the live agent
// loop exactly, per step:
//
//	EvUserMessage → EvTurnStart → EvAssistantStart → (EvTextDelta | tool-use)*
//	→ EvTurnEnd → EvAssistantMessage → EvToolCall* → EvToolResult* → … → EvDone
//
// (EvTurnEnd deliberately precedes EvAssistantMessage, matching agent.go.) It is
// pure and deterministic: the same rows and options always yield the same frames.
//
// A ModeRaw synthesis drops compaction rows entirely; ModeEffective emits the
// compaction animation (EvCompactStart/EvCompactEnd) in place. Both play every
// message row in file order — the effective/raw distinction is only about the
// compaction beat and the transcript resync a carrier layers on top.
func Synthesize(rows []core.ReplayRow, opts Options) []Frame {
	if opts.Pace.TextRunes <= 0 || opts.Pace.TextInterval <= 0 {
		opts.Pace = DefaultPace()
	}
	if opts.Mode == "" {
		opts.Mode = ModeEffective
	}
	if opts.Pace.WaitCap <= 0 {
		opts.Pace.WaitCap = DefaultPace().WaitCap
	}
	s := &synth{opts: opts}
	for _, row := range rows {
		switch row.Kind {
		case core.ReplayRowMessage:
			s.message(row.Message)
		case core.ReplayRowUsage:
			// The live loop emits EvUsage for its own requests only. A
			// sub-agent's row and a host side-channel row are booked
			// total-only and never reach the event stream, so a replay that
			// emitted them moved the gauge on a turn the session never had.
			// The next real turn's cumulative already carries their cost.
			if row.Delegated || row.Source != "" {
				continue
			}
			s.emit(core.EvUsage{Usage: row.Usage, Cumulative: row.Cumulative}, 0)
		case core.ReplayRowCompaction:
			s.compaction(row.Checkpoint)
		case core.ReplayRowPermission:
			s.permission(row.Permission)
		case core.ReplayRowAsk:
			s.ask(row.Ask)
		}
	}
	s.closePrompt()
	return s.frames
}

type synth struct {
	opts       Options
	frames     []Frame
	step       int
	promptOpen bool
	// hold is added to the next frame's delay: the time a client needs to
	// play the resolution just emitted as keystrokes (see walk.go).
	hold time.Duration
}

func (s *synth) emit(ev core.AgentEvent, d time.Duration) {
	s.frames = append(s.frames, Frame{Event: ev, Delay: d + s.hold})
	s.hold = 0
}

func (s *synth) message(m provider.Message) {
	switch m.Role {
	case provider.RoleUser:
		// A synthetic user message (a continuation-gate nudge) rides inside a
		// running prompt and must not open a new one; a genuine prompt does.
		synthetic := m.Meta[core.MetaSynthetic] == "true"
		if !synthetic {
			s.closePrompt()
			s.step = 0
			s.promptOpen = true
		}
		s.emit(core.EvUserMessage{Message: m, Synthetic: synthetic}, 0)
	case provider.RoleAssistant:
		s.assistantTurn(m)
	case provider.RoleTool:
		s.toolResults(m)
	}
}

func (s *synth) assistantTurn(m provider.Message) {
	s.step++
	s.emit(core.EvTurnStart{Step: s.step}, s.opts.Pace.Think)
	s.emit(core.EvAssistantStart{}, 0)
	hasTools := false
	for _, c := range m.Content {
		switch b := c.(type) {
		case provider.TextBlock:
			s.textDeltas(b.Text)
		case provider.ToolCallBlock:
			hasTools = true
			s.emit(core.EvToolUseStart{ID: b.ID, Name: b.Name}, 0)
			if len(b.Arguments) > 0 {
				s.emit(core.EvToolUseArgs{ID: b.ID, Delta: string(b.Arguments)}, 0)
			}
			s.emit(core.EvToolUseEnd{ID: b.ID}, 0)
		}
	}
	stop := provider.StopEnd
	if hasTools {
		stop = provider.StopToolUse
	}
	s.emit(core.EvTurnEnd{Stop: stop}, 0)
	s.emit(core.EvAssistantMessage{Message: m}, 0)
	for _, c := range m.Content {
		if b, ok := c.(provider.ToolCallBlock); ok {
			s.emit(core.EvToolCall{ID: b.ID, Name: b.Name, Args: b.Arguments}, 0)
		}
	}
	// A tool-using turn continues (tool results + a follow-up turn); a
	// text-only turn is terminal and closes the prompt with EvDone.
	if !hasTools {
		s.emit(core.EvDone{}, 0)
		s.promptOpen = false
	}
}

func (s *synth) toolResults(m provider.Message) {
	for _, c := range m.Content {
		if b, ok := c.(provider.ToolResultBlock); ok {
			s.emit(core.EvToolResult{
				ID:     b.CallID,
				Result: core.ToolResult{Content: b.Content, IsError: b.IsError},
			}, s.opts.Pace.Tool)
		}
	}
}

func (s *synth) compaction(checkpoint []provider.Message) {
	if s.opts.Mode == ModeRaw {
		return
	}
	// The checkpoint summary replaces the transcript when the compaction
	// completes — tag the end frame so the fold/snapshot collapse to it.
	reset := checkpoint
	if reset == nil {
		reset = []provider.Message{}
	}
	s.emit(core.EvCompactStart{Reason: "compaction"}, s.opts.Pace.Think)
	s.frames = append(s.frames, Frame{
		Event: core.EvCompactEnd{},
		Delay: s.opts.Pace.Compact,
		Reset: reset,
	})
}

// closePrompt emits the terminal EvDone if a prompt is still open — the last
// assistant turn used tools but the recording ended before its results/reply,
// or the file ends on a bare user message. Idempotent.
func (s *synth) closePrompt() {
	if s.promptOpen {
		s.emit(core.EvDone{}, 0)
		s.promptOpen = false
	}
}

func (s *synth) textDeltas(text string) {
	runes := []rune(text)
	n := s.opts.Pace.TextRunes
	for i := 0; i < len(runes); i += n {
		end := min(i+n, len(runes))
		s.emit(core.EvTextDelta{Delta: string(runes[i:end])}, s.opts.Pace.TextInterval)
	}
}

// permission plays a tool-approval exchange: the prompt lands at once (the
// tool call that needs it is already on screen), and the decision follows
// after the wait the person actually took, clamped so a demo shows the pause
// without reproducing a lunch break.
func (s *synth) permission(rec core.PermissionRecord) {
	s.emit(EvPermissionRequest{CallID: rec.CallID, Tool: rec.Tool, Preview: rec.Preview}, 0)
	resolved := EvPermissionResolved{CallID: rec.CallID, Allow: rec.Allow, Reason: rec.Reason, Scope: rec.Scope}
	s.emit(resolved, s.wait(rec.Waited))
	s.hold = WalkDuration(permissionOption(resolved), 5, "")
}

// ask plays a question exchange the same way.
func (s *synth) ask(rec core.AskRecord) {
	s.emit(EvAskRequest{AskID: rec.AskID, Questions: rec.UserQuestions()}, 0)
	qs, as := rec.UserQuestions(), rec.UserAnswers()
	s.emit(EvAskResolved{AskID: rec.AskID, Answers: as}, s.wait(rec.Waited))
	if opt, note := askOption(qs, as); opt > 0 {
		s.hold = WalkDuration(opt, len(qs[0].Options), note)
	}
}

// wait clamps a recorded human interval to [Think, WaitCap].
func (s *synth) wait(d time.Duration) time.Duration {
	if d < s.opts.Pace.Think {
		return s.opts.Pace.Think
	}
	if d > s.opts.Pace.WaitCap {
		return s.opts.Pace.WaitCap
	}
	return d
}
