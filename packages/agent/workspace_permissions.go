package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// The permissions inspector surface: the session's approval mode + compiled
// rules and the live "always-allow" grants. Backed by the per-session
// ConfirmGate. Grant revocation is session-scoped; rule add/remove edits USER
// config and applies live to every session (gate.SetRules), since user rules are
// process-global. Only user rules are editable here — project rules can't hold
// allow (the self-approval ban) and extension/builtin rules aren't config.

// permissionsView builds the permissions pane from the session's gate.
func (s *wsSession) permissionsView() *ctrlproto.PermissionsView {
	v := &ctrlproto.PermissionsView{Mode: string(core.ApprovalYolo)}
	if s.gate == nil {
		return v // pure-yolo: no gate, nothing to inspect
	}
	v.Mode = string(s.gate.Mode())
	for _, r := range s.gate.Rules() {
		args := ""
		if r.Args != nil {
			args = r.Args.String()
		}
		v.Rules = append(v.Rules, ctrlproto.PermissionRule{
			Tool: r.Tool, Args: args, Decision: string(r.Decision), Reason: r.Reason,
			Source: r.Source, Removable: r.Source == "user" || r.Source == "project",
		})
	}
	v.AllowAll, v.Grants = s.gate.Grants()
	return v
}

// permissionsAction handles grant revocation (session-scoped) and user-rule
// add/remove (persisted to user config + applied live to every session).
func (s *wsSession) permissionsAction(action string, args map[string]string) error {
	if s.gate == nil {
		return ctrlproto.Errorf(ctrlproto.CodeUnsupported, "no permission gate for this session")
	}
	switch action {
	case "revoke":
		tool := args["tool"]
		if tool == "" {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "revoke: missing tool")
		}
		s.gate.Revoke(tool)
		s.broadcast(ctrlproto.SurfaceUpdatedEvent("permissions"))
	case "revoke_all":
		s.gate.ClearAllowAll()
		s.broadcast(ctrlproto.SurfaceUpdatedEvent("permissions"))
	case "add_rule":
		tool := strings.TrimSpace(args["tool"])
		decision := strings.ToLower(strings.TrimSpace(args["decision"]))
		if tool == "" {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "add_rule: missing tool")
		}
		if !validRuleDecision(decision) {
			return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "add_rule: decision must be allow, deny, or ask")
		}
		rule := PermissionRuleConfig{Tool: tool, Args: strings.TrimSpace(args["args"]), Decision: decision, Reason: strings.TrimSpace(args["reason"])}
		var err error
		if args["scope"] == "project" {
			// Project rules are restrict-only — the self-approval ban forbids allow.
			if decision == string(core.RuleAllow) {
				return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "add_rule: project rules can't grant allow")
			}
			err = setProjectPermissionRule(s.cwd, rule, true)
		} else {
			err = addUserPermissionRule(rule)
		}
		if err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "add_rule: %v", err)
		}
		s.ws.refreshAllPolicies()
		s.ws.broadcastAll(ctrlproto.SurfaceUpdatedEvent("permissions"))
	case "remove_rule":
		rule := PermissionRuleConfig{Tool: args["tool"], Args: args["args"], Decision: strings.ToLower(args["decision"])}
		var err error
		if args["scope"] == "project" {
			err = setProjectPermissionRule(s.cwd, rule, false)
		} else {
			err = removeUserPermissionRule(rule)
		}
		if err != nil {
			return ctrlproto.Errorf(ctrlproto.CodeInternal, "remove_rule: %v", err)
		}
		s.ws.refreshAllPolicies()
		s.ws.broadcastAll(ctrlproto.SurfaceUpdatedEvent("permissions"))
	default:
		return ctrlproto.Errorf(ctrlproto.CodeBadRequest, "unknown permissions action %q", action)
	}
	return nil
}

func validRuleDecision(d string) bool {
	switch d {
	case string(core.RuleAllow), string(core.RuleDeny), string(core.RuleAsk):
		return true
	}
	return false
}

// addUserPermissionRule appends a rule to the user config (idempotent on an
// exact duplicate). Richer than AppendUserPermissionRule (which is allow-only).
func addUserPermissionRule(rule PermissionRuleConfig) error {
	return MutateConfig(func(c *Config) {
		if slices.Contains(c.Permissions, rule) {
			return
		}
		c.Permissions = append(c.Permissions, rule)
	})
}

// removeUserPermissionRule deletes the first user rule matching tool+args+
// decision (the fields the pane sends back for a listed rule).
func removeUserPermissionRule(match PermissionRuleConfig) error {
	return MutateConfig(func(c *Config) {
		for i, r := range c.Permissions {
			if r.Tool == match.Tool && r.Args == match.Args && strings.EqualFold(r.Decision, match.Decision) {
				c.Permissions = append(c.Permissions[:i], c.Permissions[i+1:]...)
				return
			}
		}
	})
}

// setProjectPermissionRule adds or removes a rule in the project config's
// permissions array (.terva/config.json), a generic-map round-trip like
// setProjectExtensionDisabled. add=false just drops the matching rule. Project
// rules are restrict-only and apply even in an untrusted workspace.
func setProjectPermissionRule(cwd string, rule PermissionRuleConfig, add bool) error {
	path := projectConfigPath(cwd)
	generic := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &generic); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	var list []any
	if arr, ok := generic["permissions"].([]any); ok {
		for _, e := range arr {
			if m, ok := e.(map[string]any); ok && projectRuleMatches(m, rule) {
				continue // drop the match (dedup on add, delete on remove)
			}
			list = append(list, e)
		}
	}
	if add {
		obj := map[string]any{"tool": rule.Tool, "decision": rule.Decision}
		if rule.Args != "" {
			obj["args"] = rule.Args
		}
		if rule.Reason != "" {
			obj["reason"] = rule.Reason
		}
		list = append(list, obj)
	}
	if len(list) == 0 {
		delete(generic, "permissions")
	} else {
		generic["permissions"] = list
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func projectRuleMatches(m map[string]any, r PermissionRuleConfig) bool {
	tool, _ := m["tool"].(string)
	args, _ := m["args"].(string)
	dec, _ := m["decision"].(string)
	return tool == r.Tool && args == r.Args && strings.EqualFold(dec, r.Decision)
}

// refreshAllPolicies rebuilds every live session's rule set from config and
// swaps it onto the gate (SetRules preserves each session's live Mode). Used
// after a user-rule edit, since user rules are process-global.
func (w *Workspace) refreshAllPolicies() {
	w.mu.Lock()
	sess := make([]*wsSession, 0, len(w.sessions))
	for _, s := range w.sessions {
		sess = append(sess, s)
	}
	w.mu.Unlock()
	for _, s := range sess {
		if s.gate == nil {
			continue
		}
		// A nil policy (yolo + no rules) means "no rules" — clear them, or a
		// just-removed rule would linger on the gate.
		var rules []core.PermissionRule
		if pol, _ := buildPermissionPolicy(s.argsSnapshot()); pol != nil {
			rules = pol.Rules
		}
		s.gate.SetRules(rules)
	}
}
