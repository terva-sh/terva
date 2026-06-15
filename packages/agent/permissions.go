package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
	"read":         true,
	"terva_status": true,
	"skill":        true,
}

// editTools names the file editors auto-edit additionally allows:
// their effects show up in diffs and the session transcript, unlike
// arbitrary shell commands.
var editTools = map[string]bool{
	"write": true,
	"edit":  true,
}

// builtinTools names every first-party tool terva registers. Workspace
// mode trusts these (and read-only tools) and asks only for foreign
// extension/MCP tools. Lists names that may not be registered in a
// given session (swarm_spawn, chat_send_*) so they classify correctly
// whenever they are; anything not listed is treated as foreign.
var builtinTools = map[string]bool{
	"read":            true,
	"write":           true,
	"edit":            true,
	"bash":            true,
	"terva_status":    true,
	"skill":           true,
	"swarm_spawn":     true,
	"chat_send_image": true,
	"chat_send_file":  true,
}

// resolveApprovalMode picks the effective mode: flag beats the
// --no-yolo alias beats the user-config default beats the built-in
// default. The built-in default is workspace for an interactive
// session (a human is present to answer the foreign-tool prompts) and
// yolo for headless modes (no prompt exists, so a prompting default
// would refuse foreign tools and break unattended automation).
func resolveApprovalMode(args Args, cfg Config) core.ApprovalMode {
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
	// Interactive (TUI) and ACP both have a real user to ask, so they default
	// to workspace — the permission round-trip engages out of the box (in ACP,
	// session/request_permission to the editor). Other headless modes (rpc/json/
	// print/swarm) have no one to prompt, so they stay yolo.
	if args.Mode == ModeInteractive || args.Mode == ModeACP {
		return core.ApprovalWorkspace
	}
	return core.ApprovalYolo
}

// resolveJail decides the sandbox's startup lock state: --no-jail /
// --jail win; otherwise the default is on for sessions with a real user —
// interactive (TUI) and ACP — pairing with their workspace approval mode so
// its trust-the-built-ins premise holds (built-in tools are confined to the
// cwd); it's off for the other headless modes (rpc/json/print/swarm) so
// unattended automation isn't surprised by path confinement.
func resolveJail(args Args) bool {
	switch {
	case args.NoJail:
		return false
	case args.Jail:
		return true
	default:
		return args.Mode == ModeInteractive || args.Mode == ModeACP
	}
}

// effectiveApprovalMode is resolveApprovalMode with the user config
// loaded — for call sites (tool-registry build) that don't already
// hold a Config.
func effectiveApprovalMode(args Args) core.ApprovalMode {
	cfg, _ := LoadConfig()
	return resolveApprovalMode(args, cfg)
}

// compilePermissionRules turns config rules into typed rules.
// projectLayer enforces the self-approval ban: an untrusted project
// rule may deny or ask, never allow. Broken rules (bad decision, bad
// regexp, empty tool) are dropped with a warning instead of failing
// startup — a typo in one rule must not take the whole binary down.
func compilePermissionRules(rules []PermissionRuleConfig, source string, projectLayer bool) ([]core.PermissionRule, []string) {
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

// buildPermissionPolicy assembles the policy for this invocation:
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
func buildPermissionPolicy(args Args) (*core.PermissionPolicy, []string) {
	var warns []string
	cfg, err := LoadConfig()
	if err != nil {
		warns = append(warns, fmt.Sprintf("permissions: user config unreadable, rules ignored: %v", err))
	}
	mode := resolveApprovalMode(args, cfg)

	var rules []core.PermissionRule
	ur, uw := compilePermissionRules(cfg.Permissions, "user", false)
	rules = append(rules, ur...)
	warns = append(warns, uw...)
	if pc, perr := LoadProjectConfig(args.CWD); perr == nil && pc != nil {
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
	er, ew := extensionPermissionRules(args.CWD, resolveTrustState(args).IsTrusted())
	rules = append(rules, er...)
	warns = append(warns, ew...)

	if mode == core.ApprovalYolo && len(rules) == 0 {
		return nil, warns
	}
	return &core.PermissionPolicy{
		Mode:      mode,
		Rules:     rules,
		ReadOnly:  builtinReadOnlySet(),
		EditTools: editTools,
		Builtin:   builtinTools,
	}, warns
}

// extensionPermissionRules collects suggested rules from installed
// extension bundles (the `permissions` key in extension.json — see
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
	if home := TervaHome(); home != "" {
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
				Name        string                 `json:"name"`
				Enabled     *bool                  `json:"enabled"`
				Permissions []PermissionRuleConfig `json:"permissions"`
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

// builtinReadOnlySet seeds the policy's dynamic read-only registry
// with the built-in classification. Extension/MCP tools that declare
// read_only join later via Resolved.MergeExtensionTools.
func builtinReadOnlySet() *core.ReadOnlySet {
	names := make([]string, 0, len(readOnlyTools))
	for n := range readOnlyTools {
		names = append(names, n)
	}
	return core.NewReadOnlySet(names...)
}

// AppendUserPermissionRule persists a durable allow grant for a tool
// — the confirm dialog's "always, save to config" answer. Appended at
// the end of the user list so existing deny/ask rules keep beating it
// (rules are first-match-wins and project rules run earlier still).
func AppendUserPermissionRule(toolName string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	for _, r := range cfg.Permissions {
		if r.Tool == toolName && r.Args == "" && strings.EqualFold(r.Decision, string(core.RuleAllow)) {
			return nil // already granted
		}
	}
	cfg.Permissions = append(cfg.Permissions, PermissionRuleConfig{
		Tool:     toolName,
		Decision: string(core.RuleAllow),
	})
	return SaveConfig(cfg)
}
