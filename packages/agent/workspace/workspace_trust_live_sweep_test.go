package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/hooks"
	"terva.sh/terva/packages/testsupport"
)

// The behavioural half of the trust census (trust_census_test.go).
//
// That census is a STATIC guard: it reads every `.Trusted` field access and
// requires each to be classified — live, wire data, or a launch snapshot with
// the reason it is acceptable and, where one exists, the live twin that re-runs
// the decision later. What it cannot do is check that a claimed live twin is
// real. "setTrusted re-runs lore discovery" is a sentence in a map; a reader
// that captures the launch answer and a reader that re-resolves look identical
// to a parser.
//
// So this file flips trust on a live workspace and asserts what actually
// changes — one test per consumer the census names, plus one that pins the gap
// the census names as unfixed. Between them the census's claims stop being
// claims.
//
// The workspace verdict, the per-session flag, SessionInfo and the swarm_spawn
// gate are covered next door in workspace_trust_live_test.go; those were the
// three fixes that prompted all of this. What follows is the rest of the fan-out.

// loreSources returns the session's discovered lore entry sources, read under
// the lock like every other consumer.
func loreSources(s *wsSession) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.loreEntries))
	for _, e := range s.loreEntries {
		out = append(out, e.Source)
	}
	return out
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

func writeLore(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	doc := "---\nconstant: true\n---\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(doc), 0o644); err != nil {
		t.Fatalf("write lore %s: %v", name, err)
	}
}

// A newly trusted project's lore must be visible to the NEXT turn, not the next
// launch — reloadLore re-runs discovery with the new verdict.
//
// The user-scope entry is a control, not decoration: without it, "the project
// entry is absent" would also pass if discovery never ran at all, which is the
// failure this is meant to catch. It must be present throughout, so any
// assertion about the project entry is about trust rather than about lore being
// switched off in the fixture.
func TestATrustFlipReDiscoversProjectLore(t *testing.T) {
	w, s := trustSession(t)
	ctx := context.Background()

	writeLore(t, filepath.Join(config.TervaHome(), "lore"), "user-entry.md", "a user-scope fact")
	writeLore(t, filepath.Join(s.cwd, ".terva", "lore"), "project-entry.md", "a project-scope fact")

	// Re-run discovery at the current (untrusted) verdict so the baseline is
	// what a session in this state actually holds.
	s.reloadLore()
	got := loreSources(s)
	if !contains(got, "user-entry.md") {
		t.Fatalf("the user-scope control entry is missing (%v) — lore discovery is not running at all, "+
			"so nothing below would be testing trust", got)
	}
	if contains(got, "project-entry.md") {
		t.Fatalf("an UNTRUSTED workspace is serving project lore: %v", got)
	}

	if err := w.Trust(ctx, false); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	got = loreSources(s)
	if !contains(got, "project-entry.md") {
		t.Errorf("after Trust the project lore is still invisible (%v) — a newly trusted repo's "+
			"lore would wait for the next launch", got)
	}
	if !contains(got, "user-entry.md") {
		t.Errorf("the user-scope entry vanished across the flip (%v) — trust must widen the set, not replace it", got)
	}

	if err := w.Untrust(ctx); err != nil {
		t.Fatalf("Untrust: %v", err)
	}
	got = loreSources(s)
	if contains(got, "project-entry.md") {
		t.Errorf("after Untrust the project lore is still live (%v) — revoking trust must tear it "+
			"back down, or /untrust is advisory", got)
	}
	if !contains(got, "user-entry.md") {
		t.Errorf("the user-scope entry was torn down by Untrust (%v) — it was never project-gated", got)
	}
}

// trustSessionWithPolicyAndHooks is trustSession's sibling for the two
// consumers that only exist when the host has something to build them from.
//
// A bare fixture yields neither: the gate is built only when a policy exists
// (no rules, yolo → nil) and the hook engine only when some hook config merges
// non-nil. Both tests below skipped on that fixture, which is a pass that
// proves nothing — so the fixture seeds one user permission rule and one user
// hook. Neither is what the test asserts about; they exist so the objects under
// test are real.
func trustSessionWithPolicyAndHooks(t *testing.T) (*Workspace, *wsSession) {
	t.Helper()
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	if err := config.MutateConfig(func(c *config.Config) {
		// A user rule the project's bundle neither adds nor removes: its only
		// job is to make BuildPermissionPolicy return non-nil so a gate exists.
		c.Permissions = []config.PermissionRuleConfig{
			{Tool: "never_called_tool", Decision: "deny", Reason: "fixture: forces a policy to exist"},
		}
		// Likewise for the hook engine: a user hook is never trust-gated, so it
		// cannot influence the residue assertion — it only makes the engine real.
		c.Hooks = &hooks.Config{PreToolUse: []hooks.Spec{{Command: "true", Tools: "never_called_tool"}}}
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	cwd := testsupport.TempDir(t)
	w, err := NewWorkspace(build.Args{
		Provider: "openai", Model: "gpt-5", CWD: cwd, NoExt: true, NoMCP: true,
	}, "test")
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	info, err := w.CreateSession(context.Background(), ctrlproto.CreateOpts{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	s := w.existing(info.ID)
	if s == nil {
		t.Fatal("session did not materialize")
	}
	return w, s
}

// Trust gates one layer of the permission policy: the deny/ask rules a PROJECT
// extension bundle suggests. Those are restrictions the user opted into by
// trusting the repo — its extensions are now spawning — so they have to land on
// the running gate immediately, which is what refreshAllPolicies is for.
//
// Note the direction. This rule can only TIGHTEN, so the failure it guards
// against is a permissive one: trusting a repo, its extensions starting, and
// the deny rules they asked for not applying until restart.
func TestATrustFlipLandsProjectExtensionPermissionRules(t *testing.T) {
	w, s := trustSessionWithPolicyAndHooks(t)
	ctx := context.Background()

	if s.gate == nil {
		t.Fatal("the fixture built no gate — the seeded user rule should have forced a policy; " +
			"skipping here would be a pass that proves nothing")
	}

	bundle := filepath.Join(s.cwd, ".terva", "extensions", "demo")
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	manifest := `{"name":"demo","permissions":[{"tool":"bash","decision":"deny","reason":"demo bundle says no"}]}`
	if err := os.WriteFile(filepath.Join(bundle, "extension.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	hasDemoRule := func() bool {
		for _, r := range s.gate.Rules() {
			if r.Reason == "demo bundle says no" {
				return true
			}
		}
		return false
	}

	w.refreshAllPolicies() // baseline at the current (untrusted) verdict
	if hasDemoRule() {
		t.Fatal("an UNTRUSTED project's extension rules are already on the gate")
	}

	if err := w.Trust(ctx, false); err != nil {
		t.Fatalf("Trust: %v", err)
	}
	if !hasDemoRule() {
		t.Error("after Trust the project extension's deny rule is not on the gate — the repo's " +
			"extensions are loading while the restrictions they asked for wait for a restart")
	}

	if err := w.Untrust(ctx); err != nil {
		t.Fatalf("Untrust: %v", err)
	}
	if hasDemoRule() {
		t.Error("after Untrust the project extension's rule is still on the gate")
	}
}

// The residue that used to be pinned here is CLOSED — a newly trusted repo's
// hooks now fire on the next tool call. Its replacement, the positive
// behavioural assertion it asked for, lives in
// workspace_trust_hooks_live_test.go (TestATrustFlipStartsAndStopsProjectHooks).
//
// Kept as a note rather than deleted silently, because the old test asserted
// the hook engine POINTER was unchanged across a flip — and that is still true.
// The fix swaps the engine's specs in place, so a pointer-identity proxy cannot
// tell "never re-derived" from "re-derived without reallocating". Anyone who
// reaches for that shortcut again should know it reads the same either way.
