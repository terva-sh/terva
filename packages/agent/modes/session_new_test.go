package modes

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// newInteractiveForNewSessionTest builds the minimal Interactive that
// startNewSession touches: a view, a turn engine, an editor, and pre-dirtied
// TUI state so the reset is observable. No agent — the TUI holds none (plan
// 4.1); the NewSession callback stands in for the host's carrier session switch.
//
// The editor is not optional. NewInteractive always mints one, so a fixture
// without one models a machine that does not exist — and startNewSession now
// reaches the composer to drop a standing next-step offer, which is exactly the
// kind of reach such a fixture hides until it nil-panics.
func newInteractiveForNewSessionTest() *Interactive {
	iv := &Interactive{
		view:  &tui.View{},
		turns: newTurnEngine(),
		ed:    tui.NewEditor(""),
		// Pre-populate state that startNewSession must clear.
		toolCalls:    map[string]*tui.ToolCallView{"x": {}},
		cumUsage:     provider.Usage{InputTokens: 1234, OutputTokens: 56},
		lastCtxInput: 9000,
		statusErr:    "stale error",
		scrollOffset: 5,
	}
	iv.ed.SetGhost("an offer from the old conversation")
	iv.cfg.Provider = "anthropic"
	iv.cfg.Model = "claude-sonnet-4-5"
	return iv
}

func TestStartNewSessionResetsStateAndInvokesCallback(t *testing.T) {
	iv := newInteractiveForNewSessionTest()

	var gotProvider, gotModel string
	called := false
	iv.cfg.NewSession = func(providerName, model string) error {
		called = true
		gotProvider, gotModel = providerName, model
		// The host switches the carrier session here; nothing local to reset.
		return nil
	}

	iv.startNewSession()

	if !called {
		t.Fatal("startNewSession did not invoke the NewSession callback")
	}
	if gotProvider != "anthropic" || gotModel != "claude-sonnet-4-5" {
		t.Errorf("callback got provider/model %q/%q, want anthropic/claude-sonnet-4-5", gotProvider, gotModel)
	}
	if iv.statusOK != "started a new session" {
		t.Errorf("statusOK = %q, want %q", iv.statusOK, "started a new session")
	}
	if iv.statusErr != "" {
		t.Errorf("statusErr = %q, want empty", iv.statusErr)
	}
	if len(iv.toolCalls) != 0 {
		t.Errorf("toolCalls not cleared: %v", iv.toolCalls)
	}
	if iv.cumUsage != (provider.Usage{}) {
		t.Errorf("cumUsage not zeroed: %+v", iv.cumUsage)
	}
	if iv.lastCtxInput != 0 || iv.scrollOffset != 0 {
		t.Errorf("lastCtxInput/scrollOffset not reset: %d/%d", iv.lastCtxInput, iv.scrollOffset)
	}
	// A suggestion computed against the conversation just left would otherwise
	// greet the new one, still looking current. Erasing the composer no longer
	// ends an offer, so the paths where the CONVERSATION moves must end it.
	if got := iv.ed.Ghost(); got != "" {
		t.Errorf("the old conversation's offer survived into the new session: %q", got)
	}
}

func TestStartNewSessionUnwired(t *testing.T) {
	iv := newInteractiveForNewSessionTest()
	iv.cfg.NewSession = nil // not wired in this build

	iv.startNewSession()

	if iv.statusErr == "" {
		t.Error("expected a statusErr when NewSession is not wired")
	}
	if iv.statusOK != "" {
		t.Errorf("statusOK should stay empty on the unwired path, got %q", iv.statusOK)
	}
	// State must be left intact (not reset) when we couldn't start one.
	if len(iv.toolCalls) == 0 {
		t.Error("toolCalls should not be cleared when the callback is unwired")
	}
}

func TestStartNewSessionCallbackError(t *testing.T) {
	iv := newInteractiveForNewSessionTest()
	iv.cfg.NewSession = func(string, string) error {
		return errTest
	}

	iv.startNewSession()

	if iv.statusErr != errTest.Error() {
		t.Errorf("statusErr = %q, want %q", iv.statusErr, errTest.Error())
	}
	if iv.statusOK != "" {
		t.Errorf("statusOK should be empty on error, got %q", iv.statusOK)
	}
}

// /new must wipe the screen + scrollback so the previous session doesn't
// linger above the fresh one (terva renders in main-screen flow mode).
func TestStartNewSessionClearsScreen(t *testing.T) {
	iv := newInteractiveForNewSessionTest()
	var buf bytes.Buffer
	iv.rend = tui.NewRenderer(&buf)
	iv.cfg.NewSession = func(string, string) error { return nil }

	iv.startNewSession()

	if !strings.Contains(buf.String(), tui.SeqClearScrollback) {
		t.Errorf("startNewSession should clear screen + scrollback; renderer output = %q", buf.String())
	}
}

// /clear likewise resets the visible frame so a cleared conversation actually
// looks cleared.
func TestSlashClearClearsScreen(t *testing.T) {
	iv := newInteractiveForNewSessionTest()
	var buf bytes.Buffer
	iv.rend = tui.NewRenderer(&buf)

	iv.slashClear(context.Background(), nil, "")

	if !strings.Contains(buf.String(), tui.SeqClearScrollback) {
		t.Errorf("/clear should clear screen + scrollback; renderer output = %q", buf.String())
	}
}

type testErr string

func (e testErr) Error() string { return string(e) }

const errTest testErr = "boom: cannot create session"
