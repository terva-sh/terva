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
	s := &synth{opts: opts}
	for _, row := range rows {
		switch row.Kind {
		case core.ReplayRowMessage:
			s.message(row.Message)
		case core.ReplayRowUsage:
			s.emit(core.EvUsage{Usage: row.Usage, Cumulative: row.Cumulative}, 0)
		case core.ReplayRowCompaction:
			s.compaction(row.Checkpoint)
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
}

func (s *synth) emit(ev core.AgentEvent, d time.Duration) {
	s.frames = append(s.frames, Frame{Event: ev, Delay: d})
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
