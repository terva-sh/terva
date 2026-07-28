package build

import (
	"testing"

	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// Plan mode's registry pruning is build's, not package permissions': the
// classification is theirs, the registry is ours. This test moved back when the
// permission logic left, because it exercises BuildToolRegistry.

func TestPlanModeFiltersToolRegistry(t *testing.T) {
	reg := BuildToolRegistry(Args{}, core.ApprovalPlan, testsupport.TempDir(t), nil, "anthropic", "apikey", true, nil)
	for name := range reg {
		// Plan keeps read-only tools plus interactive tools
		// (ask_user_question) — asking the user is exactly what plan
		// mode wants when requirements are unclear.
		if !permissions.IsReadOnly(name) && !permissions.IsInteractive(name) {
			t.Errorf("plan registry leaked mutating tool %s", name)
		}
	}
	if _, ok := reg["read"]; !ok {
		t.Error("plan registry should keep read")
	}
	if _, ok := reg["ask_user_question"]; !ok {
		t.Error("plan registry should keep the interactive ask_user_question tool")
	}
	if _, ok := reg["bash"]; ok {
		t.Error("plan registry must not contain bash")
	}
}
