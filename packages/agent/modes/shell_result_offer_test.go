package modes

// Stage 3 of docs/proposals/shell-escape-context.md: the TUI offers a finished
// "!" escape's output to the session.
//
// The claim under test is narrow and easy to get wrong in a way no
// content-reading assertion would notice: what crosses the wire is the RAW
// output, not the block the TUI paints. The two differ only by ANSI escape
// sequences, so `Contains("3 files changed")` passes on either one. These
// compare bytes.

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/tui"
)

// shellOffer is one captured shell.result call.
type shellOffer struct {
	sess string
	p    ctrlproto.ShellResultParams
}

// shellOfferCarrier is a carrier that serves the verb and records what it was
// given. Embeds fakeCarrier so it satisfies the whole Carrier surface.
type shellOfferCarrier struct {
	*fakeCarrier
	offers chan shellOffer
}

func (c *shellOfferCarrier) ShellResult(_ context.Context, sess string, p ctrlproto.ShellResultParams) error {
	select {
	case c.offers <- shellOffer{sess: sess, p: p}:
	default:
	}
	return nil
}

func newShellOfferCarrier() *shellOfferCarrier {
	return &shellOfferCarrier{fakeCarrier: newFakeCarrier(), offers: make(chan shellOffer, 4)}
}

// newOfferFixture builds an Interactive wired to a carrier, with the feature in
// the requested state. Constructed directly rather than through
// startInteractive: the thing under test is one method's decision, and driving
// a real terminal would test the escape's whole execution path to observe it.
func newOfferFixture(on bool) (*Interactive, *shellOfferCarrier) {
	c := newShellOfferCarrier()
	i := &Interactive{ed: tui.NewEditor("> ")}
	i.cfg.Carrier = c
	i.cfg.CarrierSession = "s1"
	i.shellResultContext = on
	return i, c
}

// The case the stage exists for, and the byte-level claim.
func TestTheRawOutputIsWhatCrossesTheWire(t *testing.T) {
	i, c := newOfferFixture(true)

	raw := "$ git status\n\n3 files changed\n\n[exit 0]"
	i.offerShellResult("git status", raw)

	got := recv(t, c.offers, "a shell.result offer")
	if got.p.Output != raw {
		t.Errorf("output crossed the wire altered:\n got %q\nwant %q", got.p.Output, raw)
	}
	if got.p.Command != "git status" {
		t.Errorf("command = %q", got.p.Command)
	}
	if got.sess != "s1" {
		t.Errorf("offered against session %q", got.sess)
	}
}

// The mistake this stage is most likely to make, pinned on its own. The styled
// block carries the same words, so only the escape sequences distinguish them.
func TestTheStyledBlockIsNotWhatCrossesTheWire(t *testing.T) {
	i, c := newOfferFixture(true)
	i.cfg.Theme = tui.Theme{Tool: 2, Error: 1, Muted: 8}

	raw := "$ git status\n\n3 files changed\n\n[exit 0]"
	styled := strings.Join(i.renderShellBlock(raw, false), "\n")
	if styled == raw {
		t.Fatal("the fixture's theme adds no styling, so this test cannot tell the two apart")
	}

	i.offerShellResult("git status", raw)

	got := recv(t, c.offers, "a shell.result offer")
	if got.p.Output == styled {
		t.Fatal("the TUI sent its RENDERED block; the model would read ANSI escape sequences as content")
	}
	if strings.Contains(got.p.Output, "\x1b[") {
		t.Errorf("the output carries escape sequences:\n%q", got.p.Output)
	}
}

// Off is the shipped default, and the check has to be here rather than only in
// the daemon: over `terva serve` that daemon may be on another host, so gating
// there alone still puts the output on the network.
func TestNothingIsOfferedWhileTheFeatureIsOff(t *testing.T) {
	i, c := newOfferFixture(false)

	i.offerShellResult("cat ~/.aws/credentials", "AWS_SECRET_ACCESS_KEY=hunter2")

	select {
	case got := <-c.offers:
		t.Fatalf("terminal output went to the daemon with the feature off: %q", got.p.Output)
	default:
	}
}

// An older daemon does not serve the verb. That is a fact about the peer, and
// must not crash the escape or scold the user.
func TestACarrierThatDoesNotServeTheVerbIsHarmless(t *testing.T) {
	i := &Interactive{ed: tui.NewEditor("> ")}
	i.cfg.Carrier = newFakeCarrier() // no ShellResult method
	i.cfg.CarrierSession = "s1"
	i.shellResultContext = true

	i.offerShellResult("git status", "3 files changed")
	// Reaching here without a panic is the assertion.
}

func TestNoSessionMeansNoOffer(t *testing.T) {
	i, c := newOfferFixture(true)
	i.cfg.CarrierSession = ""

	i.offerShellResult("git status", "3 files changed")

	select {
	case <-c.offers:
		t.Fatal("offered against an empty session")
	default:
	}
}

// --- through the real escape, which is where the argument is chosen ----------

// The tests above drive offerShellResult directly, and that is not enough: the
// mistake this stage can make is at the CALL SITE, where `out` and the styled
// block are both in scope and either compiles. Mutating startShellEscape to
// send strings.Join(block, "\n") passed every one of them.
//
// So this runs a real "!" escape through the real key loop and reads what the
// carrier was handed. 🪤 One production caller means testing through the caller.
func TestARealEscapeOffersItsUnstyledOutput(t *testing.T) {
	c := newShellOfferCarrier()
	c.stream = make(chan ctrlproto.Event, 16)
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = c
		cfg.CarrierSession = "s1"
	})
	// The feature is off by default; this session has it on.
	h.i.mu.Lock()
	h.i.shellResultContext = true
	h.i.mu.Unlock()

	h.term.Type("!echo hello-from-the-shell\r")
	h.waitText("[exit 0]")

	got := recv(t, c.offers, "the escape's offer")
	if got.p.Command != "echo hello-from-the-shell" {
		t.Errorf("command = %q", got.p.Command)
	}
	if !strings.Contains(got.p.Output, "hello-from-the-shell") {
		t.Errorf("the command's output did not reach the wire:\n%q", got.p.Output)
	}
	// The claim: unstyled. The rendered block carries the same words, so this is
	// the only assertion that separates them.
	if strings.Contains(got.p.Output, "\x1b[") {
		t.Errorf("the RENDERED block went to the daemon; the model would read escape sequences as content:\n%q", got.p.Output)
	}
}

// And with the feature off — the shipped default — a real escape offers nothing.
func TestARealEscapeOffersNothingWhileTheFeatureIsOff(t *testing.T) {
	c := newShellOfferCarrier()
	c.stream = make(chan ctrlproto.Event, 16)
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = c
		cfg.CarrierSession = "s1"
	})

	h.term.Type("!echo hello-from-the-shell\r")
	h.waitText("[exit 0]")

	select {
	case got := <-c.offers:
		t.Fatalf("terminal output was offered with the feature off: %q", got.p.Output)
	default:
	}
}
