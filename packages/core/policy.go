package core

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
)

// ReadOnlySet is the registry of side-effect-free tool names the
// approval modes consult. It is mutex-guarded because membership
// grows after gate construction: extension and MCP tools that declare
// read_only join when their managers merge into the registry, which
// may happen while the policy is live (mid-session reloads). Relying
// on "merges only happen between turns" would be exactly the
// call-ordering convention this fork replaces with contracts.
type ReadOnlySet struct {
	mu sync.RWMutex
	m  map[string]bool
}

// NewReadOnlySet builds a set from the initial (built-in) names.
func NewReadOnlySet(names ...string) *ReadOnlySet {
	s := &ReadOnlySet{m: make(map[string]bool, len(names))}
	for _, n := range names {
		s.m[n] = true
	}
	return s
}

// Add marks more tools read-only. Nil-safe.
func (s *ReadOnlySet) Add(names ...string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	for _, n := range names {
		s.m[n] = true
	}
	s.mu.Unlock()
}

// Has reports membership. Nil-safe (nil set contains nothing).
func (s *ReadOnlySet) Has(name string) bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.m[name]
}

// ApprovalMode names how much the agent may do without asking. It is
// one of two orthogonal axes — the other is the sandbox, which bounds
// what a tool can touch once it runs; the mode decides whether a tool
// call runs at all. Keeping the axes separate is deliberate (the
// conflated single-trust-level design is the known failure mode; see
// docs/plans/harness-landscape-2026.md A4/B6).
type ApprovalMode string

const (
	// ApprovalPlan permits read-only tools only. Mutating calls are
	// refused outright — not prompted — so the model is steered to
	// present a plan instead of attempting changes.
	ApprovalPlan ApprovalMode = "plan"
	// ApprovalAsk prompts for every tool call, read-only included:
	// it is the exact posture --no-yolo always had, kept bit-for-bit
	// so that flag can alias to it.
	ApprovalAsk ApprovalMode = "ask"
	// ApprovalAutoEdit auto-allows read-only tools and the file
	// editors (write/edit) and prompts for everything else — the
	// practical middle: file changes are cheap to review in a diff,
	// arbitrary commands are not.
	ApprovalAutoEdit ApprovalMode = "auto-edit"
	// ApprovalWorkspace trusts first-party tools and reads: every
	// built-in tool (read/write/edit/bash, bounded by the sandbox)
	// and every read-only tool — including read-only extension/MCP
	// tools — runs freely, while foreign tools that can have side
	// effects (a writing extension tool, a mutating MCP tool) prompt.
	// The trust axis here is origin (your own tools vs foreign code),
	// not just read-vs-write. The interactive default.
	ApprovalWorkspace ApprovalMode = "workspace"
	// ApprovalYolo auto-allows everything, foreign side-effecting
	// tools included.
	ApprovalYolo ApprovalMode = "yolo"
)

// ParseApprovalMode validates a mode string from a flag or config.
func ParseApprovalMode(s string) (ApprovalMode, error) {
	switch ApprovalMode(s) {
	case ApprovalPlan, ApprovalAsk, ApprovalAutoEdit, ApprovalWorkspace, ApprovalYolo:
		return ApprovalMode(s), nil
	}
	return "", fmt.Errorf("unknown approval mode %q (valid: plan, ask, auto-edit, workspace, yolo)", s)
}

// Authority classifies what kind of effect a tool can have, a finer
// distinction than the single read-only/mutating split. A tool's
// authority is declared by the tool (extension/MCP) or known for a
// built-in; it is advisory data the approval modes and UI consult, not
// a capability grant.
//
// The crucial distinction the read-only bool cannot express is
// network-read: a web fetch reads nothing on the local machine yet can
// leak data, trigger remote logging, or reach a private network, so it
// must NOT be folded into the local-read auto-allow. See
// docs/standard-tools.md.
type Authority string

const (
	// AuthLocalRead reads files/state under the jail with no process,
	// network, or external side effect. The only class that is
	// auto-allowable as "read-only" (plan-permitted, workspace/auto-edit
	// auto-allowed). The legacy read_only=true bool maps here.
	AuthLocalRead Authority = "local-read"
	// AuthWorkspaceMutate writes files / edits workspace state.
	AuthWorkspaceMutate Authority = "workspace-mutation"
	// AuthProcessExec starts commands or long-running subprocesses.
	AuthProcessExec Authority = "process-execution"
	// AuthNetworkRead fetches URLs / search results. Not local-read:
	// gated like a side-effecting tool until a host network policy
	// (allowlist) opts it in.
	AuthNetworkRead Authority = "network-read"
	// AuthExternalMutate writes to third-party APIs, sends messages,
	// opens PRs, changes remote resources.
	AuthExternalMutate Authority = "external-mutation"
	// AuthUserInteraction blocks to ask the user something (the
	// ask_user_question tool) with no other side effect. Always
	// permitted — gating a question behind an approval prompt, or
	// refusing it in plan mode, is nonsensical.
	AuthUserInteraction Authority = "user-interaction"
)

// IsReadOnlyAuthority reports whether a declared authority denotes a
// side-effect-free local read — the only class auto-allowable as
// "read-only". An empty/unknown value returns false so the caller falls
// back to the legacy read_only bool (empty) or treats the tool as
// side-effecting (unknown), both of which are the safe default.
func IsReadOnlyAuthority(a string) bool { return Authority(a) == AuthLocalRead }

// RuleDecision is what a matched permission rule does with a call.
type RuleDecision string

const (
	RuleAllow RuleDecision = "allow"
	RuleDeny  RuleDecision = "deny"
	// RuleAsk forces a confirmation prompt even in modes that would
	// auto-allow the call (and therefore refuses it in headless
	// modes, where no prompt exists) — except in yolo, which never
	// prompts: there an ask rule degrades to allow. Use deny, not ask,
	// to block something in yolo.
	RuleAsk RuleDecision = "ask"
)

// PermissionRule is one compiled, ordered permission rule. Rules are
// data, not config: compilation (regexp, validation, the
// project-rules-cannot-allow restriction) happens at the config
// boundary in packages/agent, so by the time a rule reaches the
// policy it is trusted and total.
type PermissionRule struct {
	// Tool matches the tool name: exact, or prefix glob when it ends
	// in '*' ("mcp_*").
	Tool string
	// Args, when non-nil, must additionally match the call's
	// arguments. It is tried against every top-level string field
	// value (so `^git (status|diff)` works for bash's command) and
	// against the compact JSON encoding as a fallback.
	Args *regexp.Regexp
	// Decision is what a match does.
	Decision RuleDecision
	// Reason is appended to deny refusals so the model knows why.
	Reason string
	// Source labels where the rule came from ("user", "project") for
	// refusal messages and debugging.
	Source string
}

// matches reports whether the rule applies to this call.
func (r PermissionRule) matches(toolName string, args json.RawMessage) bool {
	if strings.HasSuffix(r.Tool, "*") {
		if !strings.HasPrefix(toolName, strings.TrimSuffix(r.Tool, "*")) {
			return false
		}
	} else if r.Tool != toolName {
		return false
	}
	if r.Args == nil {
		return true
	}
	for _, txt := range argsMatchTexts(args) {
		if r.Args.MatchString(txt) {
			return true
		}
	}
	return false
}

// argsMatchTexts returns the strings a rule's Args pattern is tried
// against: each top-level string field value, then the compact JSON.
// Matching field values directly keeps patterns human ("^git status"
// instead of escaping JSON quoting around it).
func argsMatchTexts(args json.RawMessage) []string {
	var out []string
	var m map[string]any
	if err := json.Unmarshal(args, &m); err == nil {
		for _, v := range m {
			if s, ok := v.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	if b := strings.TrimSpace(string(args)); b != "" {
		out = append(out, b)
	}
	return out
}

// PolicyVerdict is the outcome of evaluating a call against the
// policy, before any session memory or interactive prompt is
// consulted.
type PolicyVerdict int

const (
	// VerdictAsk defers to the session cache and the Confirmer.
	VerdictAsk PolicyVerdict = iota
	// VerdictAllow runs the call without prompting.
	VerdictAllow
	// VerdictDeny refuses the call with a model-readable reason.
	VerdictDeny
)

// PermissionPolicy is the typed permission contract: an approval
// mode, an ordered rule list (first match wins), and the tool
// classification the modes need. The classification is explicit data
// supplied at construction — never derived by probing tool types —
// and unknown tools (extensions, MCP) default to mutating.
type PermissionPolicy struct {
	Mode ApprovalMode
	// Rules are evaluated in order; the first match decides. The
	// config layer orders project rules (restrict-only) ahead of
	// user rules.
	Rules []PermissionRule
	// ReadOnly names side-effect-free tools: auto-allowed in
	// auto-edit, the only tools permitted in plan. Built-ins seed it;
	// extension/MCP tools that declare read_only join at merge time.
	ReadOnly *ReadOnlySet
	// EditTools names the file editors auto-allowed in auto-edit
	// (write/edit). Static: only built-ins can be edit tools.
	EditTools map[string]bool
	// Builtin names the first-party tools (read/write/edit/bash and
	// the other tools terva itself registers). Used by workspace
	// mode to tell trusted built-ins from foreign extension/MCP
	// tools. Static, set at construction; anything not listed is
	// treated as foreign.
	Builtin map[string]bool
	// Interactive names tools whose only effect is asking the user
	// (ask_user_question). They are permitted in every mode, plan
	// included, and never prompt — gating a question behind approval is
	// nonsensical. Static, set at construction.
	Interactive map[string]bool
}

// Evaluate runs the decision ladder for one call:
//
//  1. plan mode refuses anything not read-only — before rules, so an
//     allow rule cannot punch a hole in an explicit plan posture;
//  2. first matching rule decides (deny refuses, allow runs, ask
//     forces a prompt even in auto-allowing modes);
//  3. the mode's default applies.
func (p *PermissionPolicy) Evaluate(toolName string, args json.RawMessage) (PolicyVerdict, string) {
	// No policy means no opinion: defer to the gate's session cache
	// and Confirmer, which is exactly the historical NewConfirmGate
	// (--no-yolo) behavior. Yolo's allow-everything lives in the nil
	// *gate* fast path, not here.
	if p == nil {
		return VerdictAsk, ""
	}
	// Interactive tools (ask the user) are permitted in every mode,
	// before the plan-deny check and without prompting: blocking a
	// clarifying question behind approval — or refusing it in plan —
	// makes no sense. The tool has no side effect beyond the prompt.
	if p.Interactive[toolName] {
		return VerdictAllow, ""
	}
	if p.Mode == ApprovalPlan && !p.ReadOnly.Has(toolName) {
		return VerdictDeny, "tool call refused: approval mode 'plan' permits read-only tools only; present the intended change to the user instead of making it"
	}
	for _, r := range p.Rules {
		if !r.matches(toolName, args) {
			continue
		}
		switch r.Decision {
		case RuleDeny:
			msg := fmt.Sprintf("tool call denied by %s permission rule", r.Source)
			if r.Reason != "" {
				msg += ": " + r.Reason
			}
			return VerdictDeny, msg
		case RuleAllow:
			return VerdictAllow, ""
		case RuleAsk:
			// yolo means "never prompt me": an ask rule only asks to
			// prompt, so in yolo it has nothing to enforce and the call
			// runs. deny rules still block above — deny, not ask, is how
			// you stop something even in yolo.
			if p.Mode == ApprovalYolo {
				return VerdictAllow, ""
			}
			return VerdictAsk, ""
		}
	}
	switch p.Mode {
	case ApprovalYolo:
		return VerdictAllow, ""
	case ApprovalWorkspace:
		// Trust first-party tools and reads (including read-only
		// foreign tools); foreign side-effecting tools fall through
		// to a prompt.
		if p.Builtin[toolName] || p.ReadOnly.Has(toolName) {
			return VerdictAllow, ""
		}
	case ApprovalAutoEdit:
		if p.ReadOnly.Has(toolName) || p.EditTools[toolName] {
			return VerdictAllow, ""
		}
	case ApprovalPlan:
		// Read-only call (the mutating case was refused above).
		return VerdictAllow, ""
	}
	return VerdictAsk, ""
}
