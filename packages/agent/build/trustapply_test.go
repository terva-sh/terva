package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/hooks"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// The order ApplyTrust runs its surfaces in is a safety rule, so it gets a test
// rather than a comment. On a WITHDRAWAL everything that lets the repository RUN
// CODE has to stop before anything else: a tool call that lands while trust is
// being revoked must not still be running the repo's pre-tool-use hook.
//
// The daemon had this backwards — hook specs were swapped after a per-session
// extension reload that can take a couple of seconds — which is the window this
// pins shut.

// hookOrderProject writes a project config whose pre-tool-use hook DENIES every
// call by exiting 2. Denial is what makes the chain observable: an exit-0 hook
// runs and says nothing, which is indistinguishable from not running at all.
func hookOrderProject(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("needs a POSIX shell to run a hook program")
	}
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	cwd := testsupport.TempDir(t)
	dir := filepath.Join(cwd, ".terva")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"hooks": map[string]any{"pre_tool_use": []map[string]any{{
		"command": "sh", "args": []string{"-c", "exit 2"},
	}}}}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return cwd
}

// hookArmed asks the engine the same question the tool-call ladder does, rather
// than reading its internals: is the project's hook in the chain right now?
func hookArmed(eng *hooks.Engine) bool {
	res := eng.RunPre(context.Background(), "bash", json.RawMessage(`{"command":"echo hi"}`))
	return res.Decision == "deny"
}

func TestApplyTrustStopsWhatTheRepoRunsBeforeWhatTheModelSees(t *testing.T) {
	cwd := hookOrderProject(t)
	args := Args{CWD: cwd, WithSkills: true}
	eng := BuildLiveTrustHookEngine(args, true)
	if eng == nil {
		t.Fatal("no standing engine for a project with hooks on disk; this test has nothing to observe")
	}

	// Each surface records when it ran AND what the hook chain looked like at
	// that moment. The second half is the real assertion: "rebuild ran last" is
	// weaker than "by the time the model's view changed, the repo had already
	// stopped executing".
	var order []string
	hooksLiveAt := map[string]bool{}
	note := func(name string) {
		order = append(order, name)
		hooksLiveAt[name] = hookArmed(eng)
	}

	ApplyTrust(context.Background(), false, TrustSurfaces{
		Args:    args,
		Hooks:   eng,
		Rebuild: func() { note("rebuild") },
		Lore:    func() { note("lore") },
		After:   func() { note("after") },
	})

	want := []string{"rebuild", "lore", "after"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("surfaces ran %v, want %v", order, want)
	}
	for _, name := range order {
		if hooksLiveAt[name] {
			t.Errorf("the project's hook chain was still armed when %q ran — a withdrawal has to stop the repo's "+
				"programs before it stops showing the repo to the model, or a tool call in that window executes "+
				"code from a repo the user just untrusted", name)
		}
	}
}

// The grant direction carries no safety weight, but it uses the same order so
// there is one sequence to reason about — and the hooks must be armed by the
// time anything downstream runs, or a host could reasonably conclude the flip
// had not happened yet.
func TestApplyTrustArmsHooksBeforeTheRestOnAGrant(t *testing.T) {
	cwd := hookOrderProject(t)
	args := Args{CWD: cwd, WithSkills: true}
	eng := BuildLiveTrustHookEngine(args, false)
	if eng == nil {
		t.Fatal("no standing engine for an untrusted project with hooks on disk; the flip would have nowhere to land")
	}
	if hookArmed(eng) {
		t.Fatal("an untrusted project's hook already runs — trust is not gating execution")
	}

	var armedWhenRebuilt bool
	ApplyTrust(context.Background(), true, TrustSurfaces{
		Args:    args,
		Hooks:   eng,
		Rebuild: func() { armedWhenRebuilt = hookArmed(eng) },
	})
	if !armedWhenRebuilt {
		t.Error("the hook chain was not yet armed when the tool set was rebuilt; the two halves of a grant should " +
			"not be observable in a half-applied state")
	}
}

// Every field is optional. A host that has no extension manager, no lore and no
// tail must not need a special case — that is what makes the struct a checklist
// a host can fill in as far as it goes rather than a contract it has to satisfy.
func TestApplyTrustToleratesAnEmptySetOfSurfaces(t *testing.T) {
	cwd := hookOrderProject(t)
	ApplyTrust(context.Background(), true, TrustSurfaces{Args: Args{CWD: cwd}})
	// A nil hook engine is the common case (no hooks anywhere) and SetSpecs is
	// nil-safe precisely so this needs no branch.
	ApplyTrust(context.Background(), false, TrustSurfaces{})
}

// Lore is trust-gated and lives on the running agent, not in the tool registry —
// so it is the surface a host closes last and forgets first. This pins the
// shared half both hosts use.
func TestRewireLoreContextFollowsTheVerdict(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")
	if err := config.SaveConfig(config.Config{Provider: "openai", Model: "gpt-5"}); err != nil {
		t.Fatal(err)
	}
	cwd := testsupport.TempDir(t)
	dir := filepath.Join(cwd, ".terva", "lore")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := "---\nname: dragons\nkeys: [dragon]\n---\nDragons hoard PROJECT-LORE-MARKER.\n"
	if err := os.WriteFile(filepath.Join(dir, "dragons.md"), []byte(entry), 0o644); err != nil {
		t.Fatal(err)
	}

	args := Args{CWD: cwd, Provider: "openai", Model: "gpt-5"}
	r, err := Resolve(args, true)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	ag := r.NewAgent()
	ag.SetMessages([]provider.Message{{
		Role:    provider.RoleUser,
		Content: []provider.Content{provider.TextBlock{Text: "tell me about the dragon"}},
	}})

	if strings.Contains(ag.ContextPreview(), "PROJECT-LORE-MARKER") {
		t.Fatal("an UNTRUSTED project's lore was already reaching the model; trust is not gating lore discovery")
	}
	if err := config.TrustPath(cwd, false); err != nil {
		t.Fatal(err)
	}
	if rr := RewireLoreContext(ag, args, EphemeralTail{}); rr == nil {
		t.Fatal("RewireLoreContext reported no resolve; a caller relying on it for its own re-pointing would " +
			"silently skip that work")
	}
	if !strings.Contains(ag.ContextPreview(), "PROJECT-LORE-MARKER") {
		t.Error("after the verdict moved, the agent's per-turn context still carries the launch answer — a newly " +
			"trusted project's keyed lore would never fire for the open session")
	}
}
