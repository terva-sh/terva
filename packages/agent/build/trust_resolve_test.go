package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// Trust's end-to-end effect runs through Resolve, so it lives here rather than
// with the trust policy in package permissions: that package resolves the
// verdict, this one is what the verdict changes.

// Trust via the STORE (persisted) loads project skills, just like
// --trust does — exercised end-to-end through Resolve.
func TestResolveTrustViaStoreLoadsProjectSkills(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := config.SaveConfig(config.Config{Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	proj := testsupport.TempDir(t)
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
	if err := config.TrustPath(proj, false); err != nil {
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
