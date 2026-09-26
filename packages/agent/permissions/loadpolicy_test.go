package permissions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/testsupport"
)

// The two entry points agree on everything except an unreadable user config:
// BuildPolicy warns and carries on, which every CLI host prints, and LoadPolicy
// returns the error, for a host with nobody to print to.
func TestLoadPolicyFailsWhereBuildPolicyWarns(t *testing.T) {
	home := testsupport.TempDir(t)
	t.Setenv("TERVA_HOME", home)
	cfgPath := filepath.Join(home, "config.json")
	in := Inputs{Mode: mode.JSON, CWD: testsupport.TempDir(t)}

	if err := os.WriteFile(cfgPath, []byte(`{"permissions": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, warns := BuildPolicy(in)
	if pol != nil {
		t.Errorf("BuildPolicy built a policy from a config it could not read: %+v", pol)
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "user config unreadable, rules ignored") {
		t.Errorf("BuildPolicy's warning changed; the CLI hosts print it: %q", warns)
	}
	if _, _, err := LoadPolicy(in); err == nil || !strings.Contains(err.Error(), "cannot be applied") {
		t.Errorf("LoadPolicy err = %v, want the unreadable config reported", err)
	}

	// A readable config: the two return the same policy.
	if err := os.WriteFile(cfgPath, []byte(`{"permissions": [{"tool": "bash", "decision": "deny"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	built, _ := BuildPolicy(in)
	loaded, _, err := LoadPolicy(in)
	if err != nil {
		t.Fatalf("LoadPolicy on a readable config: %v", err)
	}
	if built == nil || loaded == nil {
		t.Fatalf("a deny rule produced no policy: built=%v loaded=%v", built, loaded)
	}
	if built.Mode != loaded.Mode || len(built.Rules) != len(loaded.Rules) {
		t.Errorf("BuildPolicy and LoadPolicy disagree: mode %s/%s, rules %d/%d",
			built.Mode, loaded.Mode, len(built.Rules), len(loaded.Rules))
	}
}

// A project config that exists but cannot be parsed used to drop its rules
// with no word at all: policyFromConfig discarded LoadProjectConfig's error.
// Project rules can only tighten, so that failed open. Now the rule is either
// applied or reported: BuildPolicy warns and names the file, and LoadPolicy
// refuses. A missing project config stays silent, because it is the common
// case and loses nothing.
func TestAProjectConfigThatCannotBeParsedIsNeverDroppedSilently(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	cwd := testsupport.TempDir(t)
	in := Inputs{Mode: mode.JSON, CWD: cwd}

	// No project config: no warning, no error.
	if _, warns := BuildPolicy(in); len(warns) != 0 {
		t.Fatalf("a missing project config warned: %q", warns)
	}
	if _, _, err := LoadPolicy(in); err != nil {
		t.Fatalf("a missing project config failed LoadPolicy: %v", err)
	}

	dir := filepath.Join(cwd, ".terva")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")

	// A readable project deny rule applies.
	if err := os.WriteFile(path, []byte(`{"permissions": [{"tool": "bash", "decision": "deny"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, warns := BuildPolicy(in)
	if len(warns) != 0 {
		t.Fatalf("a readable project config warned: %q", warns)
	}
	if pol == nil || len(pol.Rules) != 1 {
		t.Fatalf("the project deny rule did not reach the policy: %+v", pol)
	}

	// The same rule behind a trailing comma: reported, never silently gone.
	if err := os.WriteFile(path, []byte(`{"permissions": [{"tool": "bash", "decision": "deny"},]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, warns = BuildPolicy(in)
	if pol != nil && len(pol.Rules) != 0 {
		t.Errorf("BuildPolicy kept rules from a config it could not parse: %+v", pol.Rules)
	}
	var found string
	for _, w := range warns {
		if strings.Contains(w, "project config unreadable") {
			found = w
		}
	}
	if found == "" {
		t.Fatalf("BuildPolicy dropped the project's rules without a warning: %q", warns)
	}
	if !strings.Contains(found, path) || !strings.Contains(found, "NOT applied") {
		t.Errorf("the warning must name the file and say the rules are not applied: %q", found)
	}
	if _, _, err := LoadPolicy(in); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("LoadPolicy err = %v, want a refusal naming %s", err, path)
	}
}
