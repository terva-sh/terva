package agent

import (
	"testing"

	"terva.sh/terva/packages/agent/mcp"
)

// TestApplyMCPAllowlist: the --mcp allowlist prunes the merged config
// before anything spawns; an empty allowlist changes nothing; and the
// adapter gate applies the same rule to the /mcp dialog's live enables,
// so a scoped run stays scoped.
func TestApplyMCPAllowlist(t *testing.T) {
	cfg := &mcp.Config{Servers: map[string]mcp.ServerConfig{
		"git": {}, "jira": {}, "mail": {},
	}}
	applyMCPAllowlist(cfg, nil)
	if len(cfg.Servers) != 3 {
		t.Fatalf("empty allowlist must not prune; got %d servers", len(cfg.Servers))
	}
	applyMCPAllowlist(cfg, []string{"git", "jira"})
	if len(cfg.Servers) != 2 {
		t.Fatalf("allowlist prune wrong: %v", cfg.Servers)
	}
	if _, ok := cfg.Servers["mail"]; ok {
		t.Error("mail survived the allowlist")
	}

	ad := &mcpToolAdapter{allowed: mcpAllowSet([]string{"git"})}
	if !ad.allowsThisRun("git") || ad.allowsThisRun("mail") {
		t.Error("adapter gate disagrees with the allowlist")
	}
	if !(&mcpToolAdapter{}).allowsThisRun("anything") {
		t.Error("no allowlist must admit everything")
	}
}
