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

// The tool is the one path both the model and /skill's primed directive take,
// so it has to resolve a qualified name — otherwise a shadowed skill is
// listed in the picker and completions but can never actually be loaded.
func TestToolLoadsQualifiedAndNotesAlternatives(t *testing.T) {
	loser := &Skill{Name: "handoff", Namespace: NamespaceClaude, Description: "theirs", Body: "CLAUDE BODY"}
	winner := &Skill{Name: "handoff", Namespace: NamespaceBuiltin, Builtin: true, Description: "ours", Body: "BUILTIN BODY", Shadowed: []*Skill{loser}}
	loser.ShadowedBy = winner
	tool := NewTool([]*Skill{winner})

	bare, _ := tool.Execute(context.Background(), []byte(`{"name":"handoff"}`), nil)
	if bare.IsError {
		t.Fatalf("bare name should load the winner: %v", bare.Content)
	}
	bareTxt := bare.Content[0].(provider.TextBlock).Text
	if !strings.Contains(bareTxt, "BUILTIN BODY") {
		t.Errorf("bare name loaded the wrong body: %q", bareTxt)
	}
	// The alternatives note is what teaches the qualified syntax, at the one
	// moment it is relevant, instead of costing manifest tokens every turn.
	if !strings.Contains(bareTxt, "claude:handoff") {
		t.Errorf("collision note should name the shadowed alternative, got:\n%s", bareTxt)
	}
	details, ok := bare.Details.(map[string]any)
	if !ok {
		t.Fatalf("details = %T, want map[string]any", bare.Details)
	}
	if got := details["qualified"]; got != "builtin:handoff" {
		t.Errorf("details qualified = %v, want builtin:handoff", got)
	}

	qual, _ := tool.Execute(context.Background(), []byte(`{"name":"claude:handoff"}`), nil)
	if qual.IsError {
		t.Fatalf("qualified name should load the shadowed skill: %v", qual.Content)
	}
	qualTxt := qual.Content[0].(provider.TextBlock).Text
	if !strings.Contains(qualTxt, "CLAUDE BODY") {
		t.Errorf("qualified name loaded the wrong body: %q", qualTxt)
	}
	// Already disambiguated — repeating the alternatives here would be noise.
	if strings.Contains(qualTxt, "also names") {
		t.Errorf("an explicitly qualified load should carry no collision note, got:\n%s", qualTxt)
	}
}

// An uncontested name must stay clean: no collision note, no qualified-syntax
// nudge. Otherwise every skill load pays for a rare case.
func TestToolNoNoteWithoutCollision(t *testing.T) {
	tool := NewTool([]*Skill{{Name: "solo", Namespace: NamespaceTerva, Description: "d", Body: "BODY"}})
	res, _ := tool.Execute(context.Background(), []byte(`{"name":"solo"}`), nil)
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	if txt := res.Content[0].(provider.TextBlock).Text; strings.Contains(txt, "also names") {
		t.Errorf("uncontested load should have no collision note, got:\n%s", txt)
	}
}
