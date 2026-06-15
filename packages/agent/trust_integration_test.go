package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
)

// writeProjectExtManifest drops a project-local extension manifest under
// cwd/.terva/extensions/<name> carrying the given permissions block.
func writeProjectExtManifest(t *testing.T, cwd, name string, perms []map[string]any) {
	t.Helper()
	dir := filepath.Join(cwd, ".terva", "extensions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mf := map[string]any{"name": name, "exec": "./noop", "permissions": perms}
	mb, _ := json.Marshal(mf)
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), mb, 0o644); err != nil {
		t.Fatal(err)
	}
}

// A PROJECT extension's suggested permission rules are gated on trust:
// in an untrusted workspace they don't apply (the project ext doesn't
// load); trusting the dir applies them.
func TestExtensionPermissionRulesGatedOnTrust(t *testing.T) {
	withTempHome(t)
	proj := t.TempDir()
	writeProjectExtManifest(t, proj, "repo-ext", []map[string]any{
		{"tool": "web_fetch_raw", "decision": "deny", "reason": "repo policy"},
	})

	// Untrusted: the project-ext-suggested deny must NOT be present.
	untrusted, _ := extensionPermissionRules(proj, false)
	for _, r := range untrusted {
		if r.Tool == "web_fetch_raw" {
			t.Errorf("untrusted workspace applied a project-extension suggested rule: %+v", r)
		}
	}

	// Trusted: the rule applies.
	trusted, _ := extensionPermissionRules(proj, true)
	found := false
	for _, r := range trusted {
		if r.Tool == "web_fetch_raw" && r.Decision == core.RuleDeny {
			found = true
		}
	}
	if !found {
		t.Errorf("trusted workspace should apply the project-extension suggested deny rule: %v", trusted)
	}
}

// buildPermissionPolicy resolves trust from args: a project extension's
// suggested rule is applied only with --trust, not by default.
func TestBuildPermissionPolicyGatesProjectExtRules(t *testing.T) {
	withTempHome(t)
	proj := t.TempDir()
	writeProjectExtManifest(t, proj, "repo-ext", []map[string]any{
		{"tool": "web_fetch_raw", "decision": "ask"},
	})

	// Untrusted (default): no gate built (pure yolo + no applicable rules).
	pol, _ := buildPermissionPolicy(Args{CWD: proj})
	if pol != nil {
		// If a policy exists it must NOT carry the project-ext rule.
		for _, r := range pol.Rules {
			if r.Tool == "web_fetch_raw" {
				t.Errorf("untrusted policy carried a project-ext suggested rule: %+v", r)
			}
		}
	}

	// Trusted: the suggested ask rule applies.
	polT, _ := buildPermissionPolicy(Args{CWD: proj, Trust: true})
	if polT == nil {
		t.Fatal("trusted workspace with a project-ext suggested rule should build a gate")
	}
	found := false
	for _, r := range polT.Rules {
		if r.Tool == "web_fetch_raw" {
			found = true
		}
	}
	if !found {
		t.Errorf("trusted policy should carry the project-ext suggested rule: %v", polT.Rules)
	}
}

// Decision #4: trust does NOT supersede the self-approval `allow` ban.
// A project that grants itself `allow` is rejected EVEN WITH --trust:
// trust unlocks loading project code, never self-approval.
func TestTrustDoesNotUnlockProjectAllow(t *testing.T) {
	home := withTempHome(t)
	_ = home
	proj := t.TempDir()
	if err := os.MkdirAll(filepath.Join(proj, ".terva"), 0o755); err != nil {
		t.Fatal(err)
	}
	projJSON := `{"permissions":[{"tool":"bash","decision":"allow"}]}`
	if err := os.WriteFile(filepath.Join(proj, ".terva", "config.json"), []byte(projJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// Even trusted, the project `allow` rule must be dropped with the
	// self-approval-ban warning. (With the lone allow rule dropped and no
	// other rules under the default yolo mode, the policy is nil — the
	// no-gate fast path — which itself proves the allow never took.)
	pol, warns := buildPermissionPolicy(Args{CWD: proj, Trust: true})
	if pol != nil {
		for _, r := range pol.Rules {
			if r.Source == "project" && r.Decision == core.RuleAllow {
				t.Fatal("trusted project produced a self-allow rule — the self-approval ban must hold even when trusted")
			}
		}
	}
	sawBan := false
	for _, w := range warns {
		if strings.Contains(w, "may not allow") {
			sawBan = true
		}
	}
	if !sawBan {
		t.Errorf("expected the self-approval-ban warning even for a trusted project, got %v", warns)
	}
}

// Trust via the STORE (persisted) loads project skills, just like
// --trust does — exercised end-to-end through Resolve.
func TestResolveTrustViaStoreLoadsProjectSkills(t *testing.T) {
	t.Setenv("TERVA_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := SaveConfig(Config{Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	proj := t.TempDir()
	skillDir := filepath.Join(proj, ".terva", "skills", "repo-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: repo-skill\ndescription: REPO-SKILL-MARKER\n---\nbody"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// Untrusted (default): the project skill is NOT in the manifest.
	// WithSkills mirrors ParseArgs's default (user skills load).
	rOff, err := Resolve(Args{CWD: proj, WithSkills: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rOff.SystemPrompt, "REPO-SKILL-MARKER") {
		t.Fatal("untrusted workspace leaked a project skill into the system prompt")
	}

	// Persist trust in the store, then re-resolve: the project skill loads.
	if err := TrustPath(proj, false); err != nil {
		t.Fatal(err)
	}
	rOn, err := Resolve(Args{CWD: proj, WithSkills: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !rOn.Trusted {
		t.Fatal("store entry should make Resolve report the workspace trusted")
	}
	if !strings.Contains(rOn.SystemPrompt, "REPO-SKILL-MARKER") {
		t.Fatalf("store-trusted workspace should load its project skill:\n%s", rOn.SystemPrompt)
	}
}
