package widgets

import (
	"math/rand"
	"time"

	"terva.sh/terva/packages/tui"
)

// Spinner drives the busy animation shown in the status bar while a
// turn is streaming. It rotates through a list of playful status
// messages and a small frame animation.
type Spinner struct {
	frames    []string
	messages  []string
	verbs     []string
	nouns     []string
	interval  time.Duration
	startedAt time.Time
	msgIdx    int

	// rendered is the chosen message with any {verb}/{noun} placeholders
	// filled, computed once at Start so it stays stable for the turn (the
	// fill is random, so re-rolling it every frame would flicker).
	rendered string

	// fixedMsg overrides the rotating message when set. Used for
	// auto-compaction so the spinner clearly says what's happening
	// instead of cycling jokes.
	fixedMsg string
}

// NewSpinner constructs a fresh spinner.
func NewSpinner(th tui.Theme) *Spinner {
	s := &Spinner{}
	s.Configure(th)
	return s
}

func (s *Spinner) Configure(th tui.Theme) {
	s.frames = append([]string(nil), th.SpinnerFrames...)
	if len(s.frames) == 0 {
		s.frames = []string{"⠋", "⠙", "⠚", "⠞", "⠖", "⠦", "⠴", "⠲", "⠳", "⠓"}
	}
	s.messages = append([]string(nil), th.SpinnerMessages...)
	if len(s.messages) == 0 {
		s.messages = []string{"thinking"}
	}
	s.verbs = append([]string(nil), th.FlavorVerbs...)
	s.nouns = append([]string(nil), th.FlavorNouns...)
	interval := th.SpinnerIntervalMS
	if interval <= 0 {
		interval = 80
	}
	s.interval = time.Duration(interval) * time.Millisecond
	if s.msgIdx >= len(s.messages) {
		s.msgIdx = 0
	}
}

// Start resets the spinner to the beginning of its animation and
// picks a random message that stays fixed for the whole run. A
// rotating rollodex of quips during a single turn felt noisy in
// practice — you'd see five different phrases for one
// long-running response, which implies progress that isn't
// actually happening. One stable phrase per turn reads calmer
// and the variety across turns (next Start picks another index)
// still keeps the set fresh over a session.
func (s *Spinner) Start() {
	s.startedAt = time.Now()
	if len(s.messages) == 0 {
		s.messages = []string{"thinking"}
	}
	s.msgIdx = rand.Intn(len(s.messages))
	s.rendered = tui.FillFlavor(s.messages[s.msgIdx], s.verbs, s.nouns)
	s.fixedMsg = ""
}

// StartFixed is like Start but pins the status text to msg for the
// duration of this spinner run. Cleared by the next Start() call.
func (s *Spinner) StartFixed(msg string) {
	s.startedAt = time.Now()
	s.fixedMsg = msg
}

// Frame returns the current spinner glyph for the running animation.
func (s *Spinner) Frame() string {
	if len(s.frames) == 0 {
		return ""
	}
	if s.startedAt.IsZero() {
		return s.frames[0]
	}
	interval := s.interval
	if interval <= 0 {
		interval = 80 * time.Millisecond
	}
	elapsed := time.Since(s.startedAt)
	idx := int(elapsed/interval) % len(s.frames)
	return s.frames[idx]
}

// Message returns the spinner's status text. One random phrase
// per Start call, pinned until the next turn. When the spinner
// was started via StartFixed, the pinned message is returned
// unchanged.
func (s *Spinner) Message() string {
	if s.fixedMsg != "" {
		return s.fixedMsg
	}
	if s.rendered != "" {
		return s.rendered
	}
	if len(s.messages) == 0 {
		return "thinking"
	}
	if s.msgIdx < 0 || s.msgIdx >= len(s.messages) {
		s.msgIdx = 0
	}
	return s.messages[s.msgIdx]
}

// Elapsed returns the wall-clock duration the spinner has been running.
func (s *Spinner) Elapsed() time.Duration {
	if s.startedAt.IsZero() {
		return 0
	}
	return time.Since(s.startedAt).Round(time.Second)
}
