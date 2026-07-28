// Package permissions decides what a run may do: the effective approval mode,
// the layered rule set the confirm gate evaluates, the sandbox's jail posture,
// and the workspace-trust verdict those lean on.
//
// It is the config boundary of the permission subsystem. The evaluation ladder
// itself is packages/core/policy.go; the store, the paths and the on-disk state
// are packages/agent/config's. What lives here is the policy in between.
//
// # It does not import package build, and that is the point
//
// This was six files inside build, the DI container, where a resolver could
// read any of Args's seventy-three fields by reaching for it. The seven it
// actually reads are Inputs now, and build projects itself into that
// (Args.PermInputs) rather than this package reaching in. The dependency points
// one way: build asks permissions what is allowed, never the reverse. A back
// edge would be an import cycle, so the compiler is the guard.
//
// # Classifications are functions, not maps
//
// Which tools are read-only, first-party, editors, or interactive is what the
// ladder trusts. Those tables stay unexported and are reached through
// RegisterReadOnly/RegisterBuiltin to write and IsReadOnly/IsBuiltin (plus the
// *Names and *Set accessors) to read — because an exported map would let any
// importer silently reclassify a mutating tool as read-only, which is the one
// mistake the classification exists to prevent.
package permissions

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/mode"
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

// readOnly names the built-in tools with no side effects. This
// is the explicit classification the approval modes consume — plan
// permits only these, auto-edit auto-allows them. Tools not listed
// here (including every extension/MCP tool) are treated as mutating;
// a new built-in must be added deliberately.
var readOnly = map[string]bool{
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
	// share_file copies a file the agent could ALREADY read into terva's own
	// share area under $TERVA_HOME — never the workspace, never a diff — so the
	// same AuthorityLocalData rationale holds. Its reach is the sandbox's read
	// set, and its audience is the single user who owns the daemon; it grants no
	// authority the agent did not already have, it only makes the bytes
	// clickable. Alive in plan mode for deliver_result's reason: a session that
	// has drafted something for the user should be able to hand it over.
	"share_file": true,
	// worktree_list joins git's own worktree bookkeeping with the managed
	// registry and mutates neither beyond reconciling entries git already
	// dropped — the read-only pre-decision call. Its four siblings
	// (create/claim/release/remove) mutate git state and stay out of this map
	// on purpose: they classify like write/edit, not like reads.
	"worktree_list": true,
}

// editTools names the file editors auto-edit additionally allows:
// their effects show up in diffs and the session transcript, unlike
// arbitrary shell commands.
var editTools = map[string]bool{
	"write": true,
	"edit":  true,
}

// interactive names tools whose only effect is asking the user
// (core.AuthUserInteraction). They are permitted in every mode, plan
// included, and never prompt — and they are exempt from plan mode's
// registry pruning so the model can still ask while planning.
var interactive = map[string]bool{
	"ask_user_question": true,
}

// builtin names the first-party tools workspace mode trusts (alongside
// read-only tools); it asks only for foreign extension/MCP tools — and for
// the three first-party names deliberately kept OUT of this set (see
// toolclass_test.go's outside-trusted-origin list: generate_image and the
// restart pair prompt on purpose). Lists names that may not be registered in
// a given session (swarm_spawn, chat_send_*, the play tools) so they
// classify correctly whenever they are; anything not listed is treated as
// foreign.
var builtin = map[string]bool{
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
	"share_file":        true,
	"ask_user_question": true,
	"worktree_list":     true,
	"worktree_create":   true,
	"worktree_claim":    true,
	"worktree_release":  true,
	"worktree_remove":   true,
	// The play-and-deliberation four, trusted by decision (2026-07-27) after
	// the classification audit found them prompting as foreign while
	// swarm_spawn — which spawns TOOL-BEARING children at yolo — was trusted.
	// world_note/world_reveal touch only narrative state (append + reveal;
	// edits stay user verbs); actor_spawn voices a cast the user declared at
	// session creation, which WAS the consent; raati_convene spends model
	// calls but holds no tool authority and has swarm_spawn's live opt-in.
	"world_note":    true,
	"world_reveal":  true,
	"actor_spawn":   true,
	"raati_convene": true,
}

// Inputs is everything the permission resolvers read out of Args: seven
// fields of seventy-three.
//
// The resolvers used to take the whole Args, which said nothing true about
// their blast radius — you had to grep to learn that adding a field to Args
// could not affect a permission decision, and nothing stopped a resolver from
// quietly starting to read one. The seven are declared here instead, so the
// dependency is a thing you can read and a thing a guard can hold
// (TestPermissionLogicReadsOnlyPermArgs).
//
// It is deliberately NOT exported. The five exported entry points still take
// Args and project it here — every one of their eleven callers already holds
// an Args, so exporting this type would buy them a conversion at the call site
// and buy the reader nothing. What it buys is that the logic below the
// adapters no longer mentions Args, which is the precondition for moving this
// cluster out of package build (Q8 step b).
type Inputs struct {
	// Mode keys modePosture — the defaults a session inherits.
	Mode mode.Mode
	// CWD is the project whose trust state, project config, and persisted
	// unjail entry apply.
	CWD string
	// Approval is --approval, the highest-precedence answer.
	Approval string
	// NoYolo is --no-yolo, the alias for --approval=ask.
	NoYolo bool
	// Jail and NoJail are this run's explicit sandbox flags. NoJail wins if
	// both are given, as it always has.
	Jail   bool
	NoJail bool
	// Trust is --trust: trust the working directory for this invocation only.
	Trust bool
}

// modePosture is the security posture a run mode inherits when nothing
// overrides it: the approval mode its sessions default to, and whether the
// sandbox starts locked to the cwd.
//
// It is one table for the same reason modeSurface is (surface.go): a new run
// mode has to ANSWER what a session may do unattended, not inherit an answer
// from the tail of a boolean chain. Both halves used to be `||` chains in the
// two resolvers below, with this table living in hostcaps_test.go as a
// hand-maintained mirror asserted against them. The mirror is the code now —
// the rows cannot drift from the resolvers because the resolvers read them.
//
// docs/architecture/06-extensibility.md §1.5 carries the full host capability
// matrix, of which this is the security half.
//
// The rows worth reading twice:
//
//   - mode.Web is the one workspace-approval mode that is NOT jailed. That pair
//     is deliberate: workspace approval's trust-the-built-ins premise leans on
//     cwd confinement, which the web host provides by serving the workspace it
//     was started in.
//   - mode.Bot is jailed for a stronger reason than the other two: its user is
//     whoever can type into a chat room. It has always been jailed, but it rode
//     mode.Interactive's default to get there until it had a mode of its own;
//     botcmd's comment ("Jail is left alone: built-in file/shell tools stay
//     confined to the cwd") records that as deliberate. Named here now rather
//     than inherited by accident — the same fix as its approval row, where a
//     typo'd config value used to hand a chat room un-gated tools.
//   - mode.SwarmAgent runs yolo, and with no --approval on its argv (pinned in
//     swarm/runner_test.go) a child never even builds a gate object. It still
//     runs full extension discovery and the user's MCP set, so this row is a
//     security boundary, not a default: tightening or loosening it is a
//     decision, made here.
//   - mode.Attach and mode.Replay assemble no agent of their own; their rows say
//     what the resolvers would answer if one ever did.
var modePosture = map[mode.Mode]struct {
	approval core.ApprovalMode
	jailed   bool
}{
	// A real user is present to answer the foreign-tool prompts, so the
	// permission round-trip engages out of the box: in ACP a
	// session/request_permission to the editor, in web a broadcast approval
	// dialog to the browser, in a bot an ask in the paired chat via its
	// ChatConfirmer.
	mode.Interactive: {core.ApprovalWorkspace, true},
	mode.ACP:         {core.ApprovalWorkspace, true},
	mode.Bot:         {core.ApprovalWorkspace, true},
	mode.Web:         {core.ApprovalWorkspace, false},

	// Nobody to prompt, so a prompting default would refuse foreign tools and
	// break unattended automation; and no jail, so that automation isn't
	// surprised by path confinement.
	mode.Print:      {core.ApprovalYolo, false},
	mode.JSON:       {core.ApprovalYolo, false},
	mode.RPC:        {core.ApprovalYolo, false},
	mode.SwarmAgent: {core.ApprovalYolo, false},
	mode.Attach:     {core.ApprovalYolo, false},
	mode.Replay:     {core.ApprovalYolo, false},
}

// postureOf answers for a mode with no posture row, and it fails CLOSED: ask
// before every tool, and keep the sandbox locked to the cwd.
//
// It used to fail open — yolo, unjailed — because that is what the boolean
// chains it replaced did, by falling off the end rather than by deciding. That
// is the wrong direction for this question, and it is the wrong direction for
// the same reason SurfaceOf gives for going the other way: the two mistakes are
// not symmetric. An unknown mode wrongly asked to confirm costs a prompt
// somebody can answer. An unknown mode wrongly handed yolo runs every tool
// unconfirmed, anywhere on the filesystem, and says nothing while it does.
//
// Nothing in the shipping binary reaches this branch. Every declared Mode has a
// row (TestModePostureTableIsExhaustive), ParseArgs defaults Mode to
// mode.Interactive, and the two production sites that build an Args literal
// without one — ResolveAttachToken's and resolveProjectScoped's — read
// disjoint fields and never consult a posture. So this is a guard against a
// future caller, not a fix for a live defect: it costs nothing today and it
// decides which way the floor tilts when someone does reach it.
//
// The safe answer is ApprovalAsk rather than ApprovalWorkspace because
// workspace mode trusts the built-in tools, and trusting anything requires
// knowing what is running. We do not know that here. A host with no confirmer
// wired degrades this to refusal (HeadlessConfirmGate), which is also safe.
func postureOf(m mode.Mode) (core.ApprovalMode, bool) {
	if p, ok := modePosture[m]; ok {
		return p.approval, p.jailed
	}
	return core.ApprovalAsk, true
}

// ResolveApprovalMode picks the effective mode: flag beats the
// --no-yolo alias beats the user-config default beats the mode's
// built-in default (modePosture).
//
// A bot rarely reaches that last step — botRun settles args.Approval to yolo
// first, and an explicit --approval/--no-yolo/config value is answered above.
// It reaches it in exactly one case: a config `approval` string that does not
// parse. That case must fail toward asking, not toward running, which is what
// its posture row says.
func ResolveApprovalMode(p Inputs, cfg config.Config) core.ApprovalMode {
	if p.Approval != "" {
		if m, err := core.ParseApprovalMode(p.Approval); err == nil {
			return m
		}
	}
	if p.NoYolo {
		return core.ApprovalAsk
	}
	if cfg.Approval != "" {
		if m, err := core.ParseApprovalMode(cfg.Approval); err == nil {
			return m
		}
	}
	approval, _ := postureOf(p.Mode)
	return approval
}

// ResolveJail decides the sandbox's startup lock state.
//
// Precedence, highest first:
//
//	--jail / --no-jail        this run only (--no-jail wins if both are
//	                          given, as it always has). Either flag settles
//	                          it, so a stored exception never overrides what
//	                          the user just typed.
//	unjailed.json             a persisted per-directory exception
//	mode default              modePosture's jailed cell
//
// The default is on for the modes with a real user because it pairs with their
// workspace approval mode: that premise — trust the built-ins — holds precisely
// BECAUSE the built-ins are confined to the cwd. Web is the documented
// exception; modePosture says why.
//
// A store that cannot be read leaves the jail on and the error is surfaced by
// the caller (JailNotice). Failing closed is the only safe direction: a
// sandbox must never come down because a file was corrupt.
func ResolveJail(p Inputs) bool {
	switch {
	case p.NoJail:
		return false
	case p.Jail:
		return true
	}
	if unjailed, err := config.IsPathUnjailed(p.CWD); err == nil && unjailed {
		return false
	}
	_, jailed := postureOf(p.Mode)
	return jailed
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
func ResolveJailNotice(p Inputs, cfg config.Config) JailNotice {
	n := JailNotice{Jailed: ResolveJail(p)}
	if p.Jail || p.NoJail {
		return n // an explicit flag this run: the user just said it, don't nag
	}
	store, err := config.LoadUnjailStore()
	if err != nil {
		n.Err = err
		return n
	}
	ok, e := store.IsUnjailed(p.CWD)
	if !ok {
		return n
	}
	n.Persisted = true
	n.Entry = e
	switch ResolveApprovalMode(p, cfg) {
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
// EffectiveApprovalMode).
func JailNoticeFor(p Inputs) JailNotice {
	cfg, err := config.LoadConfig()
	if err != nil {
		cfg = config.Config{}
	}
	return ResolveJailNotice(p, cfg)
}

// WarnPersistentlyUnjailed prints the notice to stderr for the modes with no
// status line. The counterpart of WarnRestrictedWorkspace (trust_cli.go), and
// the same stance: inform, never prompt.
func WarnPersistentlyUnjailed(p Inputs) {
	if msg := JailNoticeFor(p).Message(); msg != "" {
		fmt.Fprintf(os.Stderr, "terva: %s\n", msg)
	}
}

// EffectiveApprovalMode is ResolveApprovalMode with the user config
// loaded — for call sites (tool-registry build) that don't already
// hold a Config.
func EffectiveApprovalMode(p Inputs) core.ApprovalMode {
	cfg, _ := config.LoadConfig()
	return ResolveApprovalMode(p, cfg)
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

// BuildPolicy assembles the policy for this invocation:
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
func BuildPolicy(p Inputs) (*core.PermissionPolicy, []string) {
	var warns []string
	cfg, err := config.LoadConfig()
	if err != nil {
		warns = append(warns, fmt.Sprintf("permissions: user config unreadable, rules ignored: %v", err))
	}
	mode := ResolveApprovalMode(p, cfg)

	var rules []core.PermissionRule
	ur, uw := compilePermissionRules(cfg.Permissions, "user", false)
	rules = append(rules, ur...)
	warns = append(warns, uw...)
	if pc, perr := config.LoadProjectConfig(p.CWD); perr == nil && pc != nil {
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
	er, ew := extensionPermissionRules(p.CWD, ResolveTrustState(p.CWD, p.Trust).IsTrusted())
	rules = append(rules, er...)
	warns = append(warns, ew...)

	if mode == core.ApprovalYolo && len(rules) == 0 {
		return nil, warns
	}
	return &core.PermissionPolicy{
		Mode:             mode,
		Rules:            rules,
		ReadOnly:         BuiltinReadOnlySet(),
		EditTools:        editTools,
		Builtin:          builtin,
		Interactive:      interactive,
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
	names := make([]string, 0, len(readOnly))
	for n := range readOnly {
		names = append(names, n)
	}
	return core.NewReadOnlySet(names...)
}

// The classification tables are maps, and a map is not an API.
//
// scripting_on.go (a build-tagged file in package build) adds code_execution to
// two of them at registration time. While everything lived in one package that
// was a map write; across a package boundary it would have to be an EXPORTED
// map write, which hands every importer the ability to reclassify any tool —
// including silently marking a mutating tool read-only, which is the one
// mistake this classification exists to prevent. So the mutation gets a name
// and the reads get predicates, and the maps stay unexported.
//
// Registration happens during package init and is not safe to call afterwards:
// the tables are read without synchronization by every gate check.

// RegisterReadOnly classifies a tool as having no side effects — plan mode
// permits it and auto-edit auto-allows it. The claim must follow the tool's
// actual capability: a tool that gains a mutating path has to lose this entry
// in the same commit.
func RegisterReadOnly(name string) { readOnly[name] = true }

// RegisterBuiltin marks a tool as first-party, so workspace mode trusts it
// instead of prompting the way it does for extension and MCP tools.
func RegisterBuiltin(name string) { builtin[name] = true }

// IsReadOnly reports the read-only classification. Plan mode's registry pruning
// asks this; so does the policy ladder, through BuiltinReadOnlySet.
func IsReadOnly(name string) bool { return readOnly[name] }

// IsInteractive reports whether a tool's only effect is asking the user
// (core.AuthUserInteraction). Those are permitted in every mode, plan included,
// and are exempt from plan mode's pruning so the model can still ask.
func IsInteractive(name string) bool { return interactive[name] }

// The name accessors exist for one caller: build's tool-classification census,
// which checks that every registered tool is classified and that no
// classification outlives its tool. That check needs the registry (build's) and
// the tables (this package's), and package permissions must not import build —
// so the tables come to it, as sorted copies rather than the live maps.

// IsBuiltin reports whether a tool is first-party and therefore trusted by
// workspace approval mode.
func IsBuiltin(name string) bool { return builtin[name] }

// BuiltinNames, ReadOnlyNames and EditToolNames return each classification's
// members, sorted, as a fresh slice.
func BuiltinNames() []string     { return sortedKeys(builtin) }
func ReadOnlyNames() []string    { return sortedKeys(readOnly) }
func EditToolNames() []string    { return sortedKeys(editTools) }
func InteractiveNames() []string { return sortedKeys(interactive) }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// EditToolSet and BuiltinSet return a classification as a fresh map, for a
// caller assembling a core.PermissionPolicy by hand — acp_mode.go does, and so
// do the ladder tests. Copies, so a hand-built policy cannot reclassify the
// real one, which an exported map would have allowed.
func EditToolSet() map[string]bool {
	out := make(map[string]bool, len(editTools))
	for k, v := range editTools {
		out[k] = v
	}
	return out
}

// BuiltinSet returns the first-party classification as a fresh map.
func BuiltinSet() map[string]bool {
	out := make(map[string]bool, len(builtin))
	for k, v := range builtin {
		out[k] = v
	}
	return out
}
