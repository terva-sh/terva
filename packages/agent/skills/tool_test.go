package skills

import (
	"context"
	"strings"
	"testing"

	"terva.sh/terva/packages/provider"
)

// The cache-safe reload contract: SetSkills swaps the tool's catalog so a
// SKILL.md added mid-session becomes loadable by name — without creating a
// new tool or touching the system prompt. This is what /skills (open + `r`)
// drives so a freshly written skill is usable without restarting terva.
func TestToolReloadMakesNewSkillLoadable(t *testing.T) {
	tool := NewTool([]*Skill{{Name: "first", Description: "d", Body: "one"}})

	// Not in the catalog yet → not loadable.
	res, _ := tool.Execute(context.Background(), []byte(`{"name":"second"}`), nil)
	if !res.IsError {
		t.Fatal("a skill not yet in the catalog should not load")
	}

	// Reload swaps in a catalog that includes it.
	tool.SetSkills([]*Skill{
		{Name: "first", Description: "d", Body: "one"},
		{Name: "second", Description: "d2", Body: "two"},
	})

	res, _ = tool.Execute(context.Background(), []byte(`{"name":"second"}`), nil)
	if res.IsError {
		t.Fatalf("after reload the new skill should load, got error: %v", res.Content)
	}
	if txt := res.Content[0].(provider.TextBlock).Text; !strings.Contains(txt, "two") {
		t.Errorf("loaded body = %q, want it to contain the new skill's body", txt)
	}
}
