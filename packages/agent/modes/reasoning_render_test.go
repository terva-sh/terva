package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui/tuitest"
)

// The live reasoning block, driven end to end: every row asserted here arrives
// as a wire event, so the test exercises the same path a daemon drives and
// cannot pass off local state the production build never sets.

// reasoningTurn boots a bound session and gets it as far as a running turn,
// ready for reasoning_delta. newFakeCarrier leaves stream nil, so it is made
// here — sending on the nil one blocks forever rather than failing.
func reasoningTurn(t *testing.T) (*harness, *fakeCarrier) {
	t.Helper()
	fc := newFakeCarrier()
	fc.stream = make(chan ctrlproto.Event, 16)
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})
	h.term.Type("go\r")
	recv(t, fc.prompts, "dispatched prompt")
	fc.stream <- conv(core.WireEvent{Type: "turn_start", Step: 1})
	fc.stream <- conv(core.WireEvent{Type: "assistant_start"})
	return h, fc
}

// A reasoning delta paints its own row and leaves the spinner's own message
// alone — the two are different voices and the spinner deliberately holds ONE
// phrase per turn.
func TestReasoningRowRendersBesideTheSpinner(t *testing.T) {
	h, fc := reasoningTurn(t)
	fc.stream <- conv(core.WireEvent{Type: "reasoning_delta", Delta: "**Inspecting commit before push**"})

	// The provider's markup does not survive to the screen.
	h.waitText("Inspecting commit before push")
	h.waitScreen("no bold markers on screen", func(s *tuitest.Screen) bool {
		return !strings.Contains(s.Text(), "**")
	})

	t.Log("\n----- SCREEN: reasoning row live -----\n" + h.term.Screen().Text())
}

// Sections ACCUMULATE: the block grows as the model thinks, and an earlier
// section stays on screen above the newer one.
//
// 🪤 This REVERSES the old rule, and the old rule was not arbitrary. The display
// was a single row wedged between the transcript and the editor, so it kept only
// the current section because a second line would have pushed the input off the
// screen. A block has room to be a log, and a thought that vanished the instant
// the model moved on was one the user never got to read — which is the whole
// complaint this block answers. The cap (LiveThinkingTailLines) is what now
// holds the height, not the discarding of everything but the last section.
func TestReasoningSectionsAccumulateInTheBlock(t *testing.T) {
	h, fc := reasoningTurn(t)
	fc.stream <- conv(core.WireEvent{Type: "reasoning_delta", Delta: "**Reading the config**"})
	h.waitText("Reading the config")
	fc.stream <- conv(core.WireEvent{Type: "reasoning_delta", Delta: "\n\n**Editing the handler**"})
	h.waitText("Editing the handler")

	screen := h.term.Screen().Text()
	first := strings.Index(screen, "Reading the config")
	second := strings.Index(screen, "Editing the handler")
	if first < 0 {
		t.Fatalf("the earlier section must stay visible once a newer one arrives:\n%s", screen)
	}
	if second < first {
		t.Errorf("sections must render in arrival order, earliest first:\n%s", screen)
	}

	t.Log("\n----- SCREEN: sections accumulate -----\n" + screen)
}

// The status bar reports the level the NEXT turn will run at, so a session
// override has to reach it.
//
// 🪤 It read i.cfg.Reasoning — the GLOBAL — which made it wrong for exactly the
// sessions someone had gone out of their way to change with /thinking: the turn
// ran at the override while the bar went on naming the old level, and the only
// surface that told the truth was /status. The authoritative read is a
// ResumeSession round trip and this is a per-frame render, so the value is
// cached (carrierReasoning) rather than fetched here.
func TestStatusBarShowsTheSessionThinkingOverride(t *testing.T) {
	fc := newFakeCarrier()
	fc.infos = map[string]ctrlproto.SessionInfo{"s1": {ID: "s1", Reasoning: "low"}}
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
		cfg.Reasoning = "maximum" // the GLOBAL setting
	})

	// Precondition: with no override applied yet, the bar names the global.
	h.waitText("maximum")

	// The same call /thinking makes.
	h.i.applyReasoningSelection("low")

	h.waitScreen("status bar names the session override", func(s *tuitest.Screen) bool {
		return strings.Contains(s.Text(), "low")
	})
	h.waitScreen("status bar stops naming the global", func(s *tuitest.Screen) bool {
		return !strings.Contains(s.Text(), "maximum")
	})

	t.Log("\n----- SCREEN: session override in the bar -----\n" + h.term.Screen().Text())
}

// The row is gone when the turn is: a thought left on screen past the work it
// narrated reads as a step still running.
func TestReasoningRowClearsWhenTheTurnEnds(t *testing.T) {
	h, fc := reasoningTurn(t)
	fc.stream <- conv(core.WireEvent{Type: "reasoning_delta", Delta: "**Still working**"})
	h.waitText("Still working")

	fc.stream <- conv(core.WireEvent{
		Type: "assistant_message",
		Message: &core.WireMessage{Role: "assistant", Content: []core.WireBlock{
			{Type: "text", Text: "all done"},
		}},
	})
	fc.stream <- conv(core.WireEvent{Type: "turn_end"})

	h.waitGone("Still working")
	t.Log("\n----- SCREEN: after turn_end -----\n" + h.term.Screen().Text())
}
