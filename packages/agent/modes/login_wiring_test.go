package modes

import (
	"context"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// authCarrier is a fake carrier that serves the auth group, standing in for the
// two things that really do: the in-process *workspace.Workspace, and the
// ctrlclient Service that `terva attach` talks through.
type authCarrier struct {
	*fakeCarrier
	step ctrlproto.AuthFlowStep

	// Guarded: a submit is dispatched on its own goroutine (it talks to a provider
	// over the network in production), so the recording races the assertion.
	mu               sync.Mutex
	started          []ctrlproto.AuthLoginStartParams
	submitted        []ctrlproto.AuthLoginSubmitParams
	logouts          []string
	endpointsRemoved []string
}

func (a *authCarrier) starts() []ctrlproto.AuthLoginStartParams {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ctrlproto.AuthLoginStartParams(nil), a.started...)
}

func (a *authCarrier) submits() []ctrlproto.AuthLoginSubmitParams {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ctrlproto.AuthLoginSubmitParams(nil), a.submitted...)
}

func (a *authCarrier) loggedOut() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.logouts...)
}

func (a *authCarrier) AuthLoginStart(_ context.Context, p ctrlproto.AuthLoginStartParams) (ctrlproto.AuthFlowStep, error) {
	a.mu.Lock()
	a.started = append(a.started, p)
	a.mu.Unlock()
	return a.step, nil
}

func (a *authCarrier) AuthLoginSubmit(_ context.Context, p ctrlproto.AuthLoginSubmitParams) error {
	a.mu.Lock()
	a.submitted = append(a.submitted, p)
	a.mu.Unlock()
	return nil
}

func (a *authCarrier) AuthLoginCancel(_ context.Context, _ ctrlproto.AuthFlowRef) error { return nil }

func (a *authCarrier) AuthLogout(_ context.Context, p ctrlproto.AuthLogoutParams) error {
	a.mu.Lock()
	a.logouts = append(a.logouts, p.Provider)
	a.mu.Unlock()
	return nil
}

// Recorded separately from logouts, because they are separate acts: a logout
// forgets a secret, this forgets the operator's definition of a machine.
func (a *authCarrier) AuthEndpointRemove(_ context.Context, p ctrlproto.AuthEndpointRemoveParams) error {
	a.mu.Lock()
	a.endpointsRemoved = append(a.endpointsRemoved, p.ID)
	a.mu.Unlock()
	return nil
}

func newAuthCarrier(step ctrlproto.AuthFlowStep) *authCarrier {
	return &authCarrier{fakeCarrier: newFakeCarrier(), step: step}
}

// onMain runs fn on the interactive's main goroutine and waits for it.
//
// startLogin, submitLogin and doLogout touch main-loop-only state (the dialog,
// loginFlow) and in production are only ever reached from a key press, which the
// main loop dispatches. A test that called them directly would be the only caller
// that races — and the race detector says so.
func onMain(t *testing.T, i *Interactive, fn func()) {
	t.Helper()
	done := make(chan struct{})
	i.runOnMain(func() {
		fn()
		close(done)
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the main loop never ran the action")
	}
}

// /login goes through the CARRIER, not through a credential state machine of the
// TUI's own. This is the whole convergence: the TUI is a ctrlproto client, and the
// login it drives is the same one the web panel drives, served by the same code.
func TestTheTUIsLoginGoesThroughTheCarrier(t *testing.T) {
	fc := newAuthCarrier(ctrlproto.AuthFlowStep{
		Flow: "f7", Kind: "form", Title: "Sign in with an API key",
		Fields: []ctrlproto.AuthField{
			{Name: "api_key", Label: "API key", Type: "secret", Required: true},
		},
	})
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
		cfg.CarrierLocal = true
	})
	i := h.i

	if i.authController() == nil {
		t.Fatal("the carrier does not expose the auth group; /login has nowhere to go")
	}

	onMain(t, i, func() { i.startLogin("anthropic", "apikey") })

	starts := fc.starts()
	if len(starts) != 1 {
		t.Fatalf("auth.login.start was called %d times, want 1", len(starts))
	}
	got := starts[0]
	if got.Provider != "anthropic" || got.Method != "apikey" {
		t.Errorf("start = %+v, want anthropic/apikey", got)
	}
	// The in-process TUI's browser IS on the daemon's host, so the loopback flows
	// are reachable and it says so. An attached TUI would not.
	if !got.Local {
		t.Error("the in-process TUI did not declare itself local; it would be denied the loopback OAuth flow it can actually use")
	}
	if i.loginFlow != "f7" {
		t.Errorf("loginFlow = %q, want the handle the daemon minted", i.loginFlow)
	}
}

// The values the dialog collected go back under the daemon's flow handle. Without
// the handle the daemon cannot tell which pkce verifier this submission belongs
// to, which is the bug the whole flow-handle mechanism exists to prevent.
func TestSubmittingCarriesTheDaemonsFlowHandle(t *testing.T) {
	fc := newAuthCarrier(ctrlproto.AuthFlowStep{Flow: "f9", Kind: "form"})
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})
	i := h.i

	onMain(t, i, func() { i.startLogin("anthropic", "apikey") })
	onMain(t, i, func() { i.submitLogin(map[string]string{"api_key": "sk-typed"}) })

	waitForCond(t, func() bool { return len(fc.submits()) == 1 }, "the submit to reach the carrier")
	got := fc.submits()[0]
	if got.Flow != "f9" {
		t.Errorf("submit carried flow %q, want the daemon's handle f9", got.Flow)
	}
	if got.Values["api_key"] != "sk-typed" {
		t.Errorf("submit values = %+v, want the typed key under the name the daemon gave it", got.Values)
	}
}

// /logout goes to the daemon too. The TUI used to clear the credential store
// itself — including a hand-rolled copy of the openai/openai-codex split — which
// is a second answer to a question the daemon already answers.
func TestLogoutGoesToTheDaemon(t *testing.T) {
	fc := newAuthCarrier(ctrlproto.AuthFlowStep{})
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = fc
		cfg.CarrierSession = "s1"
	})

	onMain(t, h.i, func() { h.i.doLogout("openai-codex") })

	if out := fc.loggedOut(); len(out) != 1 || out[0] != "openai-codex" {
		t.Fatalf("logouts = %v, want the daemon asked to clear openai-codex", out)
	}
}

// A carrier that does not serve the auth group — a replay session, or a daemon
// started without --web-allow-login — must leave /login unreachable rather than
// offering a dialog that cannot finish.
func TestACarrierWithoutTheAuthGroupOffersNoLogin(t *testing.T) {
	h := startInteractive(t, func(cfg *InteractiveConfig) {
		cfg.Ready = true
		cfg.Carrier = newFakeCarrier() // no auth verbs
		cfg.CarrierSession = "s1"
	})
	if h.i.canLogin() {
		t.Error("a carrier with no auth group reports that it can log in")
	}
	if h.i.authController() != nil {
		t.Error("authController is non-nil on a carrier that does not serve the group")
	}
}
