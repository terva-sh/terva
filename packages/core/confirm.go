package core

import (
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
}

// Confirmer asks the user to approve or refuse a single tool call.
// Implementations block until the user responds (or the agent's
// context is cancelled, in which case they should return
// Allow=false with a cancellation reason).
//
// preview is a short one-line summary of the args (the shell
// command, the file path, the URL) that the TUI shows alongside
// the tool name. It is intentionally short: no full tool outputs.
type Confirmer interface {
	Confirm(toolName string, preview string) ConfirmDecision
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

	// onPersist, when set, is called with a tool name after the user
	// asks for a durable allow grant (ConfirmDecision.PersistTool).
	// The host wires this to its config store; the gate stays
	// storage-agnostic.
	onPersist func(toolName string)

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

// SetPersist installs the host callback for durable allow grants.
func (g *ConfirmGate) SetPersist(fn func(toolName string)) {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.onPersist = fn
	g.mu.Unlock()
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
// Order matters: the policy runs before the session cache so a deny
// rule beats a remembered "always allow" — explicit config outranks
// a session convenience.
//
// A nil ConfirmGate always allows (treat as yolo mode).
func (g *ConfirmGate) Check(toolName string, args json.RawMessage, preview string) (bool, string, json.RawMessage) {
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
	g.mu.Unlock()
	if inner == nil {
		return false, "tool call refused: confirmation is required (--no-yolo / approval mode) and there is no interactive prompt in this mode; ask the user what to do instead", nil
	}

	decision := inner.Confirm(toolName, preview)

	g.mu.Lock()
	persist := g.onPersist
	if decision.Allow {
		if decision.RememberAll {
			g.allowAll = true
		}
		if decision.RememberTool || decision.PersistTool {
			g.allowedTool[toolName] = true
		}
	}
	g.mu.Unlock()
	if decision.Allow && decision.PersistTool && persist != nil {
		persist(toolName)
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
