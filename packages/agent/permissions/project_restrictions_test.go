package permissions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/mode"
	"terva.sh/terva/packages/testsupport"
)

// Each project setting that can only restrict is lost when its file cannot be
// parsed, and each loss is reported (TKT-01M372ESJB). Before, only the deny and
// ask rules were: a broken file that disabled an extension or an MCP server let
// it start with no word to the user. The hosts warn and carry on, so a typo in
// a repository's file cannot take away the user's own tools; the warning names
// the file and every restriction that is not in force.
func TestEveryProjectRestrictionIsReportedWhenItsFileCannotBeParsed(t *testing.T) {
	bodies := map[string]string{
		"permissions":                `{"permissions": [{"tool": "bash", "decision": "deny"},]}`,
		"disable_extensions":         `{"disable_extensions": ["ext-a",]}`,
		"disable_mcp":                `{"disable_mcp": ["srv-a",]}`,
		"disable_context_extensions": `{"disable_context_extensions": ["ctx-a",]}`,
		"tickets":                    `{"tickets": false,}`,
		"project_scoped":             `{"project_scoped": true,}`,
	}
	if len(bodies) != len(config.ProjectRestrictions) {
		t.Fatalf("the test covers %d restrictions and config.ProjectRestrictions lists %d; cover the new one", len(bodies), len(config.ProjectRestrictions))
	}
	for _, field := range config.ProjectRestrictions {
		t.Run(field, func(t *testing.T) {
			body, ok := bodies[field]
			if !ok {
				t.Fatalf("no malformed config for %s", field)
			}
			t.Setenv("TERVA_HOME", testsupport.TempDir(t))
			cwd := testsupport.TempDir(t)
			path := filepath.Join(cwd, ".terva", "config.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}

			_, warns := BuildPolicy(Inputs{Mode: mode.JSON, CWD: cwd})
			var found string
			for _, w := range warns {
				if strings.Contains(w, "project config unreadable") {
					found = w
				}
			}
			if found == "" {
				t.Fatalf("a broken file holding %s gave no warning: %q", field, warns)
			}
			if !strings.Contains(found, path) || !strings.Contains(found, field) || !strings.Contains(found, "NOT applied") {
				t.Errorf("the warning must name %s, the file, and that it is not applied: %q", field, found)
			}
			if _, _, err := LoadPolicy(Inputs{Mode: mode.JSON, CWD: cwd}); err == nil || !strings.Contains(err.Error(), field) || !strings.Contains(err.Error(), path) {
				t.Errorf("LoadPolicy err = %v, want a refusal naming %s and %s", err, field, path)
			}
		})
	}
}

// The failure the warning reports is real: the restriction is not in force.
// Pinned so that a later change to fail closed shows up here, not as a
// warning that has stopped being true.
func TestABrokenProjectConfigDisablesNothing(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	cwd := testsupport.TempDir(t)
	path := filepath.Join(cwd, ".terva", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"disable_extensions": ["ext-a"], "disable_mcp": ["srv-a"],}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, n := range config.UnionDisabledExtensions(cwd) {
		if n == "ext-a" {
			t.Fatal("the broken file's disable list applied; the warning says it does not, so update both")
		}
	}
	if config.ResolvedDisableMCP(cwd, true)["srv-a"] {
		t.Fatal("the broken file's MCP disable list applied; the warning says it does not, so update both")
	}
}
