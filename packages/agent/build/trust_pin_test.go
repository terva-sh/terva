package build

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/testsupport"
)

// A host whose trust posture is fixed for the process's lifetime (rpc: there is
// no trust verb on that wire) still re-runs the whole of Resolve when it
// rebuilds its tool set. Without a pin, that one path re-read the trust store
// while the extension manager and the hook engine kept the launch verdict — so
// a `terva trust` in another terminal could hand the model a trusted repo's
// project skills inside a process whose extensions and hooks were still gated
// as untrusted. A SPLIT posture is worse than either consistent one, and the
// process cannot notice it about itself.

// pinProject writes a project skill whose description is a marker, and returns
// the project dir. The marker in the system prompt is the observable: it is
// there exactly when Resolve treated the workspace as trusted.
func pinProject(t *testing.T) string {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := config.SaveConfig(config.Config{Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	proj := testsupport.TempDir(t)
	dir := filepath.Join(proj, ".terva", "skills", "repo-skill")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: repo-skill\ndescription: PINNED-SKILL-MARKER\n---\nbody"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return proj
}

func resolvedTrustedContent(t *testing.T, args Args) bool {
	t.Helper()
	r, err := Resolve(args, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return strings.Contains(r.SystemPrompt, "PINNED-SKILL-MARKER")
}

func TestATrustPinOverridesTheStoreInBothDirections(t *testing.T) {
	proj := pinProject(t)
	base := Args{CWD: proj, WithSkills: true}

	// Sanity: without a pin the store is the answer, in both states. Without
	// this the assertions below could pass on a project that never loaded its
	// skill for some unrelated reason.
	if resolvedTrustedContent(t, base) {
		t.Fatal("an untrusted workspace loaded its project skill; this test cannot say anything about pinning")
	}
	if err := config.TrustPath(proj, false); err != nil {
		t.Fatal(err)
	}
	if !resolvedTrustedContent(t, base) {
		t.Fatal("a store-trusted workspace did not load its project skill; this test cannot say anything about pinning")
	}

	// Pinned OFF against a store that says trusted. This is the direction the
	// existing Args.Trust flag cannot express — it can only force trust ON —
	// and it is the one a launched-untrusted rpc worker needs.
	off := false
	pinnedOff := base
	pinnedOff.TrustPin = &off
	if resolvedTrustedContent(t, pinnedOff) {
		t.Error("a resolve pinned to UNTRUSTED loaded the project skill anyway — the store moved under a process " +
			"whose extensions and hooks are still gated as untrusted, and its tool set no longer matches them")
	}

	// And the mirror: pinned ON against a store that says untrusted, which is
	// what a worker launched with --trust needs after someone runs `terva untrust`.
	if err := config.UntrustPath(proj); err != nil {
		t.Fatal(err)
	}
	on := true
	pinnedOn := base
	pinnedOn.TrustPin = &on
	if !resolvedTrustedContent(t, pinnedOn) {
		t.Error("a resolve pinned to TRUSTED lost the project skill when the store was cleared — the pin has to hold " +
			"the launch verdict in both directions or it only half-exists")
	}
}

// The pin is opt-in. Every live-trust host (the workspace daemon, acp) leaves it
// nil precisely so a trust verb's write to the store is what the next rebuild
// reads; a pin that defaulted to the launch verdict would silently make those
// hosts fixed-trust again.
func TestNoPinMeansTheStoreDecides(t *testing.T) {
	proj := pinProject(t)
	args := Args{CWD: proj, WithSkills: true}
	if args.TrustPin != nil {
		t.Fatal("Args' zero value carries a pin; a host would have to remember to clear it, which is backwards")
	}
	if err := config.TrustPath(proj, false); err != nil {
		t.Fatal(err)
	}
	if !resolvedTrustedContent(t, args) {
		t.Error("with no pin a resolve ignored a trust grant the store already carries — a live-trust host's /trust " +
			"would persist the verdict and change nothing")
	}
}
