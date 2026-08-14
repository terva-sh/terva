package modes

// Stage 3 of docs/proposals/withdraw-cancelled-prompt.md: a prompt the daemon
// withdrew comes back to the composer instead of being retyped.
//
// Driven through the real terminal, because the claim is about what the user
// sees: the text is IN the editor afterwards, not merely somewhere in the
// program's state.

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
	"terva.sh/terva/packages/tui/tuitest"
)

// composerRow is the editor's row: the LAST "▌" row on screen. The shared
// editorRow helper takes the FIRST, which on these fixtures is the greeting
// bubble — user-role rows carry the same gutter, so "first" only means "the
// editor" on a screen with an empty transcript.
func composerRow(s *tuitest.Screen) string {
	out := ""
	for _, row := range s.Rows() {
		if strings.HasPrefix(strings.TrimSpace(row), "▌") {
			out = row
		}
	}
	return out
}

// withdrawn is the daemon's withdrawal broadcast, carrying the prompt in full.
func withdrawn(text string) ctrlproto.Event {
	return conv(core.WireEvent{Type: "user_message_withdrawn", Text: text})
}

// interruptAndWithdraw plays the daemon's real sequence for an interrupted
// turn. The order is not cosmetic: core emits EvDone from runLoop's terminal
// stop and only THEN withdraws in PromptExtra, so "done" genuinely arrives
// first and the restore has to work after the turn slot is already released.
func interruptAndWithdraw(fc *fakeCarrier, text string) {
	fc.stream <- conv(core.WireEvent{Type: "done"})
	fc.stream <- withdrawn(text)
}

// The case the whole feature exists for: send something by accident, stop the
// turn before the model answers, and get it back to edit rather than retype.
func TestAWithdrawnPromptReturnsToTheComposer(t *testing.T) {
	fc := newFakeCarrier()
	fc.stream = make(chan ctrlproto.Event, 16)
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	h.term.Type("htop\r")
	if got := recv(t, fc.prompts, "dispatched prompt"); got != "htop" {
		t.Fatalf("dispatched prompt = %q", got)
	}
	// The submit cleared the editor — otherwise the assertion below would pass
	// on text that never left.
	h.waitScreen("editor cleared by the submit", func(s *tuitest.Screen) bool {
		return !strings.Contains(composerRow(s), "htop")
	})

	interruptAndWithdraw(fc, "htop")

	h.waitText("back in the composer")
	h.waitScreen("the prompt is in the editor again", func(s *tuitest.Screen) bool {
		return strings.Contains(composerRow(s), "htop")
	})
}

// A withdrawal for a prompt this client never sent belongs to another client on
// the same session. Its text must not appear in this composer.
func TestAForeignWithdrawalIsNotClaimed(t *testing.T) {
	fc := newFakeCarrier()
	fc.stream = make(chan ctrlproto.Event, 16)
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	h.term.Type("my own prompt\r")
	recv(t, fc.prompts, "dispatched prompt")

	interruptAndWithdraw(fc, "something another device typed")

	// Asserting "nothing happened" needs a sync point, or it passes before the
	// event has even been handled — which is exactly how the first draft of this
	// test let a mutation through. The client's OWN withdrawal is that sync
	// point: it can only restore if the foreign one was both ignored AND left
	// the arm intact, so the positive assertion below carries the negative one.
	interruptAndWithdraw(fc, "my own prompt")
	h.waitText("back in the composer")

	got := h.term.Screen().Text()
	if strings.Contains(got, "something another device typed") {
		t.Errorf("another client's prompt landed in this composer; screen:\n%s", got)
	}
	if !strings.Contains(got, "my own prompt") {
		t.Errorf("a foreign withdrawal consumed this client's reclaimable draft; screen:\n%s", got)
	}
}

// A draft typed after the prompt went out is the newer intent. The returning
// prompt is parked rather than allowed to overwrite it — fixing a lost message
// by losing a different one is not a fix.
func TestAWithdrawalDoesNotClobberANewerDraft(t *testing.T) {
	fc := newFakeCarrier()
	fc.stream = make(chan ctrlproto.Event, 16)
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	h.term.Type("htop\r")
	recv(t, fc.prompts, "dispatched prompt")

	h.term.Type("something I would rather say")
	h.waitText("something I would rather say")

	interruptAndWithdraw(fc, "htop")

	h.waitText("set aside:")
	h.waitScreen("the newer draft is untouched", func(s *tuitest.Screen) bool {
		return strings.Contains(composerRow(s), "something I would rather say")
	})

	// And the parked one is recoverable the way every parked draft is.
	h.term.Type("\x13")
	h.waitText("htop")
}

// A `!` shell escape runs through the submit path and returns before the
// dispatch — it never becomes a prompt, so it must not take over the arm. The
// slash-command branch returns from the same place for the same reason.
//
// The escape is what this drives rather than a slash command, and the
// difference was measured: `/help` is consumed by the slash-suggest popup and
// never reaches the arm site at all, so a test built on it passes whether the
// guard is there or not. The escape does reach it.
func TestAShellEscapeDoesNotArmTheRestore(t *testing.T) {
	fc := newFakeCarrier()
	fc.stream = make(chan ctrlproto.Event, 16)
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	// A real prompt arms the restore...
	h.term.Type("the real prompt\r")
	recv(t, fc.prompts, "dispatched prompt")

	// The escape needs the busy slot, which the dispatched turn still holds, so
	// end that turn first. This is also the real order: done lands before the
	// withdrawal.
	fc.stream <- conv(core.WireEvent{Type: "done"})
	// Sending `done` only hands it to the pump — the slot is released when the
	// TUI gets round to applying it. Typing the escape before then meets a
	// still-busy TUI, which refuses it outright ("busy — wait for the current
	// turn to finish"), and the wait below burns its full timeout on an escape
	// that never ran. Reproducible with -cpu=1; it is what turned this red on
	// CI's loaded box while an idle laptop stayed green 60 runs deep.
	h.waitScreen("the finished turn to release the busy slot", func(*tuitest.Screen) bool {
		return !h.i.turns.Busy()
	})

	// ...and an escape afterwards must not take that arm over.
	h.term.Type("!true\r")
	h.waitText("[exit 0]")

	// If the escape wrongly armed, this restores IT and consumes the arm.
	interruptAndWithdraw(fc, "!true")
	// So the real prompt's withdrawal is the discriminator: it can only reach
	// the composer if the escape left the arm alone.
	interruptAndWithdraw(fc, "the real prompt")

	h.waitScreen("the real prompt is what comes back", func(s *tuitest.Screen) bool {
		return strings.Contains(composerRow(s), "the real prompt")
	})
}

// --- the matching rule, at the level where it is observable ------------------

// The terminal tests above cover the integration, but they cannot see the
// DIFFERENCE between "restored the right text" and "restored the right text for
// the wrong reason": a rule that claims every withdrawal still ends up putting
// this client's own draft on screen, because that draft is all it has. Only the
// arm's state distinguishes them, so it is asserted directly.
func newRestoreFixture() *Interactive {
	return &Interactive{ed: tui.NewEditor("> ")}
}

func TestAForeignWithdrawalNeitherRestoresNorDisarms(t *testing.T) {
	i := newRestoreFixture()
	i.ed.SetValue("mine")
	armed := i.ed.State()
	i.ed.Clear()
	i.armWithdrawableDraft(armed, nil, "mine")

	i.restoreWithdrawnPrompt("something another device typed")

	if got := i.ed.Value(); got != "" {
		t.Errorf("a withdrawal this client did not send wrote %q into the composer", got)
	}
	if i.withdrawDraft == nil {
		t.Error("a foreign withdrawal consumed this client's arm — its own prompt is now unreclaimable")
	}

	// And the arm still works afterwards, which is the point of keeping it.
	i.restoreWithdrawnPrompt("mine")
	if got := i.ed.Value(); got != "mine" {
		t.Errorf("composer = %q after the matching withdrawal; want the prompt back", got)
	}
	if i.withdrawDraft != nil {
		t.Error("a claimed withdrawal must consume the arm, or the next one restores stale text")
	}
}

func TestAWithdrawalWithNothingArmedIsInert(t *testing.T) {
	i := newRestoreFixture()
	i.restoreWithdrawnPrompt("anything at all")
	if got := i.ed.Value(); got != "" {
		t.Errorf("wrote %q into the composer with nothing armed", got)
	}
}

// Empty text is what a withdrawal carries when something upstream lost it.
// Restoring an empty draft would silently clear a composer the user was using.
func TestAnEmptyWithdrawalIsInert(t *testing.T) {
	i := newRestoreFixture()
	i.ed.SetValue("mine")
	armed := i.ed.State()
	i.ed.Clear()
	i.armWithdrawableDraft(armed, nil, "")

	i.restoreWithdrawnPrompt("")
	if i.withdrawDraft == nil {
		t.Error("an empty withdrawal consumed the arm")
	}
}

// Three drafts and one park slot: the composer in use, something already set
// aside, and a withdrawal arriving. Nothing may be destroyed to make room —
// the prompt is still in the input history, which is what the status says.
func TestAWithdrawalWithBothSlotsFullDestroysNothing(t *testing.T) {
	i := newRestoreFixture()
	i.ed.SetValue("the withdrawn one")
	armed := i.ed.State()

	i.ed.SetValue("already parked")
	parked := i.ed.State()
	i.stash = &draftStash{ed: parked}

	i.ed.SetValue("actively typing")
	i.armWithdrawableDraft(armed, nil, "the withdrawn one")

	i.restoreWithdrawnPrompt("the withdrawn one")

	if got := i.ed.Value(); got != "actively typing" {
		t.Errorf("composer = %q; the draft in use must survive", got)
	}
	if i.stash == nil || i.stash.ed.Value() != "already parked" {
		t.Error("the already-parked draft was overwritten to make room")
	}
}
