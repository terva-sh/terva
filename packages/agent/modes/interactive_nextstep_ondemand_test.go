package modes

// The on-demand trigger: /nextstep, the surface for a user who wants the
// suggestion NOW rather than after thirty seconds of silence.
//
// Every test here submits the real command through the real editor, because the
// point of the feature is a command a person types. Calling askNextStepNow
// directly would leave the slash registration untested, and a spec without a
// handler (or a handler nothing routes to) is the exact failure this package's
// registry tests exist for.
//
// What is asserted throughout is the DIFFERENCE from the idle path, since that
// is all this stage adds: the setting does not gate it, the arm does not gate
// it, the window does not gate it, failures are reported rather than swallowed,
// and the offer waits behind a draft instead of being thrown away.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui/tuitest"
)

// noNextStepCarrier is a carrier that cannot suggest anything.
//
// It embeds the Carrier INTERFACE rather than *fakeCarrier, and that is the
// whole trick: the embed promotes exactly Carrier's method set, so
// SuggestNextStep — an optional controller, deliberately absent from the
// mandatory surface — does not come along. Embedding the struct would inherit
// the method and quietly test nothing. The type assertion in nextStepCarrier is
// what this fixture exists to fail.
type noNextStepCarrier struct{ Carrier }

// status reads the two status lines under the mutex that guards them.
func status(h *harness) (ok string, bad string) {
	h.i.mu.Lock()
	defer h.i.mu.Unlock()
	return h.i.statusOK, h.i.statusErr
}

// noAskFor fails if the daemon is asked at all within a window covering several
// animation ticks. Unlike noAsk it does not touch the arm: the on-demand path
// never reads one, so ageing it here would only obscure what is being tested.
func noAskFor(t *testing.T, fc *fakeCarrier, why string) {
	t.Helper()
	select {
	case <-fc.nextSteps:
		t.Fatalf("the daemon was asked while %s", why)
	case <-time.After(400 * time.Millisecond):
	}
}

// waitStatus waits for one of the status lines to say something.
func waitStatus(t *testing.T, h *harness, what string, pred func(ok, bad string) bool) {
	t.Helper()
	h.waitScreen(what, func(*tuitest.Screen) bool {
		ok, bad := status(h)
		return pred(ok, bad)
	})
}

// The setting is not a gate. next_step governs the offer terva volunteers; a
// user who left it off has not asked to lose a command they typed.
//
// Nothing arms the trigger in this test and the feature is switched OFF, so the
// idle path cannot fire at all — which is what makes the ask that arrives
// attributable to the command. The on_demand flag on the wire confirms it from
// the other side: the idle path never sets it.
func TestOnDemandIgnoresTheSetting(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	h.i.mu.Lock()
	h.i.nextStepEnabled = false
	h.i.mu.Unlock()

	h.term.Type("/nextstep\r")
	recv(t, fc.nextSteps, "the daemon to be asked with the setting off")
	h.waitText("run the tests")

	if !fc.lastNextStepParams().OnDemand {
		t.Fatal("the ask went out without on_demand, so the daemon told the model the user had asked for nothing")
	}
	// Offered, not typed, and not sent — the same promise the idle path makes.
	if c := composer(h); !c.empty {
		t.Fatalf("the offer went into the buffer: %q", c.value)
	}
	select {
	case p := <-fc.prompts:
		t.Fatalf("the suggestion was SENT: %q", p)
	default:
	}
}

// The other side of the same coin: the idle path must NOT claim the user asked.
// Without this, setting on_demand unconditionally would pass every test above.
func TestTheIdleAskIsNotMarkedOnDemand(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	replyLanded(t, h, fc)
	goQuiet(h)

	recv(t, fc.nextSteps, "the idle ask")
	if fc.lastNextStepParams().OnDemand {
		t.Fatal("an unrequested suggestion was marked on_demand; the daemon would tell the model a user asked when none did")
	}
}

// No arm, no window: the command works twice on the same reply, because the
// rationing exists to bound what terva spends unbidden and this is bidden.
func TestOnDemandRepeatsOnTheSameReply(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	replyLanded(t, h, fc)

	h.term.Type("/nextstep\r")
	recv(t, fc.nextSteps, "the first ask")
	h.waitText("run the tests")

	// The standing offer is cleared first: the second ask has to be observable
	// as an ASK, not inferred from a screen that already said the right thing.
	cleared := make(chan struct{})
	h.i.runOnMain(func() { h.i.ed.SetGhost(""); close(cleared) })
	<-cleared

	h.term.Type("/nextstep\r")
	recv(t, fc.nextSteps, "the second ask on the same reply")
	h.waitText("run the tests")
}

// Asking spends the arm. The user has the suggestion this reply is owed, and the
// idle trigger must not land a second one on top of it half a minute later —
// that would overwrite the offer they asked for with one they did not, and bill
// them for it.
func TestOnDemandSpendsTheIdleArm(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	replyLanded(t, h, fc) // the arm is standing

	h.term.Type("/nextstep\r")
	recv(t, fc.nextSteps, "the on-demand ask")
	h.waitText("run the tests")

	// The composer is empty and the ghost is cleared, so the idle path's own
	// suppressions are not what is holding it back here — only the spent arm is.
	cleared := make(chan struct{})
	h.i.runOnMain(func() { h.i.ed.SetGhost(""); close(cleared) })
	<-cleared
	goQuiet(h)
	noAsk(t, h, fc, "the user had just asked for a suggestion")

	// And it is the ARM that was spent, not the feature that was broken: the
	// next reply offers normally.
	replyLanded(t, h, fc)
	goQuiet(h)
	recv(t, fc.nextSteps, "the idle ask for the following reply")
}

// The offer waits behind a draft instead of being discarded.
//
// The idle path drops a suggestion that comes back to a composer the user has
// started typing in, and that is right there: they never asked. Here they did,
// so the line is HELD — invisible while their own words are on screen, and
// shown the moment the composer empties.
func TestOnDemandHoldsTheOfferBehindADraft(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	fc.nextStepGate = make(chan struct{})

	h.term.Type("/nextstep\r")
	recv(t, fc.nextSteps, "the ask to go out")

	// They start typing while the completion is in flight.
	h.term.Type("half a thought")
	h.waitText("half a thought")
	close(fc.nextStepGate)

	// Held, not shown: the line is on hand, the composer still reads as theirs.
	h.waitScreen("the offer to be held", func(*tuitest.Screen) bool {
		return composer(h).ghost == "run the tests"
	})
	c := composer(h)
	if c.empty || !strings.Contains(c.value, "half a thought") {
		t.Fatalf("the user's draft was disturbed: %q", c.value)
	}
	if strings.Contains(h.term.Screen().Text(), "run the tests") {
		t.Fatalf("a held offer was drawn over the user's own writing; screen:\n%s", h.term.Screen().Text())
	}

	// Clearing the composer reveals what they asked for.
	for range len("half a thought") {
		h.term.Type("\x7f")
	}
	h.waitText("run the tests")
}

// A turn is running, so the answer to "what next" is being written. Refused —
// and said out loud, because a command that does nothing silently reads as
// broken.
func TestOnDemandRefusesWhileATurnRuns(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	if !h.i.turns.claimSlot(func() {}) {
		t.Fatal("could not claim the turn slot")
	}

	h.term.Type("/nextstep\r")
	noAskFor(t, fc, "a turn was still running")
	waitStatus(t, h, "the refusal to be reported", func(_, bad string) bool { return bad != "" })

	// Released, it works — otherwise this test would pass on a command that
	// never asks at all.
	h.i.turns.releaseSlot()
	h.term.Type("/nextstep\r")
	recv(t, fc.nextSteps, "the ask once the turn finished")
}

// The user sent something while the completion was in flight. The line was
// drafted against a conversation that has since moved, so it is dropped rather
// than offered — and the drop is reported, not silent.
func TestOnDemandDropsASupersededSuggestion(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	fc.nextStepGate = make(chan struct{})

	h.term.Type("/nextstep\r")
	recv(t, fc.nextSteps, "the ask to go out")

	// A turn is underway by the time the answer lands.
	if !h.i.turns.claimSlot(func() {}) {
		t.Fatal("could not claim the turn slot")
	}
	defer h.i.turns.releaseSlot()
	close(fc.nextStepGate)

	waitStatus(t, h, "the drop to be reported", func(ok, _ string) bool {
		return strings.Contains(ok, "moved on")
	})
	if c := composer(h); c.ghost != "" {
		t.Fatalf("a superseded suggestion was offered anyway: %q", c.ghost)
	}
}

// A failure the user asked for is a failure they hear about. The idle path
// swallows these deliberately — nobody asked, so an error banner would be more
// interruption than the feature saves — but that argument does not survive the
// user typing a command.
func TestOnDemandReportsAFailure(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	fc.nextStepErr = errors.New("the provider fell over")

	h.term.Type("/nextstep\r")
	recv(t, fc.nextSteps, "the ask to go out")
	waitStatus(t, h, "the failure to be reported", func(_, bad string) bool {
		return strings.Contains(bad, "the provider fell over")
	})
	if c := composer(h); c.ghost != "" {
		t.Fatalf("a failed ask left an offer behind: %q", c.ghost)
	}
}

// The idle path's silence about failure is the counterpart, and it is asserted
// here so that making the on-demand path speak cannot quietly make the
// unrequested one speak too.
func TestTheIdleAskStaysSilentAboutAFailure(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	fc.nextStepErr = errors.New("the provider fell over")
	replyLanded(t, h, fc)
	goQuiet(h)

	recv(t, fc.nextSteps, "the idle ask")
	// Give the failure every chance to surface before concluding it did not.
	time.Sleep(300 * time.Millisecond)
	if _, bad := status(h); bad != "" {
		t.Fatalf("an unrequested suggestion reported its own failure: %q", bad)
	}
}

// An empty answer is the model declining, which the ask explicitly invites. On
// demand that still needs saying, or the command looks like it did not run.
func TestOnDemandSaysWhenThereIsNothingToSuggest(t *testing.T) {
	h, fc := nextStepHarness(t, "")

	h.term.Type("/nextstep\r")
	recv(t, fc.nextSteps, "the ask to go out")
	waitStatus(t, h, "the empty answer to be reported", func(ok, _ string) bool {
		return strings.Contains(ok, "nothing obvious")
	})
	if c := composer(h); c.ghost != "" {
		t.Fatalf("an empty answer became an offer: %q", c.ghost)
	}
}

// Two commands in quick succession must not both go out. The claim and the
// check share one critical section, so the second is refused rather than
// racing the first into a duplicate spend.
func TestOnDemandRefusesWhileAnAskIsInFlight(t *testing.T) {
	h, fc := nextStepHarness(t, "run the tests")
	fc.nextStepGate = make(chan struct{})

	h.term.Type("/nextstep\r")
	recv(t, fc.nextSteps, "the first ask")

	h.term.Type("/nextstep\r")
	noAskFor(t, fc, "an ask was already in flight")
	waitStatus(t, h, "the refusal to be reported", func(_, bad string) bool { return bad != "" })

	close(fc.nextStepGate)
	h.waitText("run the tests")
}

// A carrier that cannot answer says so. The idle path stays dark on such a
// build, which is right for something nobody invoked and wrong for a command
// someone typed.
func TestOnDemandReportsAnUnsupportedCarrier(t *testing.T) {
	fc := newFakeCarrier()
	fc.stream = make(chan ctrlproto.Event, 16)
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = &noNextStepCarrier{fc}
		cfg.CarrierSession = "s1"
	})

	h.term.Type("/nextstep\r")
	waitStatus(t, h, "the unsupported carrier to be reported", func(_, bad string) bool {
		return strings.Contains(bad, "not available here")
	})
}
