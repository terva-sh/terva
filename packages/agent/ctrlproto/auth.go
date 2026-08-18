package ctrlproto

import (
	"context"

	"terva.sh/terva/packages/core"
)

// The auth group: establishing, repairing, and revoking the credential terva
// uses to reach a MODEL PROVIDER.
//
// Not the panel's own bearer-token login (web/auth.go). That answers "may you
// talk to this daemon"; this answers "may this daemon talk to Anthropic".
//
// What is NOT here, deliberately: any verb that returns a secret. Not now, not
// behind a flag. A credential goes in and never comes back out — the client can
// see THAT anthropic is logged in (auth.providers) and can replace or revoke it,
// but nothing on this wire will ever hand it back the token. See Security in
// docs/proposals/web-provider-login.md.

// AuthController is the optional interface behind [GroupAuth]. A carrier that
// implements it can serve a login; one that does not never advertises the group,
// so a client that negotiates it is guaranteed the verbs work.
type AuthController interface {
	// AuthLoginStart begins a flow and returns what to put in front of the user.
	AuthLoginStart(ctx context.Context, p AuthLoginStartParams) (AuthFlowStep, error)
	// AuthLoginSubmit completes a form step. It is the one call in ctrlproto that
	// carries a secret.
	AuthLoginSubmit(ctx context.Context, p AuthLoginSubmitParams) error
	// AuthLoginCancel abandons a flow.
	AuthLoginCancel(ctx context.Context, p AuthFlowRef) error
	// AuthLogout clears a stored credential.
	AuthLogout(ctx context.Context, p AuthLogoutParams) error
	// AuthEndpointRemove forgets a named openai-compatible endpoint — the
	// definition, not just its key. See AuthEndpointRemoveParams.
	AuthEndpointRemove(ctx context.Context, p AuthEndpointRemoveParams) error
}

// AuthLoginStartParams begins a login.
//
// Method is "apikey" or "oauth". The client does NOT choose the concrete oauth
// variant (device code vs paste-the-code-back), because only the daemon knows
// which variants can actually complete over this carrier — and the answer is
// permanent, not a preference. A browser on a phone can never reach the DAEMON's
// loopback, so the loopback flows are not offered over the wire at all.
type AuthLoginStartParams struct {
	Provider string `json:"provider"`
	Method   string `json:"method"` // apikey | oauth

	// Local says the caller's browser is on the DAEMON'S OWN HOST — true only for
	// an in-process carrier (the TUI you launched on this machine). It is not a
	// preference and a remote client must never set it: it is a statement of fact
	// about where the browser is, and the daemon uses it to decide whether the
	// loopback flows are reachable at all.
	//
	// When it is set, the daemon may additionally run the loopback OAuth callback
	// (and open a browser), so a local user gets the good flow — click, approve,
	// done — while still receiving the paste-back form as a fallback. When it is
	// not, those flows are omitted entirely, because a callback server bound to a
	// port on the daemon is not something a phone can reach, and offering it would
	// be offering a dead end.
	//
	// It grants no authority: the worst a client can do by lying is ask the daemon
	// to bind a loopback port and open a browser nobody is looking at, and it still
	// gets a paste-back form it can actually complete.
	Local bool `json:"local,omitempty"`
}

// AuthFlowRef names a flow that is already in progress.
type AuthFlowRef struct {
	Flow string `json:"flow"`
}

// AuthFlowStep is what the daemon asks the client to render.
//
// One shape for every provider, present and future. The alternative — naming
// api_key, base_url, model and context_window as wire fields — makes the next
// provider that wants a field nobody anticipated into a PROTOCOL CHANGE: a new
// struct member, a daemon release, a client release, and a feature string to
// bridge the two. For something whose whole job is to collect a handful of
// strings, that is a bad trade, and it is one we would make repeatedly.
//
// So the daemon describes the form and the client renders it.
type AuthFlowStep struct {
	// Flow is the opaque handle for THIS attempt, required on submit.
	//
	// It exists because a login is not a pure function of its inputs: the manual
	// OAuth flow leaves a pkce verifier on the daemon, and the code the user
	// pastes back is only meaningful against the flow that minted it. With one
	// user at one keyboard that is invisible; with a daemon serving a phone and a
	// laptop, two logins started at once would exchange each other's codes. A
	// submit against a superseded handle is refused (CodeBusy), not mis-exchanged.
	Flow string `json:"flow"`

	// Kind says what to render, not which provider is being rendered:
	//
	//	form    — collect Fields, then call auth.login.submit
	//	display — show URL (and UserCode) and wait; the daemon is polling, and
	//	          completion arrives as an auth_state event. Nothing to submit.
	//	info    — read-only prose. Nothing to submit, ever.
	Kind string `json:"kind"`

	Title string   `json:"title,omitempty"`
	Lines []string `json:"lines,omitempty"` // prose above the fields; the whole body when kind=info

	// URL is DISPLAYED, never auto-opened: the daemon's browser is not the user's,
	// and on a headless host there is no browser at all. Present on kind=display
	// (the verification page) and beside the fields on kind=form (the authorize
	// page you visit before pasting the code back).
	URL string `json:"url,omitempty"`

	// UserCode is the device-flow code, carried on its own so a client can render
	// "type this code" as text. It also rides inside URL, which is all the TUI
	// ever needed — but a phone reading a panel served from a VM cannot open a
	// URL for the user, so it has to be able to show the code itself.
	UserCode string `json:"user_code,omitempty"`

	Fields []AuthField `json:"fields,omitempty"` // kind=form
}

// AuthField is one input.
//
// The daemon owns order, labels, and validation; the client owns only
// presentation. That ownership is not pedantry: today the browser form and the
// TUI dialog order openai-compatible's four fields DIFFERENTLY, because each
// decided for itself. One descriptor ends that class of drift.
// AuthFieldSecret is the Type of a field whose value must never be shown. Both
// renderers key on it — the web client as <input type="password">, the TUI as a
// masked editor — so it is a constant rather than a string spelled twice.
const AuthFieldSecret = "secret"

type AuthField struct {
	Name  string `json:"name"` // the key in AuthLoginSubmitParams.Values
	Label string `json:"label"`
	// Type is a rendering and validation HINT: text | secret | integer.
	// Everything crosses the wire as a string, including integer, and the daemon
	// parses. The context-window field is already parsed and range-checked in the
	// TUI dialog AND again in the auth manager; a typed int on the wire would add
	// a third site to disagree. One authority, at the end.
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	// RequiredUnless names another field that relaxes this one: required only
	// while THAT field is empty. openai-compatible's default model needs it —
	// mandatory for the single shared slot, pointless for a named endpoint, which
	// discovers its own models. Without it the client could not tell the user what
	// is still outstanding before they press the button, and a form that goes
	// quietly dead is the bug this whole surface exists to avoid. The daemon still
	// re-checks at submit: this is an affordance, never the authority.
	RequiredUnless string `json:"required_unless,omitempty"`
	Default        string `json:"default,omitempty"`
	Placeholder    string `json:"placeholder,omitempty"`
	Help           string `json:"help,omitempty"`
}

// AuthLoginSubmitParams completes a form step. Values is keyed by AuthField.Name.
//
// This is the only frame in ctrlproto that carries a secret. It goes one way: in.
type AuthLoginSubmitParams struct {
	Flow   string            `json:"flow"`
	Values map[string]string `json:"values"`
}

// AuthLogoutParams clears a stored credential. Provider "all" clears every one.
//
// "openai" and "openai-codex" are separate logins sharing one slot on disk — a
// platform API key and a ChatGPT subscription — and clearing one must leave the
// other standing. auth.Describe owns that split; this just names which.
type AuthLogoutParams struct {
	Provider string `json:"provider"`
}

// AuthEndpointRemoveParams forgets a named openai-compatible endpoint: its entry
// in config.json's `endpoints`, and any key stored under that id.
//
// Deliberately NOT a logout. A logout forgets a secret and the provider is still
// there to sign back into; this forgets the operator's DEFINITION of a server —
// which host, which port, which context window. Making "sign out" do that
// silently would be a trap, so the two verbs stay apart even though the same
// pane offers both.
type AuthEndpointRemoveParams struct {
	ID string `json:"id"`
}

// AuthState is the auth_state event: a login flow's progress.
//
// It rides the WORKSPACE address ([AddrWorkspace]), because a credential is a
// fact about the daemon and not about any session — and because the panel must
// hear "your login landed" whether or not it has a session in focus. It never
// carries a secret; Message is an error string.
type AuthState struct {
	Kind     string `json:"kind"` // started | success | error | canceled
	Flow     string `json:"flow,omitempty"`
	Provider string `json:"provider,omitempty"`
	Method   string `json:"method,omitempty"` // apikey | oauth
	URL      string `json:"url,omitempty"`
	UserCode string `json:"user_code,omitempty"`
	Message  string `json:"message,omitempty"`
}

// AuthStateEvent builds an [EventAuthState] event.
func AuthStateEvent(st AuthState) Event {
	return Event{WireEvent: core.WireEvent{Type: EventAuthState}, Auth: &st}
}
