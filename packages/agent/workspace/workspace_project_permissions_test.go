package workspace

import (
	"encoding/json"
	"os"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// setProjectPermissionRule was the fourth copy of the project-config
// read-modify-write, and the only one in a package the other three could not
// see. That is the interesting half of the finding: three tests each asserted
// "preserves unrelated fields" against a copy in packages/agent/config, and none
// of them said anything about this one.
//
// It now shares config.MutateProjectConfig, so this asserts the CROSS-PACKAGE
// claim the copies never could — a permission rule written here survives the
// extension and model setters written there, in both orders.
func TestAProjectPermissionRuleAndTheConfigSettersKeepEachOthersKeys(t *testing.T) {
	cwd := testsupport.TempDir(t)
	rule := config.PermissionRuleConfig{Tool: "bash", Args: "rm *", Decision: "deny", Reason: "no"}

	if err := setProjectPermissionRule(cwd, rule, true); err != nil {
		t.Fatal(err)
	}
	if err := config.SetProjectExtensionDisabled(cwd, "memory", true); err != nil {
		t.Fatal(err)
	}
	if err := config.SetProjectModel(cwd, "anthropic", "opus"); err != nil {
		t.Fatal(err)
	}

	read := func() map[string]any {
		t.Helper()
		raw, err := os.ReadFile(config.ProjectConfigPath(cwd))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("the published document does not parse: %v\n%s", err, raw)
		}
		return doc
	}

	doc := read()
	for _, key := range []string{"permissions", "disable_extensions", "provider", "model"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("%q was dropped by a writer in another package", key)
		}
	}

	// And the reverse order: removing the rule must not take the others with it.
	if err := setProjectPermissionRule(cwd, rule, false); err != nil {
		t.Fatal(err)
	}
	doc = read()
	if _, ok := doc["permissions"]; ok {
		t.Error("removing the only rule left an empty permissions key behind")
	}
	for _, key := range []string{"disable_extensions", "provider", "model"} {
		if _, ok := doc[key]; !ok {
			t.Errorf("removing a permission rule dropped %q", key)
		}
	}
}
