package modes

import (
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// newInteractiveForNewSessionTest builds the minimal Interactive that
// startNewSession touches: a view, an agent, and pre-dirtied TUI state
// so the reset is observable.
func newInteractiveForNewSessionTest() *Interactive {
	iv := &Interactive{
		view:  &tui.View{},
		agent: &core.Agent{Model: "claude-sonnet-4-5"},
		// Pre-populate state that startNewSession must clear.
		toolCalls:    map[string]*tui.ToolCallView{"x": {}},
		cumUsage:     provider.Usage{InputTokens: 1234, OutputTokens: 56},
		lastCtxInput: 9000,
		statusErr:    "stale error",
		scrollOffset: 5,
	}
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
		// Mimic the host: the live agent's transcript is reset.
		iv.agent.SetMessages(nil)
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

type testErr string

func (e testErr) Error() string { return string(e) }

const errTest testErr = "boom: cannot create session"
