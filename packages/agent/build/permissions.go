package build

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/envcompat"
)

// This file is the config boundary of the permission subsystem: it
// resolves the effective approval mode, compiles JSON rule configs
// into typed core.PermissionRules (enforcing the project-layer
// self-approval ban), and assembles the core.PermissionPolicy the
// confirm gate evaluates. The evaluation ladder itself lives in
// packages/core/policy.go.

// readOnlyTools names the built-in tools with no side effects. This
// is the explicit classification the approval modes consume — plan
// permits only these, auto-edit auto-allows them. Tools not listed
// here (including every extension/MCP tool) are treated as mutating;
// a new built-in must be added deliberately.
var readOnlyTools = map[string]bool{
	"read":            true,
	"grep":            true,
	"glob":            true,
	"terva_status":    true,
	"session_inspect": true,
	"skill":           true,
	// The task tools mutate only terva's own task board under $TERVA_HOME — never
	// the workspace — so they auto-admit and stay available in plan mode like a
	// read-only tool (the built-in equivalent of the former extension's
	// AuthorityLocalData). task_list is a pure read; create/update/archive touch
	// only that board, which never appears in a workspace diff.
	"task_list":    true,
	"task_create":  true,
	"task_update":  true,
	"task_archive": true,
	// activate_tools only changes which tool schemas are advertised this session
	// (lazy tool visibility, retro H2·b) — visibility, never authority — so it
	// auto-admits without a confirm, like session_inspect. Each tool it reveals
	// still faces its own gate when actually called.
	"activate_tools": true,
	// deliver_result writes only the swarm state dir under the terva home —
	// never the workspace — the same AuthorityLocalData rationale as the task
	// tools above. Read-only classification also keeps it alive in plan mode,
	// where a dispatched reviewer must still deliver its findings.
	"deliver_result": true,
	// worktree_list joins git's own worktree bookkeeping with the managed
	// registry and mutates neither beyond reconciling entries git already
	// dropped — the read-only pre-decision call. Its four siblings
	// (create/claim/release/remove) mutate git state and stay out of this map
	// on purpose: they classify like write/edit, not like reads.
	"worktree_list": true,
}

// EditTools names the file editors auto-edit additionally allows:
// their effects show up in diffs and the session transcript, unlike
// arbitrary shell commands.
var EditTools = map[string]bool{
	"write": true,
	"edit":  true,
}

// interactiveTools names tools whose only effect is asking the user
// (core.AuthUserInteraction). They are permitted in every mode, plan
// included, and never prompt — and they are exempt from plan mode's
// registry pruning so the model can still ask while planning.
var interactiveTools = map[string]bool{
	"ask_user_question": true,
}

// BuiltinTools names every first-party tool terva registers. Workspace
// mode trusts these (and read-only tools) and asks only for foreign
// extension/MCP tools. Lists names that may not be registered in a
// given session (swarm_spawn, chat_send_*) so they classify correctly
// whenever they are; anything not listed is treated as foreign.
var BuiltinTools = map[string]bool{
	"read":              true,
	"write":             true,
	"edit":              true,
	"bash":              true,
	"grep":              true,
	"glob":              true,
	"terva_status":      true,
	"session_inspect":   true,
	"skill":             true,
	"task_list":         true,
	"task_create":       true,
	"task_update":       true,
	"task_archive":      true,
	"activate_tools":    true,
	"deliver_result":    true,
	"swarm_spawn":       true,
	"chat_send_image":   true,
	"chat_send_file":    true,
	"ask_user_question": true,
	"worktree_list":     true,
	"worktree_create":   true,
	"worktree_claim":    true,
	"worktree_release":  true,
	"worktree_remove":   true,
}

// ResolveApprovalMode picks the effective mode: flag beats the
// --no-yolo alias beats the user-config default beats the built-in
// default. The built-in default is workspace for an interactive
// session (a human is present to answer the foreign-tool prompts) and
// yolo for headless modes (no prompt exists, so a prompting default
// would refuse foreign tools and break unattended automation).
func ResolveApprovalMode(args Args, cfg config.Config) core.ApprovalMode {
	if args.Approval != "" {
		if m, err := core.ParseApprovalMode(args.Approval); err == nil {
			return m
		}
	}
	if args.NoYolo {
		return core.ApprovalAsk
	}
	if cfg.Approval != "" {
		if m, err := core.ParseApprovalMode(cfg.Approval); err == nil {
			return m
		}
	}
	// Interactive (TUI), ACP, web, and a bot all have a real user to ask, so
	// they default to workspace — the permission round-trip engages out of the
	// box (in ACP, session/request_permission to the editor; in web, a broadcast
	// approval dialog to the browser; in a bot, an ask in the paired chat via
	// its ChatConfirmer). Other headless modes (rpc/json/print/swarm) have no one
	// to prompt, so they stay yolo.
	//
	// The bot rarely reaches this line — botRun settles args.Approval to yolo
	// first, and an explicit --approval/--no-yolo/config value is answered above.
	// It reaches it in exactly one case: a config `approval` string that does not
	// parse. That case must fail toward asking, not toward running. A bot used to
	// land here as ModeInteractive and get workspace; now that it has a mode of
	// its own, it has to be listed by name or a typo'd config would quietly hand
	// a chat room un-gated tools.
	if args.Mode == ModeInteractive || args.Mode == ModeACP || args.Mode == ModeWeb || args.Mode == ModeBot {
		return core.ApprovalWorkspace
	}
	return core.ApprovalYolo
}

// resolveJail decides the sandbox's startup lock state.
//
// Precedence, highest first:
//
//	--jail / --no-jail        this run only (--no-jail wins if both are
//	                          given, as it always has). Either flag settles
//	                          it, so a stored exception never overrides what
//	                          the user just typed.
//	unjailed.json             a persisted per-directory exception
//	mode default              on for sessions with a real user (interactive,
//	                          ACP, bot), off for the headless modes
//
// The default is on for interactive and ACP because it pairs with their
// workspace approval mode: that mode's trust-the-built-ins premise holds
// precisely BECAUSE the built-ins are confined to the cwd. It's off for the
// other headless modes (rpc/json/print/swarm) so unattended automation isn't
// surprised by path confinement.
//
// A bot is jailed for a different reason than the other two, and it is the
// stronger one: its user is whoever can type into a chat room. It has always
// been jailed — it rode ModeInteractive's default to get there, and botcmd's
// comment ("Jail is left alone: built-in file/shell tools stay confined to the
// cwd") records that as deliberate. Now that a bot has a mode of its own, the
// intent is named here instead of inherited by accident.
//
// A store that cannot be read leaves the jail on and the error is surfaced by
// the caller (JailNotice). Failing closed is the only safe direction: a
// sandbox must never come down because a file was corrupt.
func resolveJail(args Args) bool {
	switch {
	case args.NoJail:
		return false
	case args.Jail:
		return true
	}
	if unjailed, err := config.IsPathUnjailed(args.CWD); err == nil && unjailed {
		return false
	}
	return args.Mode == ModeInteractive || args.Mode == ModeACP || args.Mode == ModeBot
}

// JailNotice describes the resolved jail posture for the user-facing surfaces.
// Persisted reports that the sandbox is down because of a stored decision
// rather than a flag — a state the user made once and will not otherwise be
// reminded of.
type JailNotice struct {
	Jailed    bool
	Persisted bool
	Entry     config.UnjailEntry
	// AutoApproved is the combination worth saying out loud: the sandbox is
	// down AND the approval mode auto-approves the built-in tools. Each is a
	// deliberate choice; together they mean built-in tools may write anywhere
	// on the filesystem without asking, and nothing else on screen says so.
	AutoApproved bool
	Err          error
}

// ResolveJailNotice resolves the jail posture along with why, for the callers
// that have to tell the user about it.
func ResolveJailNotice(args Args, cfg config.Config) JailNotice {
	n := JailNotice{Jailed: resolveJail(args)}
	if args.Jail || args.NoJail {
		return n // an explicit flag this run: the user just said it, don't nag
	}
	store, err := config.LoadUnjailStore()
	if err != nil {
		n.Err = err
		return n
	}
	ok, e := store.IsUnjailed(args.CWD)
	if !ok {
		return n
	}
	n.Persisted = true
	n.Entry = e
	switch ResolveApprovalMode(args, cfg) {
	case core.ApprovalWorkspace, core.ApprovalYolo:
		n.AutoApproved = true
	}
	return n
}

// Message renders the notice for a user-facing surface, or "" when there is
// nothing worth saying. A persisted unjail is worth saying: unlike a flag, the
// user set it once — possibly long ago — and the status bar signals it only by
// the ABSENCE of a "jailed" badge, which is no signal at all.
func (n JailNotice) Message() string {
	if n.Err != nil {
		// The store is unreadable, so the jail stayed on. Say so: silence here
		// would look identical to "you have no unjail rules", and the user
		// would wonder why their directory is suddenly confined.
		return fmt.Sprintf("could not read the unjail list (%v) — staying jailed", n.Err)
	}
	if !n.Persisted {
		return ""
	}
	if n.AutoApproved {
		return fmt.Sprintf("unjailed by a saved rule for %s, and the approval mode auto-approves built-in tools: "+
			"they may read and write anywhere without asking. `terva jail` to undo", n.Entry.Real)
	}
	return fmt.Sprintf("unjailed by a saved rule for %s — tools may read and write outside it. `terva jail` to undo", n.Entry.Real)
}

// JailNoticeFor is ResolveJailNotice with the user config loaded, for the call
// sites that don't already hold one (same convenience shape as
// effectiveApprovalMode).
func JailNoticeFor(args Args) JailNotice {
	cfg, err := config.LoadConfig()
	if err != nil {
		cfg = config.Config{}
	}
	return ResolveJailNotice(args, cfg)
}

// WarnPersistentlyUnjailed prints the notice to stderr for the modes with no
// status line. The counterpart of WarnRestrictedWorkspace (trust_cli.go), and
// the same stance: inform, never prompt.
func WarnPersistentlyUnjailed(args Args) {
	if msg := JailNoticeFor(args).Message(); msg != "" {
		fmt.Fprintf(os.Stderr, "terva: %s\n", msg)
	}
}

// effectiveApprovalMode is ResolveApprovalMode with the user config
// loaded — for call sites (tool-registry build) that don't already
// hold a Config.
func effectiveApprovalMode(args Args) core.ApprovalMode {
	cfg, _ := config.LoadConfig()
	return ResolveApprovalMode(args, cfg)
}

// compilePermissionRules turns config rules into typed rules.
// projectLayer enforces the self-approval ban: an untrusted project
// rule may deny or ask, never allow. Broken rules (bad decision, bad
// regexp, empty tool) are dropped with a warning instead of failing
// startup — a typo in one rule must not take the whole binary down.
func compilePermissionRules(rules []config.PermissionRuleConfig, source string, projectLayer bool) ([]core.PermissionRule, []string) {
	var out []core.PermissionRule
	var warns []string
	for i, rc := range rules {
		warn := func(msg string) {
			warns = append(warns, fmt.Sprintf("%s permission rule %d dropped: %s", source, i+1, msg))
		}
		if strings.TrimSpace(rc.Tool) == "" {
			warn("tool is required")
			continue
		}
		dec := core.RuleDecision(strings.ToLower(strings.TrimSpace(rc.Decision)))
		switch dec {
		case core.RuleAllow, core.RuleDeny, core.RuleAsk:
		default:
			warn(fmt.Sprintf("unknown decision %q (valid: allow, deny, ask)", rc.Decision))
			continue
		}
		if projectLayer && dec == core.RuleAllow {
			warn("project rules may not allow (a cloned repo cannot grant itself tool access); use deny or ask")
			continue
		}
		var re *regexp.Regexp
		if rc.Args != "" {
			var err error
			re, err = regexp.Compile(rc.Args)
			if err != nil {
				warn(fmt.Sprintf("args regexp: %v", err))
				continue
			}
		}
		out = append(out, core.PermissionRule{
			Tool:     strings.TrimSpace(rc.Tool),
			Args:     re,
			Decision: dec,
			Reason:   rc.Reason,
			Source:   source,
		})
	}
	return out, warns
}

// BuildPermissionPolicy assembles the policy for this invocation:
// effective mode, the layered rule list, and the built-in tool
// classification. Returns nil when the mode is yolo and no rules exist
// — the historical no-gate fast path.
//
// Layer order (first match wins): user, then project, then extension.
// The user is sovereign on their own machine — an explicit user rule
// outranks any restrict-only layer, and the user layer is the only one
// that may `allow`. The restrict-only layers apply only where no
// higher layer already decided: a repo-specific project rule beats a
// global extension default, both can tighten but never grant.
func BuildPermissionPolicy(args Args) (*core.PermissionPolicy, []string) {
	var warns []string
	cfg, err := config.LoadConfig()
	if err != nil {
		warns = append(warns, fmt.Sprintf("permissions: user config unreadable, rules ignored: %v", err))
	}
	mode := ResolveApprovalMode(args, cfg)

	var rules []core.PermissionRule
	ur, uw := compilePermissionRules(cfg.Permissions, "user", false)
	rules = append(rules, ur...)
	warns = append(warns, uw...)
	if pc, perr := config.LoadProjectConfig(args.CWD); perr == nil && pc != nil {
		pr, w := compilePermissionRules(pc.Permissions, "project", true)
		rules = append(rules, pr...)
		warns = append(warns, w...)
	}
	// Extension-suggested rules from PROJECT extension bundles are gated
	// on Workspace Trust: an untrusted project's extensions don't load,
	// so their suggested deny/ask rules must not apply either. User-ext
	// (global) suggestions always apply. The project's own restrict-only
	// rules above stay honored even when untrusted — they can only tighten
	// (rows 4/5 of docs/plans/workspace-trust.md).
	er, ew := extensionPermissionRules(args.CWD, ResolveTrustState(args).IsTrusted())
	rules = append(rules, er...)
	warns = append(warns, ew...)

	if mode == core.ApprovalYolo && len(rules) == 0 {
		return nil, warns
	}
	return &core.PermissionPolicy{
		Mode:             mode,
		Rules:            rules,
		ReadOnly:         BuiltinReadOnlySet(),
		EditTools:        EditTools,
		Builtin:          BuiltinTools,
		Interactive:      interactiveTools,
		DecomposeCommand: decomposeBashForPolicy,
	}, warns
}

// decomposeBashForPolicy is the shell splitter the permission policy
// injects (core.PermissionPolicy.DecomposeCommand). For a bash call that
// runs more than one command it returns one synthetic `{"command": …}`
// args object per command, so the policy judges each against the rules
// independently; for anything else it returns nil and the call is judged
// as a single unit. The shell parsing lives here in packages/agent/tools
// so packages/core stays free of a shell grammar.
func decomposeBashForPolicy(toolName string, args json.RawMessage) []json.RawMessage {
	if toolName != "bash" {
		return nil
	}
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil
	}
	scopes := tools.DecomposeBashCommand(a.Command)
	if len(scopes) < 2 {
		return nil
	}
	out := make([]json.RawMessage, 0, len(scopes))
	for _, s := range scopes {
		b, err := json.Marshal(struct {
			Command string `json:"command"`
		}{Command: s})
		if err != nil {
			continue
		}
		out = append(out, b)
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// DeriveGrantScopes is the narrow-grant deriver hosts install on a
// ConfirmGate (core.ConfirmGate.SetScopeDeriver): for a bash call it turns
// the command into the anchored "always allow <command>" options the
// permission dialog offers next to the blanket grant. Bash-only on
// purpose — other tools' args have no command grammar to anchor on, and
// mode defaults already auto-allow the read-only ones. Same layering as
// decomposeBashForPolicy above: the shell parsing stays out of core.
func DeriveGrantScopes(toolName string, args json.RawMessage) []core.GrantScope {
	if toolName != "bash" {
		return nil
	}
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return nil
	}
	return tools.BashGrantScopes(a.Command)
}

// extensionPermissionRules collects suggested rules from installed
// extension bundles (the `permissions` Key in extension.json — see
// docs/extensions.md). Extensions are code the user installed, but
// their *suggested policy* still may only restrict: like project
// rules, allow is rejected, so installing a bundle can tighten the
// posture but never grant tool access the user didn't. Evaluated last
// (after user and project): a bundle's ask/deny is a default the user
// can always override with an explicit allow in their own config, and
// a project rule wins over it where the user is silent.
// trustProject gates the PROJECT extension roots: an untrusted workspace
// contributes no project-ext-suggested rules (its extensions don't load).
// The user-ext (global) root is always read.
func extensionPermissionRules(cwd string, trustProject bool) ([]core.PermissionRule, []string) {
	var roots []string
	if home := config.TervaHome(); home != "" {
		roots = append(roots, filepath.Join(home, "extensions"))
	}
	if cwd != "" && trustProject {
		for _, dirName := range envcompat.ProjectDirNames() {
			roots = append(roots, filepath.Join(cwd, dirName, "extensions"))
		}
	}
	var rules []core.PermissionRule
	var warns []string
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			mb, err := os.ReadFile(filepath.Join(root, e.Name(), "extension.json"))
			if err != nil {
				continue
			}
			var m struct {
				Name        string                        `json:"name"`
				Enabled     *bool                         `json:"enabled"`
				Permissions []config.PermissionRuleConfig `json:"permissions"`
			}
			if json.Unmarshal(mb, &m) != nil || (m.Enabled != nil && !*m.Enabled) || len(m.Permissions) == 0 {
				continue
			}
			source := "extension"
			if m.Name != "" {
				source = "extension " + m.Name
			}
			r, w := compilePermissionRules(m.Permissions, source, true)
			rules = append(rules, r...)
			warns = append(warns, w...)
		}
	}
	return rules, warns
}

// BuiltinReadOnlySet seeds the policy's dynamic read-only registry
// with the built-in classification. Extension/MCP tools that declare
// read_only join later via Resolved.MergeExtensionTools.
func BuiltinReadOnlySet() *core.ReadOnlySet {
	names := make([]string, 0, len(readOnlyTools))
	for n := range readOnlyTools {
		names = append(names, n)
	}
	return core.NewReadOnlySet(names...)
}
