package core

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
)

// ConfirmDecision is the outcome of a confirmation prompt for a
// tool call. It answers two questions: should this call run, and
// should future calls of this shape skip the prompt for the rest
// of the session.
type ConfirmDecision struct {
	Allow bool
	// Reason is shown to the model as the tool error when
	// Allow=false. Examples: "user declined", "user refused: rm -rf
	// looks dangerous".
	Reason string
	// RememberTool, when true, auto-allows every future call of the
	// same tool name for the rest of the session without prompting.
	RememberTool bool
	// RememberAll, when true, auto-allows every future call of any
	// tool for the rest of the session without prompting.
	// Effectively turns yolo back on for this session.
	RememberAll bool
	// PersistTool, when true, additionally asks the host to save an
	// allow rule for this tool name beyond the session (the gate
	// itself cannot write config; it reports through the OnPersist
	// callback). Implies RememberTool for the current session.
	PersistTool bool
	// PersistScopes narrows a PersistTool grant to the given args
	// patterns (RE2, one saved rule each) instead of the whole tool —
	// the confirmer echoes back the Pattern of the GrantScopes it
	// offered and the user accepted. A scoped grant deliberately does
	// NOT set the session-wide tool grant: the persisted rules take
	// over on the next policy refresh, and anything they don't match
	// must keep prompting.
	PersistScopes []string
}

// GrantScope is one derived narrow-grant option for a call: Display is the
// human-readable form a dialog shows ("git status"), Pattern the RE2 the
// host saves as a rule's args filter ("^git status(?:\s|$)"). The gate
// never derives these itself — the host injects a deriver (SetScopeDeriver)
// the same way the agent layer injects DecomposeCommand into the policy,
// so core stays free of tool-specific parsing.
type GrantScope struct {
	Display string
	Pattern string
}

// ConfirmRequest carries everything the gate knows about the call being
// confirmed. Scopes, when non-empty, are the derived narrow-grant options
// the dialog may offer alongside the blanket "always allow".
type ConfirmRequest struct {
	Tool    string
	Preview string
	CallID  string
	Scopes  []GrantScope
}

// Confirmer asks the user to approve or refuse a single tool call.
// Implementations block until the user responds.
//
// ctx is the CALL's context — the turn the tool call belongs to. An
// implementation that parks MUST select on ctx.Done() and return Allow=false
// with a cancellation reason when it fires. This is a hard requirement, not a
// courtesy: the caller blocks in ConfirmGate.Check, which blocks the turn
// goroutine, so a confirmer that ignores cancellation holds a cancelled turn
// open for as long as its own timeout allows.
//
// Before this parameter existed, four hosts each solved that privately and
// differently. rpc swept every parked ask on abort (blunt: it cancels other
// turns' asks too, and its own comment named the cause — "BeforeToolExecute is
// ctx-free"). The web daemon and ACP each stashed the live turn's context on
// the session and read it back at park time — which answers "the session's
// current turn", not "the turn this call belongs to". The TUI selected on
// nothing at all and relied on the front end to sweep. Bot mode parked on the
// DAEMON's context, so a cancelled turn's approval held the confirmer's mutex
// until the ask timed out and the next turn's first question queued behind it.
//
// One parameter replaces four answers, and it is the one the call site always
// had.
//
// preview is a short one-line summary of the args (the shell
// command, the file path, the URL) that the TUI shows alongside
// the tool name. It is intentionally short: no full tool outputs.
type Confirmer interface {
	Confirm(ctx context.Context, toolName string, preview string) ConfirmDecision
}

// ConfirmerWithCall is the optional upgrade for a Confirmer that needs the id
// of the call being gated: the gate passes the model's tool-call id (or the
// unique id a non-ladder door minted), so an implementation can key its
// parked prompt on the call itself instead of smuggling the id in through a
// host side-channel ahead of Check. That side-channel ("record the current
// call, tools run one at a time") is exactly what a host_tool_call approval
// racing a model call's breaks: two parks under one remembered id collide,
// and the loser can never be answered. The gate prefers ConfirmWithCall
// whenever the implementation offers it and a call id is known.
type ConfirmerWithCall interface {
	Confirmer
	ConfirmWithCall(ctx context.Context, toolName, preview, callID string) ConfirmDecision
}

// ConfirmerWithRequest is the richest confirmer shape: the gate hands over
// the whole ConfirmRequest, including any derived grant scopes, so the
// dialog can offer a call-dependent "always allow <command>" option. The
// gate prefers it over ConfirmerWithCall whenever the implementation
// offers it.
type ConfirmerWithRequest interface {
	Confirmer
	ConfirmWithRequest(ctx context.Context, req ConfirmRequest) ConfirmDecision
}

// ConfirmGate wraps a Confirmer with session-scoped memory for the
// "allow, always" decisions. Once the user picks RememberTool on a
// given tool name, the gate short-circuits subsequent calls of that
// name. Once the user picks RememberAll, the gate short-circuits
// everything for the rest of the session.
//
// Safe for concurrent use: the allow-lists are guarded by a mutex
// because the agent can queue tool calls from different goroutines.
type ConfirmGate struct {
	inner Confirmer

	// policy, when non-nil, is the typed rule/mode ladder evaluated
	// before any session memory or prompt (see PermissionPolicy).
	// Mutated only by copy-on-write under mu (SetMode), so a reader
	// that snapshots the pointer can use it lock-free afterward.
	policy *PermissionPolicy

	// onPersist, when set, is called after the user asks for a durable
	// allow grant (ConfirmDecision.PersistTool) — once per saved rule:
	// argsPattern is the RE2 the grant is narrowed to, "" for a blanket
	// tool grant. The host wires this to its config store; the gate
	// stays storage-agnostic.
	onPersist func(toolName, argsPattern string)

	// scopeDeriver, when set, turns a call's args into the narrow-grant
	// options offered to a ConfirmerWithRequest. Injected by the host
	// (core never parses tool args itself); nil means no scoped options.
	scopeDeriver func(toolName string, args json.RawMessage) []GrantScope

	// classifier, when non-nil and classifierMode is enabled, screens the
	// calls the policy decided to prompt on. See classifier.go: it is a
	// third axis (who answers), not a sixth approval mode, and it is off
	// unless the operator turned it on. Swapped under mu like the policy.
	classifier     Classifier
	classifierMode ClassifierMode

	mu          sync.Mutex
	allowAll    bool
	allowedTool map[string]bool
}

// NewConfirmGate returns a gate backed by inner with no policy (the
// historical --no-yolo shape: everything prompts). Inner can be nil;
// in that case every not-yet-allowed tool call is refused with a
// fixed reason (the gate is effectively a blocker until AllowAll /
// SetConfirmer is called).
func NewConfirmGate(inner Confirmer) *ConfirmGate {
	return NewPolicyGate(nil, inner)
}

// NewPolicyGate returns a gate that evaluates policy first, then
// falls back to session memory and the inner Confirmer for calls the
// policy says to ask about.
func NewPolicyGate(policy *PermissionPolicy, inner Confirmer) *ConfirmGate {
	return &ConfirmGate{
		policy:      policy,
		inner:       inner,
		allowedTool: map[string]bool{},
	}
}

// SetPersist installs the host callback for durable allow grants. The
// callback runs once per saved rule: argsPattern is the RE2 the grant is
// narrowed to ("^git status(?:\s|$)"), or "" for a blanket tool grant.
func (g *ConfirmGate) SetPersist(fn func(toolName, argsPattern string)) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.onPersist = fn
	g.mu.Unlock()
}

// SetScopeDeriver installs the hook that derives narrow-grant options from
// a call's args. Nil-safe; a nil or absent deriver means confirmers are
// offered no scoped options and grants stay tool-wide.
func (g *ConfirmGate) SetScopeDeriver(fn func(toolName string, args json.RawMessage) []GrantScope) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.scopeDeriver = fn
	g.mu.Unlock()
}

// SetClassifier installs (or removes) the screening classifier and the
// authority it holds. A nil classifier, or ClassifierOff, disables screening
// entirely and the gate behaves exactly as it did before the feature existed.
//
// Both are set together on purpose: a classifier with no mode, or a mode with
// no classifier, is a half-configured gate whose behaviour nobody can predict
// from reading one line of wiring.
func (g *ConfirmGate) SetClassifier(c Classifier, mode ClassifierMode) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.classifier, g.classifierMode = c, mode
	g.mu.Unlock()
}

// ClassifierMode reports the screening mode actually in force: ClassifierOff
// whenever no classifier is installed, so a caller (the status bar) can never
// advertise screening that is not happening. Nil-safe.
func (g *ConfirmGate) ClassifierMode() ClassifierMode {
	if g == nil {
		return ClassifierOff
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.classifier == nil {
		return ClassifierOff
	}
	return g.classifierMode
}

// Mode returns the gate's current approval mode (ApprovalYolo when the
// gate has no policy). Nil-safe.
func (g *ConfirmGate) Mode() ApprovalMode {
	if g == nil {
		return ApprovalYolo
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.policy == nil {
		return ApprovalYolo
	}
	return g.policy.Mode
}

// SetMode switches the approval mode at runtime by copy-on-write: a
// fresh policy with the new mode replaces the old pointer under the
// lock, so Check's snapshot-then-evaluate stays race-free. No-op on a
// gate with no policy (pure-yolo gates are never constructed in modes
// that allow switching). Nil-safe.
func (g *ConfirmGate) SetMode(m ApprovalMode) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.policy != nil {
		np := *g.policy
		np.Mode = m
		g.policy = &np
	}
	g.mu.Unlock()
}

// SetRules replaces the policy's rule list live (copy-on-write, like SetMode) —
// the next Check sees the new rules. The current Mode is preserved (a live mode
// change is not clobbered). Nil-safe; a no-op when the gate has no policy (the
// pure-yolo fast path), where rule edits are new-session-only.
func (g *ConfirmGate) SetRules(rules []PermissionRule) {
	if g == nil {
		return
	}
	g.mu.Lock()
	if g.policy != nil {
		np := *g.policy
		np.Rules = rules
		g.policy = &np
	}
	g.mu.Unlock()
}

// Rules returns a snapshot of the policy's ordered rules (for an
// inspector UI). Nil when the gate has no policy. Nil-safe.
func (g *ConfirmGate) Rules() []PermissionRule {
	if g == nil {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.policy == nil {
		return nil
	}
	return append([]PermissionRule(nil), g.policy.Rules...)
}

// Grants returns this session's "always allow" state: allowAll (the
// user picked "yes, always") and the sorted tool names granted "always
// this tool". For an inspector UI. Nil-safe.
func (g *ConfirmGate) Grants() (allowAll bool, tools []string) {
	if g == nil {
		return false, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for name := range g.allowedTool {
		tools = append(tools, name)
	}
	sort.Strings(tools)
	return g.allowAll, tools
}

// Check is the BeforeToolExecute-style entry point. Returns
// allowed, reason, modifiedArgs. modifiedArgs is always nil: the
// gate never rewrites args; it only allows or denies.
//
// callID names the call being decided — the model's tool-call id on the
// ladder door, a minted unique id on the host_tool_call / code_execution
// doors, "" when the caller has nothing (a ConfirmerWithCall then falls back
// to plain Confirm). The gate only forwards it; it never keys state on it.
//
// Order matters: the policy runs before the session cache so a deny
// rule beats a remembered "always allow" — explicit config outranks
// a session convenience.
//
// ctx is the calling turn's context. It reaches the inner Confirmer unchanged;
// the gate itself never parks, so it is the confirmer's to honour. A door that
// is not a turn (a worker's ask, a shutdown-scoped prompt) passes the context it
// genuinely lives under rather than a turn's.
//
// A nil ConfirmGate always allows (treat as yolo mode).
func (g *ConfirmGate) Check(ctx context.Context, toolName string, args json.RawMessage, preview, callID string) (bool, string, json.RawMessage) {
	if g == nil {
		return true, "", nil
	}

	g.mu.Lock()
	pol := g.policy
	g.mu.Unlock()
	switch verdict, reason := pol.Evaluate(toolName, args); verdict {
	case VerdictAllow:
		return true, "", nil
	case VerdictDeny:
		return false, reason, nil
	}

	g.mu.Lock()
	if g.allowAll || g.allowedTool[toolName] {
		g.mu.Unlock()
		return true, "", nil
	}
	inner := g.inner
	cls, clsMode := g.classifier, g.classifierMode
	g.mu.Unlock()

	// The classifier answers what would otherwise become a prompt, and its
	// position in this function is the whole design:
	//
	//   - AFTER the policy, so an explicit deny rule outranks any verdict a
	//     model can produce. Config the operator wrote beats a guess.
	//   - AFTER the session grants, so a tool they already answered "always"
	//     for does not spend a model call to be re-approved.
	//   - BEFORE the no-confirmer refusal below, so a headless run gets
	//     screened instead of blanket-refused. That is the case this exists
	//     for: nobody is there to answer, and the alternative is a call that
	//     waits forever or dies with a message the agent cannot act on.
	//   - BEFORE the prompt, because replacing the prompt is the point.
	if cls != nil && clsMode.Enabled() {
		// Never spend a model call on a turn that has already stopped: the
		// answer could only arrive too late to be used. The same check guards
		// the human prompt below, for the same reason.
		if err := ctx.Err(); err != nil {
			return false, "tool call refused: the turn was cancelled before this call could be approved", nil
		}
		mode := ApprovalYolo
		if pol != nil {
			mode = pol.Mode
		}
		switch res := cls.Classify(ctx, ClassifyRequest{
			Tool:    toolName,
			Args:    args,
			Preview: preview,
			Mode:    mode,
		}); res.Verdict {
		case ClassifyDeny:
			// Honoured in both modes: refusing only ever subtracts.
			reason := strings.TrimSpace(res.Reason)
			if reason == "" {
				// A denial the agent cannot learn from just gets a cosmetic
				// retry of the same call.
				reason = "tool call refused by the screening classifier, which gave no reason"
			}
			return false, reason, nil
		case ClassifyApprove:
			// An approval is AUTHORITY, so only approve mode may act on one.
			// In screen mode it is discarded and the prompt happens anyway —
			// that downgrade is what makes screen mode safe to leave on.
			if clsMode == ClassifierApprove {
				return true, "", nil
			}
		}
		// Abstain, and a discarded approve, fall through to the human below.
	}

	if inner == nil {
		return false, "tool call refused: confirmation is required (--no-yolo / approval mode) and there is no interactive prompt in this mode; ask the user what to do instead", nil
	}

	g.mu.Lock()
	derive := g.scopeDeriver
	g.mu.Unlock()
	var scopes []GrantScope
	if derive != nil {
		scopes = derive(toolName, args)
	}

	// A call that is already cancelled must not be asked about: the answer can
	// only arrive too late to be used, and asking spends a human's attention on
	// a turn that has stopped. Every confirmer would deny on its own ctx select
	// anyway; doing it here means the prompt never reaches a screen.
	if err := ctx.Err(); err != nil {
		return false, "tool call refused: the turn was cancelled before this call could be approved", nil
	}

	var decision ConfirmDecision
	switch cc := inner.(type) {
	case ConfirmerWithRequest:
		decision = cc.ConfirmWithRequest(ctx, ConfirmRequest{
			Tool:    toolName,
			Preview: preview,
			CallID:  callID,
			Scopes:  scopes,
		})
	case ConfirmerWithCall:
		if callID != "" {
			decision = cc.ConfirmWithCall(ctx, toolName, preview, callID)
		} else {
			decision = inner.Confirm(ctx, toolName, preview)
		}
	default:
		decision = inner.Confirm(ctx, toolName, preview)
	}

	g.mu.Lock()
	persist := g.onPersist
	scoped := len(decision.PersistScopes) > 0
	if decision.Allow {
		if decision.RememberAll {
			g.allowAll = true
		}
		// A scoped grant must NOT blanket-allow the tool for the
		// session: the persisted rules pick up matching calls after the
		// host's policy refresh, and everything else keeps prompting.
		if (decision.RememberTool || decision.PersistTool) && !scoped {
			g.allowedTool[toolName] = true
		}
	}
	g.mu.Unlock()
	if decision.Allow && decision.PersistTool && persist != nil {
		if scoped {
			for _, p := range decision.PersistScopes {
				persist(toolName, p)
			}
		} else {
			persist(toolName, "")
		}
	}

	reason := strings.TrimSpace(decision.Reason)
	if !decision.Allow && reason == "" {
		reason = "tool call refused by user"
	}
	return decision.Allow, reason, nil
}

// Reset clears the session memory. Invoked when the user toggles
// yolo mode back on via /yolo or closes the session.
func (g *ConfirmGate) Reset() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.allowAll = false
	g.allowedTool = map[string]bool{}
	g.mu.Unlock()
}

// AllowAll flips the gate into "always allow" for the rest of the
// session. Used by /yolo on to turn confirmation off at runtime
// without having to restart with the flag flipped.
func (g *ConfirmGate) AllowAll() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.allowAll = true
	g.mu.Unlock()
}

// Revoke takes back a single tool's session "always allow" grant. The
// next call of that tool prompts again (or follows the mode default).
// No-op if the tool was never granted. The session-wide allowAll flag
// is left alone — clear that with ClearAllowAll. Nil-safe. Used by the
// /permissions inspector so a grant given by accident can be undone
// without ending the session.
func (g *ConfirmGate) Revoke(toolName string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	delete(g.allowedTool, toolName)
	g.mu.Unlock()
}

// ClearAllowAll cancels a session-wide "yes, always" grant while
// leaving the per-tool grants in place — so dropping the blanket
// allowance doesn't also forget the specific tools you meant to keep.
// Nil-safe.
func (g *ConfirmGate) ClearAllowAll() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.allowAll = false
	g.mu.Unlock()
}

// SetConfirmer replaces the inner Confirmer. Used by the cli to
// hand the Interactive TUI to a gate that was constructed before
// the TUI existed (a construction-order knot we unwind here).
// Safe to call from any goroutine.
func (g *ConfirmGate) SetConfirmer(c Confirmer) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.inner = c
	g.mu.Unlock()
}

// BuildPreview turns a tool call's JSON args into a short one-line
// summary the TUI can show in the confirmation prompt. Prioritises
// obvious human-readable fields (command, path, url) over raw JSON.
// Returns at most maxLen characters.
func BuildPreview(args json.RawMessage, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 120
	}
	if len(args) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return truncatePreview(string(args), maxLen)
	}
	for _, k := range []string{"command", "path", "file_path", "url", "query", "name"} {
		if v, ok := m[k].(string); ok && v != "" {
			return truncatePreview(v, maxLen)
		}
	}
	b, _ := json.Marshal(m)
	return truncatePreview(string(b), maxLen)
}

func truncatePreview(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return "..."[:n]
	}
	return s[:n-3] + "..."
}
