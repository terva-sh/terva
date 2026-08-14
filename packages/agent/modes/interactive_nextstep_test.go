package modes

// Stage 3 of docs/proposals/idle-suggestions.md: the idle trigger.
//
// Every test here drives the REAL path — a real `done` event arms it, the real
// animation tick fires it, the real carrier type-assertion reaches the daemon
// — because the proposal's warning about this area is that a guard whose setup
// never reaches the code path passes vacuously, and that has already happened
// twice nearby.
//
// The 30-second window is not waited out. Instead the two timestamps the
// trigger reads are moved into the past, which exercises the same comparison
// the same way without pinning the test to a wall clock. What is NOT faked is
// the arming: that comes from a wire event every time.

import (
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui/tuitest"
)

// nextStepHarness boots a TUI whose carrier can answer the ask, with the
// feature switched on (stage 4 owns the setting; without this the trigger is
// inert and every test below would pass for the wrong reason).
func nextStepHarness(t *testing.T, line string) (*harness, *fakeCarrier) {
	t.Helper()
	fc := newFakeCarrier()
	fc.stream = make(chan ctrlproto.Event, 16)
	fc.nextSteps = make(chan struct{}, 4)
	fc.nextStepLine = line
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})
	h.i.mu.Lock()
	h.i.nextStepEnabled = true
	h.i.mu.Unlock()
	return h, fc
}

// replyLanded plays a finished assistant turn and waits for the trigger to arm
// off it — the positive sync point every test below needs before it can say
// anything about what happens next.
func replyLanded(t *testing.T, h *harness, fc *fakeCarrier) {
	t.Helper()
	fc.stream <- conv(core.WireEvent{Type: "done"})
	h.waitScreen("the reply to arm an offer", func(*tuitest.Screen) bool {
		h.i.mu.Lock()
		defer h.i.mu.Unlock()
		return !h.i.nextStepArmedAt.IsZero()
	})
}

// ageArm back-dates a standing arm so the next tick sees the window elapsed.
// Only an arm that EXISTS is moved: a zero arm means nothing is waiting.
func ageArm(h *harness) {
	h.i.mu.Lock()
	if !h.i.nextStepArmedAt.IsZero() {
		h.i.nextStepArmedAt = time.Now().Add(-2 * nextStepIdle)
	}
	h.i.mu.Unlock()
}

// ageInput back-dates the last keystroke, which is the other half of the
// silence. Kept separate from ageArm because one test needs a RECENT keystroke
// and an old arm at the same time.
func ageInput(h *harness) {
	h.i.mu.Lock()
	h.i.lastInputAt = time.Now().Add(-2 * nextStepIdle)
	h.i.mu.Unlock()
}

// goQuiet is the pair: an old arm and an old keystroke.
func goQuiet(h *harness) {
	ageArm(h)
	ageInput(h)
}

// composer reads the editor, which is MAIN-LOOP-ONLY state. Reading it from
// the test goroutine is a data race the -race suite will find even when the
// plain run is green — it did, on the first CI attempt for this stage. Every
// look at the editor goes through the main loop, the way production callers
// must.
type composerState struct {
	value string
	ghost string
	empty bool
}

func composer(h *harness) composerState {
	var out composerState
	done := make(chan struct{})
	h.i.runOnMain(func() {
		out = composerState{value: h.i.ed.Value(), ghost: h.i.ed.Ghost(), empty: h.i.ed.IsEmpty()}
		close(done)
	})
	<-done
	return out
}

// noAsk fails if the daemon is asked within a window covering several 120ms
// animation ticks.
//
// It keeps ageing the arm throughout rather than once at the top, and that is
// load-bearing rather than tidy. Ageing once races the event that arms: push a
// "done" and back-date immediately and the arm lands AFTERWARDS, freshly
// stamped, so no ask follows within the window and the test passes whatever the
// code does. Mutation caught exactly that — the enabled-gate guard survived
// having its gate deleted. Ageing on every pass means an arm that appears late
// is still old by the next tick, so a suppression that has stopped working has
// nowhere to hide.
func noAsk(t *testing.T, h *harness, fc *fakeCarrier, why string) {
	t.Helper()
	deadline := time.Now().Add(700 * time.Millisecond)
	for time.Now().Before(deadline) {
		ageArm(h)
		select {
		case <-fc.nextSteps:
			t.Fatalf("terva asked for a suggestion while %s", why)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// The whole path: a reply lands, the user goes quiet, and the offered line is
// on the composer where they type — not in the buffer, and not sent.
func TestAnIdleUserIsOfferedANextStep(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	replyLanded(t, h, fc)
	goQuiet(h)

	recv(t, fc.nextSteps, "the daemon to be asked")
	h.waitText("run the tests")

	// Offered, not typed: nothing has been sent and the composer is still empty
	// as far as everything that asks is concerned.
	if c := composer(h); !c.empty {
		t.Fatalf("the offer went into the buffer: %q", c.value)
	}
	select {
	case p := <-fc.prompts:
		t.Fatalf("a suggestion was SENT without the user doing anything: %q", p)
	default:
	}
}

// Once per reply. The arm is spent when the ask goes out, so a user who walks
// away does not pay for a completion every 120ms until they come back.
func TestTheOfferIsMadeOncePerReply(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	replyLanded(t, h, fc)
	goQuiet(h)
	recv(t, fc.nextSteps, "the first ask")

	// Still idle, still empty, still quiet — and no second ask.
	goQuiet(h)
	noAsk(t, h, fc, "still idle after one offer")

	// A new reply is a new arm, which is what makes this once-per-reply rather
	// than once-per-session.
	clearGhost := make(chan struct{})
	h.i.runOnMain(func() { h.i.ed.SetGhost(""); close(clearGhost) })
	<-clearGhost
	replyLanded(t, h, fc)
	goQuiet(h)
	recv(t, fc.nextSteps, "the ask for the second reply")
}

// Silence is measured from the keyboard too: a user typing and deleting is
// present, and gets no offer.
func TestTypingRestartsTheSilence(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	replyLanded(t, h, fc)
	goQuiet(h)
	// A keystroke lands after the window elapsed: it is what the trigger reads
	// next, and it says the user is here.
	h.term.Type("x")
	h.waitText("x")
	// Back to an empty composer, but the keystroke is recent.
	h.term.Type("\x7f")
	noAsk(t, h, fc, "the user was typing a moment ago")

	// The must-still-succeed half: with the keystroke aged out, it fires. Without
	// this the test above would pass on a trigger that never fires at all.
	goQuiet(h)
	recv(t, fc.nextSteps, "the ask once the typing aged out")
}

// A cancelled turn is not an invitation. Esc means the user wanted it to stop,
// and "here is what to do next" is the wrong answer to that.
func TestACancelledTurnOffersNothing(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	fc.stream <- conv(core.WireEvent{Type: "turn_end", Stop: string(provider.StopAborted)})
	fc.stream <- conv(core.WireEvent{Type: "done"})
	goQuiet(h)
	noAsk(t, h, fc, "the last turn was cancelled")

	// And the suppression is per-turn, not permanent: the next turn's reply
	// offers normally.
	fc.stream <- conv(core.WireEvent{Type: "turn_start"})
	replyLanded(t, h, fc)
	goQuiet(h)
	recv(t, fc.nextSteps, "the ask after a clean turn following a cancel")
}

// A failed turn has no reply to build on, and the trailing "done" must not arm
// one off the back of it.
func TestAFailedTurnOffersNothing(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	fc.stream <- conv(core.WireEvent{Type: "error", Error: "the provider fell over"})
	fc.stream <- conv(core.WireEvent{Type: "done"})
	goQuiet(h)
	noAsk(t, h, fc, "the last turn errored")

	fc.stream <- conv(core.WireEvent{Type: "turn_start"})
	replyLanded(t, h, fc)
	goQuiet(h)
	recv(t, fc.nextSteps, "the ask after a clean turn following a failure")
}

// A half-written message is the user's, and outranks anything terva might
// propose. This is also the only suppression the editor would enforce on its
// own — the point of checking here is that the ASK is not made either.
func TestADraftInTheComposerOffersNothing(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	replyLanded(t, h, fc)
	h.term.Type("half a thought")
	h.waitText("half a thought")
	goQuiet(h)
	noAsk(t, h, fc, "the composer had a draft in it")

	for range len("half a thought") {
		h.term.Type("\x7f")
	}
	h.waitScreen("the composer to empty", func(*tuitest.Screen) bool { return composer(h).empty })
	goQuiet(h)
	recv(t, fc.nextSteps, "the ask once the draft was cleared")
}

// A dialog owns the bottom of the screen; the composer underneath is not what
// the user is looking at.
func TestAnOpenDialogOffersNothing(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	replyLanded(t, h, fc)
	h.i.runOnMain(func() { h.i.openSessionsDialog() })
	h.waitText("── sessions")
	goQuiet(h)
	noAsk(t, h, fc, "a dialog was open")

	h.term.Type("\x1b")
	h.waitGone("── sessions")
	goQuiet(h)
	recv(t, fc.nextSteps, "the ask once the dialog closed")
}

// A `!` command running in this process means the user is mid-errand, and the
// output they are waiting for may be the thing that decides their next move.
//
// The flag is set directly rather than by driving a real escape: startShellEscape
// sets exactly this and clears it when the command exits, and a real command
// fast enough for a test is also too fast to still be running when the tick
// looks. What matters for the trap the proposal warns about is that the TRIGGER
// is driven for real, and it is — the tick, the arm and the carrier are all the
// production path.
func TestAShellEscapeInFlightOffersNothing(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	replyLanded(t, h, fc)
	h.i.mu.Lock()
	h.i.shellRunning = true
	h.i.mu.Unlock()
	goQuiet(h)
	noAsk(t, h, fc, "a ! command was still running")

	h.i.mu.Lock()
	h.i.shellRunning = false
	h.i.mu.Unlock()
	goQuiet(h)
	recv(t, fc.nextSteps, "the ask once the command finished")
}

// A completion takes real time, and the user may start writing while it runs.
// The answer is then dropped rather than held: a line computed against a
// conversation the user has since moved past is worse than no line at all,
// because it still looks current.
//
// Asserts on the STORED offer rather than the screen. The editor already
// refuses to draw one over a non-empty composer, so a screen assertion would
// pass whether the answer was dropped or merely hidden — and hidden is the
// failure, since deleting back to empty would then reveal it.
func TestAnAnswerThatArrivesTooLateIsDropped(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	fc.mu.Lock()
	fc.nextStepGate = make(chan struct{})
	fc.mu.Unlock()

	replyLanded(t, h, fc)
	goQuiet(h)
	recv(t, fc.nextSteps, "the ask to go out")

	// The user starts writing while the completion is still in flight.
	h.term.Type("my own words")
	h.waitText("my own words")
	close(fc.nextStepGate)

	h.waitScreen("the in-flight ask to finish", func(*tuitest.Screen) bool {
		h.i.mu.Lock()
		defer h.i.mu.Unlock()
		return !h.i.nextStepInFlight
	})
	c := composer(h)
	if c.ghost != "" {
		t.Fatalf("a stale offer was stored behind the user's typing: %q — deleting "+
			"back to an empty composer would surface it", c.ghost)
	}
	if c.value != "my own words" {
		t.Fatalf("the user's text was disturbed: %q", c.value)
	}
}

// The feature is off unless something turns it on, and stage 3 ships with
// nothing that does. A build where the trigger fires by default would spend a
// completion per reply for every user who never asked for the feature.
func TestTheTriggerIsInertUntilEnabled(t *testing.T) {
	fc := newFakeCarrier()
	fc.stream = make(chan ctrlproto.Event, 16)
	fc.nextSteps = make(chan struct{}, 4)
	fc.nextStepLine = "run the tests"
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})
	h.i.mu.Lock()
	on := h.i.nextStepEnabled
	h.i.mu.Unlock()
	if on {
		t.Fatal("the idle trigger defaults to ON; it must not spend a completion nobody asked for")
	}

	fc.stream <- conv(core.WireEvent{Type: "done"})
	goQuiet(h)
	noAsk(t, h, fc, "the feature was never switched on")
}

// The setting reaches the trigger through the settings pump, which is what
// "applies live" means: the daemon persists the toggle, the pane broadcasts,
// and the client mirrors it. Driven end to end — the harness never sets
// nextStepEnabled here, the served row does.
func TestTheSettingSwitchesTheTriggerOn(t *testing.T) {
	fc := newFakeCarrier()
	fc.stream = make(chan ctrlproto.Event, 16)
	fc.nextSteps = make(chan struct{}, 4)
	fc.nextStepLine = "run the tests"
	fc.settingsExtra = []ctrlproto.SettingItem{
		{Key: "next_step", Label: "Suggest a next step", Type: "bool", Value: "true"},
	}
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	h.i.runOnMain(func() { h.i.refreshCarrierApprovalMode() })
	h.waitScreen("the served setting to reach the trigger", func(*tuitest.Screen) bool {
		h.i.mu.Lock()
		defer h.i.mu.Unlock()
		return h.i.nextStepEnabled
	})

	replyLanded(t, h, fc)
	goQuiet(h)
	recv(t, fc.nextSteps, "the ask, with the feature switched on by the setting alone")
}

// A withdrawn prompt is the user's own words taken out from under them, and it
// outranks the machine's idea. Driven through the real wire event, because that
// is what a daemon-side withdrawal actually is.
func TestAWithdrawnPromptBeatsAStandingOffer(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	// Send something, so there is a prompt this client can have withdrawn.
	h.term.Type("my real message\r")
	recv(t, fc.prompts, "the dispatched prompt")

	replyLanded(t, h, fc)
	goQuiet(h)
	recv(t, fc.nextSteps, "the offer to be asked for")
	h.waitText("run the tests")

	fc.stream <- conv(core.WireEvent{Type: "user_message_withdrawn", Text: "my real message"})
	h.waitScreen("the withdrawn prompt to take the composer", func(*tuitest.Screen) bool {
		return composer(h).value == "my real message"
	})
	if g := composer(h).ghost; g != "" {
		t.Fatalf("the offer survived a withdrawn prompt: %q — deleting the restored "+
			"text would surface a suggestion from before it came back", g)
	}
}

// A stashed draft is the user's own words parked deliberately, and recalling it
// likewise drops the offer. ctrl+s is the real key for this.
func TestRecallingAStashedDraftBeatsAStandingOffer(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	h.term.Type("a parked draft")
	h.waitText("a parked draft")
	h.term.Type("\x13") // ctrl+s: set aside
	h.waitScreen("the draft to be parked", func(*tuitest.Screen) bool { return composer(h).empty })

	replyLanded(t, h, fc)
	goQuiet(h)
	recv(t, fc.nextSteps, "the offer to be asked for")
	h.waitText("run the tests")

	h.term.Type("\x13") // ctrl+s again: take it back
	h.waitScreen("the parked draft to come back", func(*tuitest.Screen) bool {
		return composer(h).value == "a parked draft"
	})
	if g := composer(h).ghost; g != "" {
		t.Fatalf("the offer survived a recalled draft: %q", g)
	}
}

// Tab turns the offer into the user's own text, and only then can Enter send
// it. Driven through the key loop, because the editor's accept is reached from
// the keymap and a direct AcceptGhost call would prove nothing about wiring.
func TestTabTakesTheOfferAndEnterSendsIt(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	replyLanded(t, h, fc)
	goQuiet(h)
	recv(t, fc.nextSteps, "the daemon to be asked")
	h.waitText("run the tests")

	h.term.Type("\t")
	h.waitScreen("the offer to become the user's text", func(*tuitest.Screen) bool {
		return composer(h).value == "run the tests"
	})

	h.term.Type("\r")
	if got := recv(t, fc.prompts, "the accepted line to be sent"); got != "run the tests" {
		t.Fatalf("sent %q, want the accepted line", got)
	}
}
