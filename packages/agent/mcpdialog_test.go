package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/mcp"
)

func writeUserConfig(t *testing.T, home string, c Config) {
	t.Helper()
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// setUserMCPDisabled toggles the user disable_mcp list and preserves the
// rest of the user config (the structured round-trip).
func TestSetUserMCPDisabledRoundTrip(t *testing.T) {
	home := withTempHome(t)
	writeUserConfig(t, home, Config{
		FavoriteModels: []string{"anthropic/opus"},
		MCP:            &mcp.Config{Servers: map[string]mcp.ServerConfig{"s": {Command: "x"}}},
	})

	if err := setUserMCPDisabled("s", true); err != nil {
		t.Fatal(err)
	}
	c, _ := LoadConfig()
	if len(c.DisableMCP) != 1 || c.DisableMCP[0] != "s" {
		t.Fatalf("disable_mcp = %v, want [s]", c.DisableMCP)
	}
	// Untouched fields survive the round-trip.
	if len(c.FavoriteModels) != 1 || c.MCP == nil || c.MCP.Servers["s"].Command != "x" {
		t.Fatalf("round-trip dropped fields: favorites=%v mcp=%+v", c.FavoriteModels, c.MCP)
	}

	// Re-enable removes it again.
	if err := setUserMCPDisabled("s", false); err != nil {
		t.Fatal(err)
	}
	if c, _ := LoadConfig(); len(c.DisableMCP) != 0 {
		t.Fatalf("disable_mcp after re-enable = %v, want empty", c.DisableMCP)
	}
}

// setProjectMCPDisabled writes the project's disable_mcp list while
// preserving unrelated project fields, and removes it cleanly.
func TestSetProjectMCPDisabledRoundTrip(t *testing.T) {
	withTempHome(t)
	proj := t.TempDir()
	writeProjectConfig(t, proj, `{"context_files":["AGENTS.md"]}`)

	if err := setProjectMCPDisabled(proj, "repo", true); err != nil {
		t.Fatal(err)
	}
	pc, err := LoadProjectConfig(proj)
	if err != nil || pc == nil {
		t.Fatalf("load project config: %v", err)
	}
	if len(pc.DisableMCP) != 1 || pc.DisableMCP[0] != "repo" {
		t.Fatalf("project disable_mcp = %v, want [repo]", pc.DisableMCP)
	}
	if len(pc.ContextFiles) != 1 {
		t.Fatalf("unrelated project field dropped: context_files=%v", pc.ContextFiles)
	}

	if err := setProjectMCPDisabled(proj, "repo", false); err != nil {
		t.Fatal(err)
	}
	pc, _ = LoadProjectConfig(proj)
	if pc != nil && len(pc.DisableMCP) != 0 {
		t.Fatalf("project disable_mcp after re-enable = %v, want empty", pc.DisableMCP)
	}
}

// resolvedDisableMCP unions user and project lists, and honors the project
// list even when the workspace is untrusted (restrict-only, always safe).
func TestResolvedDisableMCPUnionHonoredUntrusted(t *testing.T) {
	home := withTempHome(t)
	writeUserConfig(t, home, Config{DisableMCP: []string{"a"}})
	proj := t.TempDir()
	writeProjectConfig(t, proj, `{"disable_mcp":["b"]}`)

	for _, trusted := range []bool{true, false} {
		set := resolvedDisableMCP(proj, trusted)
		if !set["a"] || !set["b"] {
			t.Fatalf("trusted=%v: disable set = %v, want a+b", trusted, set)
		}
	}
}

// listMCPServers builds the config-driven row set with scope, disable, and
// trust-gate state — independent of any live Manager (nil here).
func TestListMCPServersScopesAndState(t *testing.T) {
	home := withTempHome(t)
	writeUserConfig(t, home, Config{
		MCP: &mcp.Config{Servers: map[string]mcp.ServerConfig{"u": {Command: "ucmd"}}},
	})
	proj := t.TempDir()
	// Project defines "p" and disables the user's "u" here.
	writeProjectConfig(t, proj, `{"mcp":{"servers":{"p":{"command":"pcmd"}}},"disable_mcp":["u"]}`)

	// Trusted: project server "p" is effective; user "u" is project-disabled.
	rows := listMCPServers(proj, true, nil)
	got := map[string]string{}
	eff := map[string]bool{}
	for _, r := range rows {
		got[r.Name] = r.Scope
		eff[r.Name] = r.Effective
	}
	if got["u"] != "global" || got["p"] != "project" {
		t.Fatalf("scopes = %v, want u:global p:project", got)
	}
	if eff["u"] {
		t.Errorf("user server u is project-disabled, should not be effective")
	}
	if !eff["p"] {
		t.Errorf("trusted project server p should be effective")
	}

	// Untrusted: project server "p" is gated off.
	rows = listMCPServers(proj, false, nil)
	for _, r := range rows {
		if r.Name == "p" {
			if !r.ProjectGated || r.Effective {
				t.Fatalf("untrusted project server p = %+v, want gated/!effective", r)
			}
		}
	}
}

// mcpServerShouldRun mirrors the row's Effective: defined and not disabled.
func TestMCPServerShouldRun(t *testing.T) {
	home := withTempHome(t)
	writeUserConfig(t, home, Config{
		MCP:        &mcp.Config{Servers: map[string]mcp.ServerConfig{"u": {Command: "x"}, "d": {Command: "y"}}},
		DisableMCP: []string{"d"},
	})
	proj := t.TempDir()
	if !mcpServerShouldRun(proj, true, "u") {
		t.Error("u is defined and enabled — should run")
	}
	if mcpServerShouldRun(proj, true, "d") {
		t.Error("d is disabled — should not run")
	}
	if mcpServerShouldRun(proj, true, "missing") {
		t.Error("undefined server should not run")
	}
}
