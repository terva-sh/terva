package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// writeExtension lays down a minimal installed extension bundle under
// home/extensions/<name>.
func writeExtension(t *testing.T, home, name string, manifest map[string]any, skillBodies map[string]string) string {
	t.Helper()
	dir := filepath.Join(home, "extensions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if manifest["name"] == nil {
		manifest["name"] = name
	}
	if manifest["exec"] == nil {
		manifest["exec"] = "./noop"
	}
	mb, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), mb, 0o644); err != nil {
		t.Fatal(err)
	}
	for skillName, body := range skillBodies {
		sd := filepath.Join(dir, "skills", skillName)
		if err := os.MkdirAll(sd, 0o755); err != nil {
			t.Fatal(err)
		}
		md := "---\nname: " + skillName + "\ndescription: from bundle\n---\n" + body
		if err := os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte(md), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestBundleSkillsDiscovered(t *testing.T) {
	home := withTempHome(t)
	writeExtension(t, home, "researcher", map[string]any{}, map[string]string{
		"web-research": "Chain search into fetch.",
	})
	found, errs := skills.Discover(home, testsupport.TempDir(t), "", true, true, true)
	if len(errs) > 0 {
		t.Fatalf("discover errors: %v", errs)
	}
	for _, s := range found {
		if s.Name == "web-research" {
			if !strings.Contains(s.Source, "extension") {
				t.Errorf("bundle skill source = %q, want extension label", s.Source)
			}
			return
		}
	}
	t.Fatal("bundle skill not discovered")
}

func TestBundleSkillsSkipDisabledExtension(t *testing.T) {
	home := withTempHome(t)
	writeExtension(t, home, "off", map[string]any{"enabled": false}, map[string]string{
		"ghost-skill": "should not load",
	})
	found, _ := skills.Discover(home, testsupport.TempDir(t), "", true, true, true)
	for _, s := range found {
		if s.Name == "ghost-skill" {
			t.Fatal("disabled extension's bundle skill was discovered")
		}
	}
}

func TestBundleSkillsNeverShadowUserSkills(t *testing.T) {
	home := withTempHome(t)
	// User-authored skill with the same name as a bundle skill.
	ud := filepath.Join(home, "skills", "web-research")
	if err := os.MkdirAll(ud, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ud, "SKILL.md"),
		[]byte("---\nname: web-research\ndescription: user's own\n---\nuser body"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExtension(t, home, "researcher", map[string]any{}, map[string]string{
		"web-research": "bundle body",
	})
	found, _ := skills.Discover(home, testsupport.TempDir(t), "", true, true, true)
	for _, s := range found {
		if s.Name == "web-research" {
			if strings.Contains(s.Source, "extension") {
				t.Fatal("bundle skill shadowed the user's skill")
			}
			return
		}
	}
	t.Fatal("skill missing entirely")
}

func TestBundlePermissionRulesRestrictOnly(t *testing.T) {
	home := withTempHome(t)
	writeExtension(t, home, "web", map[string]any{
		"permissions": []map[string]any{
			{"tool": "web_fetch_raw", "decision": "ask"},
			{"tool": "bash", "decision": "allow"}, // must be dropped
			{"tool": "web_fetch", "args": "169\\.254\\.", "decision": "deny", "reason": "metadata endpoint"},
		},
	}, nil)

	pol, warns := permissions.BuildPolicy(build.Args{CWD: testsupport.TempDir(t)}.PermInputs())
	if pol == nil {
		t.Fatal("bundle rules exist; policy must be non-nil")
	}
	var fromExt []core.PermissionRule
	for _, r := range pol.Rules {
		if strings.HasPrefix(r.Source, "extension") {
			fromExt = append(fromExt, r)
		}
	}
	if len(fromExt) != 2 {
		t.Fatalf("want 2 surviving extension rules (allow dropped), got %+v", fromExt)
	}
	for _, r := range fromExt {
		if r.Decision == core.RuleAllow {
			t.Fatal("extension bundle granted itself an allow rule")
		}
	}
	joined := strings.Join(warns, "; ")
	if !strings.Contains(joined, "may not allow") {
		t.Errorf("want self-approval-ban warning, got %v", warns)
	}
	// And the deny has teeth.
	args, _ := json.Marshal(map[string]string{"url": "http://169.254.169.254/iam"})
	if v, reason := pol.Evaluate("web_fetch", args); v != core.VerdictDeny || !strings.Contains(reason, "metadata") {
		t.Errorf("bundle deny rule should fire: %v %q", v, reason)
	}
}

func TestBundleRulesOrderedBetweenProjectAndUser(t *testing.T) {
	home := withTempHome(t)
	cfgJSON := `{"permissions":[{"tool":"x","decision":"allow"}]}`
	if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfgJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	writeExtension(t, home, "e", map[string]any{
		"permissions": []map[string]any{{"tool": "x", "decision": "ask"}},
	}, nil)
	proj := testsupport.TempDir(t)
	if err := os.MkdirAll(filepath.Join(proj, ".terva"), 0o755); err != nil {
		t.Fatal(err)
	}
	projJSON := `{"permissions":[{"tool":"x","decision":"deny"}]}`
	if err := os.WriteFile(filepath.Join(proj, ".terva", "config.json"), []byte(projJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	pol, _ := permissions.BuildPolicy(build.Args{CWD: proj}.PermInputs())
	if pol == nil || len(pol.Rules) != 3 {
		t.Fatalf("rules = %+v", pol)
	}
	want := []string{"user", "project", "extension e"}
	for i, w := range want {
		if pol.Rules[i].Source != w {
			t.Errorf("rule %d source = %q, want %q", i, pol.Rules[i].Source, w)
		}
	}
	// User is sovereign: the user's allow wins over the project deny
	// and the extension ask on the same tool.
	if v, _ := pol.Evaluate("x", nil); v != core.VerdictAllow {
		t.Errorf("user allow should win the three-layer conflict, got %v", v)
	}
}
