package worker

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/filelock"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// tervaHome points $TERVA_HOME at a throwaway directory, holding cfg as its
// config.json when cfg is not empty. Every test in this package that resolves
// an assembly needs it: config.LoadConfig and the global AGENTS.md chain both
// read that directory, so without it a test reads whoever ran it.
func tervaHome(t *testing.T, cfg string) string {
	t.Helper()
	home := testsupport.TempDir(t)
	if cfg != "" {
		if err := os.WriteFile(filepath.Join(home, "config.json"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TERVA_HOME", home)
	return home
}

// tervaHomeWithCredentials isolates config the way tervaHome does, but keeps the
// real credentials reachable: it LINKS auth.json and its lock into the throwaway
// home instead of copying them. The live tests use it, because they spend real
// money and so need a credential that a bare temp home does not have.
//
// A child process is why the link has to exist at all. These tests spawn a real
// terva, which resolves credentials from the TERVA_HOME it inherits.
// config.PinnedGlobalHome is the in-process pin that project-scoped mode uses
// for exactly this config/credential split, and it does not survive a process
// boundary — so the credential must be reachable at $TERVA_HOME/auth.json.
//
// It must not be a COPY. Store.RefreshOAuth persists a refreshed token back to
// that path. Against a copy the rotated token dies with the temp directory while
// the real auth.json keeps the token that was just spent, and a provider that
// rotates refresh tokens reads that replay as an attack and revokes the grant.
// The store's own comment names the result: "a logged-out user". A link makes
// the refresh write THROUGH to the real file, the way a production run does.
//
// The lock travels for the same reason. Store.lockPath is the store path plus
// ".lock", so a lock inside the temp home would guard nothing and would let this
// test refresh at the same time as a real terva — the exact replay that the
// store's double-check exists to prevent. Linking a lock that does not exist yet
// is correct: filelock opens with O_CREATE, which follows the link and creates
// the real file.
func tervaHomeWithCredentials(t *testing.T, cfg string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("this lends credentials with a symlink, which needs privilege on Windows")
	}

	// Resolve every real path BEFORE tervaHome redirects the environment.
	realAuth := config.AuthPath()
	realKey := config.SecretsKeyPath()
	if _, err := os.Stat(realAuth); err != nil {
		t.Skipf("no credentials to lend this test: %s (%v)", realAuth, err)
	}

	home := tervaHome(t, cfg)

	links := []struct{ target, name string }{
		{realAuth, "auth.json"},
		{realAuth + ".lock", "auth.json.lock"}, // mirrors Store.lockPath
	}
	// An encrypted auth.json cannot be opened without the key that `terva secret
	// init` wrote beside it, so that travels too. Only when it sits in the
	// credential home: a --secrets-key-file override points somewhere the child
	// would not look for it anyway.
	if _, err := os.Stat(realKey); err == nil && filepath.Dir(realKey) == filepath.Dir(realAuth) {
		links = append(links, struct{ target, name string }{realKey, filepath.Base(realKey)})
	}
	for _, l := range links {
		if err := os.Symlink(l.target, filepath.Join(home, l.name)); err != nil {
			t.Fatalf("link %s into the test home: %v", l.name, err)
		}
	}
	return home
}

// pinWeakTier points r at the cheap rung of whatever provider resolved, so a
// live test spends the least its subscription allows. It replaces a hardcoded
// model id, which two of the three live tests carried and the third lacked.
//
// The tier table matches model FAMILIES by substring ("haiku", "flash-lite",
// "nano"), not exact ids, so this survives the rename that eventually retires
// claude-haiku-4-5 for its successor. A pinned id would need chasing across
// three files, and its failure would reach only whoever paid to run them.
//
// Reading the provider from r rather than naming one is what lets the portable
// test use this at all: it spends terva's own subscription, and which provider
// that is belongs to the machine, not to this file.
//
// The overrides are nil deliberately. tervaHomeWithCredentials isolates config,
// so there are no user tier overrides to read, and nil keeps the pick identical
// on every machine.
//
// ResolveSwarmTier caps at the host model's own tier, so this never picks
// something STRONGER than what resolved. When a provider has neither an
// override nor a built-in table the pick is zero and the test proceeds on the
// host model, which is the fallback every other caller of it takes.
func pinWeakTier(t *testing.T, r *build.Resolved) {
	t.Helper()
	pick := tools.ResolveSwarmTier(r.Provider, r.Model, "weak", nil)
	if pick.IsZero() {
		t.Logf("provider %q has no weak rung; spending on the resolved default %q", r.Provider, r.Model)
		return
	}
	t.Logf("provider %q weak rung: %s (resolved default was %q)", r.Provider, pick.Label(), r.Model)
	r.Model = pick.Model
	if pick.Reasoning != "" {
		r.Reasoning = pick.Reasoning
	}
}

// The cost promise of every live test rests on that weak rung resolving. If a
// provider keeps its tier table while the weak match stops naming anything in
// the catalog — a family renamed, a rung retired — pinWeakTier falls back to the
// host model and the next live run quietly costs more. Nobody would notice,
// because the tests that would show it are the ones nobody runs.
//
// Self-enrolling on purpose: it walks the catalog instead of a list, so a
// provider added to swarmTierFamilies is covered without anyone recalling that
// this test exists.
func TestEveryProviderWithATierTableStillResolvesAWeakRung(t *testing.T) {
	seen, checked := map[string]bool{}, 0
	for _, m := range provider.Active() {
		if m.Provider == "" || seen[m.Provider] {
			continue
		}
		seen[m.Provider] = true
		if !tools.SwarmTierHasBuiltin(m.Provider) {
			continue
		}
		checked++
		if pick := tools.ResolveSwarmTier(m.Provider, "", "weak", nil); pick.IsZero() {
			t.Errorf("provider %q carries a built-in tier table, but no weak rung resolves — a live test on it would spend at the host model's rate", m.Provider)
		}
	}
	// Without this the test passes loudest when it walks nothing at all.
	if checked == 0 {
		t.Fatal("no catalog provider has a built-in tier table; this guard proved nothing")
	}
}

// pinWeakTier draws no coverage from the live tests, which never run
// unattended. These two cases are its whole contract: it moves DOWN to the
// cheap rung for a provider that has one, and it leaves the host model alone
// for a provider that does not.
func TestPinWeakTierDropsToTheCheapRungAndOtherwiseKeepsTheHostModel(t *testing.T) {
	t.Run("a provider with a tier table drops to its weak rung", func(t *testing.T) {
		r := build.Resolved{Provider: "anthropic", Model: "claude-opus-4-8"}
		pinWeakTier(t, &r)
		if r.Model == "claude-opus-4-8" {
			t.Fatal("still on the host model — the run would be billed at the strong rung")
		}
		if !strings.Contains(r.Model, "haiku") {
			t.Fatalf("anthropic's weak rung should be a haiku model, got %q", r.Model)
		}
	})
	t.Run("a provider without one keeps the host model", func(t *testing.T) {
		r := build.Resolved{Provider: "not-a-provider", Model: "some-host-model"}
		pinWeakTier(t, &r)
		if r.Model != "some-host-model" {
			t.Fatalf("the fallback must leave the host model alone, got %q", r.Model)
		}
	})
}

// The live tests that use tervaHomeWithCredentials cost real money, so nobody
// runs them casually and a regression there would sit undetected. Prove the two
// properties they depend on here instead, for free.
//
// The write-through half is the one with teeth: it is exactly what a COPY would
// break, and the failure mode of that break is a revoked grant rather than a red
// test. See the helper's comment for the chain.
func TestTheLentHomeIsolatesConfigButWritesCredentialsThrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the helper lends credentials with a symlink, which needs privilege on Windows")
	}
	// Stand in for the user's real home: a credential to lend, and a config that
	// must NOT follow.
	real := testsupport.TempDir(t)
	if err := os.WriteFile(filepath.Join(real, "auth.json"), []byte(`{"anthropic":{"refresh":"original"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, "config.json"), []byte(`{"model":"the-runners-own-choice"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TERVA_HOME", real)

	home := tervaHomeWithCredentials(t, "")
	if home == real {
		t.Fatal("the lent home IS the real home — nothing was isolated")
	}

	// Config stayed behind. This is the leak the whole exercise is about.
	if _, err := os.Stat(filepath.Join(home, "config.json")); !os.IsNotExist(err) {
		t.Fatalf("config.json followed into the lent home (stat err = %v)", err)
	}

	// The credential came along and reads back.
	got, err := os.ReadFile(filepath.Join(home, "auth.json"))
	if err != nil {
		t.Fatalf("read the lent credential: %v", err)
	}
	if !strings.Contains(string(got), "original") {
		t.Fatalf("the lent credential is not the real one: %s", got)
	}

	// A refresh WRITES THROUGH to the real file. Against a copy this assertion
	// fails, the rotated token dies with the temp directory, and the next real
	// run replays a spent one.
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(`{"anthropic":{"refresh":"rotated"}}`), 0o600); err != nil {
		t.Fatalf("simulate a refresh against the lent home: %v", err)
	}
	back, err := os.ReadFile(filepath.Join(real, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(back), "rotated") {
		t.Fatalf("a refresh in the lent home did not reach the real auth.json: %s", back)
	}

	// Locking the lent home takes the REAL lock, so this test and a real terva
	// cannot refresh at the same time. The link starts dangling; filelock opens
	// with O_CREATE, which follows it and creates the real file.
	lk, err := filelock.Acquire(filepath.Join(home, "auth.json.lock"))
	if err != nil {
		t.Fatalf("acquire the lent lock: %v", err)
	}
	lk.Release()
	if _, err := os.Stat(filepath.Join(real, "auth.json.lock")); err != nil {
		t.Fatalf("locking the lent home did not create the REAL lock file: %v", err)
	}
}

// loadedRepo resolves a project with every discovery channel populated: an
// AGENTS.md chain, an explicit context file, a persona charter. The point is a
// briefing composed from a MAXIMAL assembly — a leak test against a bare prompt
// proves only that we did not leak what was not there.
func loadedRepo(t *testing.T) build.Resolved {
	t.Helper()

	// The assembly must not depend on whose machine runs it. The real
	// $TERVA_HOME otherwise joins in: its AGENTS.md is discovered and becomes a
	// briefing pointer outside the lease, which fails the vessel and pointer
	// tests below on any developer machine that has one.
	//
	// Emptying the home does not go far enough, and that is the interesting
	// half. swarm-worktrees and auto-swarm are gated on config.LoadConfig, so a
	// CLEAN home drops them and the "maximal assembly" promised above quietly
	// shrinks — weakest on CI, which is the one machine nobody inspects. Switch
	// them on deliberately, and every machine composes the same maximal
	// briefing with the most harness-local segments there are to leak.
	tervaHome(t, `{"auto_swarm_enabled":true,"swarm_worktrees":true}`)

	repo := testsupport.TempDir(t)
	write := func(name, body string) string {
		p := filepath.Join(repo, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("AGENTS.md", "Run the linter before you commit.")
	ctx := write("house-style.md", "Two spaces, never tabs.")

	r, err := build.Resolve(build.Args{
		CWD:                repo,
		ContextFiles:       []string{ctx},
		AppendSystemPrompt: []string{"Prefer boring solutions."},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func demoTask() Task {
	return Task{
		Mission:    "Make the flaky retry test deterministic.",
		Acceptance: "The suite passes 20 times in a row.",
	}
}

// THE GATE. Prompt leakage is the only silent failure in this design — a leaked
// segment produces a worker that runs and hallucinates tools — so "we removed
// enough" must be mechanical, not remembered. A fully loaded assembly, composed
// and rendered, must contain nothing that stays home.
func TestComposedBriefingLeaksNothing(t *testing.T) {
	r := loadedRepo(t)
	b := Compose(r, demoTask(), Workspace{Path: "/lease/wt-1", Branch: "work/flaky"})

	if leaks := Scrub(b.Text(), r); len(leaks) > 0 {
		for _, l := range leaks {
			t.Errorf("%v", l)
		}
		t.Fatalf("briefing leaked %d segment(s)/tool(s) across the harness boundary:\n%s", len(leaks), b.Text())
	}
}

// The scrub is only worth trusting if it can fail. Hand it the very thing it
// exists to catch — terva's conventions segment, forwarded verbatim — and it
// must say so. Without this, a Scrub that returned nil unconditionally would
// pass the gate above forever.
func TestScrubCatchesAForwardedHarnessSegment(t *testing.T) {
	r := loadedRepo(t)
	b := Compose(r, demoTask(), Workspace{Path: "/lease/wt-1"})

	var conventions string
	for _, seg := range r.SystemSegments {
		if seg.Source == build.SourceConventions {
			conventions = seg.Text
		}
	}
	if conventions == "" {
		t.Fatal("no conventions segment in a coding assembly")
	}

	leaked := b.Text() + "\n\n" + conventions
	leaks := Scrub(leaked, r)
	if len(leaks) == 0 {
		t.Fatal("scrub passed a briefing carrying terva's conventions verbatim — it cannot fail, so it proves nothing")
	}
	var found bool
	for _, l := range leaks {
		if l.Kind == "segment" && l.Detail == build.SourceConventions {
			found = true
		}
	}
	if !found {
		t.Errorf("scrub flagged something, but not the conventions segment: %v", leaks)
	}
}

// A leak survives a re-flow. A backend that word-wraps or re-indents the text it
// was handed has still leaked it, so the scrub matches on a distinctive run
// rather than on an exact copy.
func TestScrubSurvivesReflow(t *testing.T) {
	r := loadedRepo(t)
	var vessel string
	for _, seg := range r.SystemSegments {
		if seg.Source == build.SourceVessel {
			vessel = seg.Text
		}
	}
	if vessel == "" {
		t.Fatal("no vessel segment")
	}
	reflowed := strings.ReplaceAll(vessel, " ", "\n  ")
	if leaks := Scrub(reflowed, r); len(leaks) == 0 {
		t.Error("a re-wrapped harness segment is still a leaked harness segment")
	}
}

// Rule 1, mechanically: a terva-specific tool name must never appear. The bare
// primitives (bash/edit/read/write) are deliberately NOT scrubbed — every coding
// agent has them under those names, and they are ordinary English besides.
func TestScrubNamesTervaSpecificToolsOnly(t *testing.T) {
	r := loadedRepo(t)
	if leaks := Scrub("Call terva_status to check your context window.", r); len(leaks) != 1 || leaks[0].Kind != "tool" {
		t.Errorf("terva_status must be caught: %v", leaks)
	}
	// The worker HAS an edit tool — Claude Code calls it Edit. Saying the word is
	// not a leak, and a scrub that flagged it would flag "read the spec" too, and
	// then someone would switch the scrub off.
	if leaks := Scrub("Read the spec, then edit the file and write it out.", r); len(leaks) != 0 {
		t.Errorf("ordinary English must not trip the scrub: %v", leaks)
	}
}

// Rule 2: pointers, not payloads. The project's own conventions are a PATH in
// the briefing, and their contents are nowhere in it.
func TestDiscoveryOwnedContentIsPointedAtNotPasted(t *testing.T) {
	r := loadedRepo(t)
	b := Compose(r, demoTask(), Workspace{Path: "/lease/wt-1"})
	text := b.Text()

	if strings.Contains(text, "Run the linter before you commit.") {
		t.Error("AGENTS.md was PASTED into the briefing; the worker will find it itself, and a paste can contradict its own discovery")
	}
	var pointed bool
	for _, p := range b.Pointers {
		if strings.HasSuffix(p.Path, "AGENTS.md") {
			pointed = true
			if p.Note == "" {
				t.Error("a bare path tells a worker to go read something without saying whether it matters")
			}
		}
	}
	if !pointed {
		t.Errorf("AGENTS.md should be a pointer, got %+v", b.Pointers)
	}
	if !strings.Contains(text, "AGENTS.md") {
		t.Error("the pointer never made it into the rendered text")
	}
}

// The fixture promises a MAXIMAL assembly, and two of its richest harness-local
// segments are switched on by config rather than being there for free. That
// makes the promise breakable in silence: drop the config from loadedRepo and
// the leak gate still passes, having scrubbed a briefing with less in it to
// leak. It read as "maximal" on a developer machine whose own config happened
// to enable them, and was never maximal on CI at all.
func TestTheLeakFixtureCarriesTheOptionalHarnessSegments(t *testing.T) {
	r := loadedRepo(t)

	for _, want := range []string{build.SourceSwarmWorktrees, build.SourceAutoSwarm} {
		var found bool
		for _, seg := range r.SystemSegments {
			if seg.Source == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the fixture no longer assembles the %q segment, so the leak gate above "+
				"never scrubs it. Restore the config.json loadedRepo writes into $TERVA_HOME.", want)
		}
	}
}

// The identity crosses and the vessel does not. This is the whole thesis of the
// identity/vessel split, and it only pays off here.
func TestIdentityTravelsAndTheVesselStaysHome(t *testing.T) {
	r := loadedRepo(t)
	b := Compose(r, demoTask(), Workspace{Path: "/lease/wt-1"})
	text := b.Text()

	if b.Identity.Intro == "" {
		t.Error("the worker was given no identity at all")
	}
	if !strings.Contains(strings.ToLower(text), "mieli") {
		t.Errorf("the mind should travel:\n%s", text)
	}
	for _, home := range []string{"terva", "pine tar", "TEHR-vah"} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(home)) {
			t.Errorf("the vessel stays home, but %q crossed:\n%s", home, text)
		}
	}
}

// A user's own appended instruction is theirs, and it is portable by contract.
// Dropping it would silently disobey them.
func TestUserAppendedInstructionsCross(t *testing.T) {
	r := loadedRepo(t)
	b := Compose(r, demoTask(), Workspace{Path: "/lease/wt-1"})
	if !strings.Contains(b.Text(), "Prefer boring solutions.") {
		t.Errorf("--append-system-prompt is the user's own instruction and must reach the worker:\n%s", b.Text())
	}
}

// There is no task_end abroad: a foreign worker just stops talking, and
// "stopped" is indistinguishable from "finished", "gave up", and "crashed"
// unless the briefing says what done looks like.
func TestBriefingStatesHowToReportBack(t *testing.T) {
	b := Compose(loadedRepo(t), demoTask(), Workspace{Path: "/lease/wt-1"})
	if b.Reporting == "" {
		t.Fatal("no reporting contract")
	}
	if !strings.Contains(b.Text(), "final message") {
		t.Errorf("the worker must be told its last message IS the report:\n%s", b.Reporting)
	}
}

// A worker does not work where we do. Pointing it at OUR copy of AGENTS.md sends
// it outside its own workspace, to read a file that may not even match the branch
// it was handed. The file it wants is the one under its own root.
//
// The tests above could not have caught this — they assert what is absent, and a
// pointer at the wrong file is very much present. It showed up on reading a real
// composed briefing out loud.
func TestPointersAimAtTheWorkersOwnCheckout(t *testing.T) {
	r := loadedRepo(t)
	b := Compose(r, demoTask(), Workspace{Path: "/leases/wt-7", Branch: "work/x"})

	// re-rooting goes through filepath.Join, so the workspace prefix carries the
	// OS separator (backslash on Windows) — build it the same way, not as a literal.
	wsPrefix := filepath.Clean("/leases/wt-7") + string(filepath.Separator)
	// An empty pointer set satisfies every loop below without checking anything,
	// and the set is exactly what this fixture's $TERVA_HOME isolation changed.
	if len(b.Pointers) == 0 {
		t.Fatal("no pointers at all — the loops below would report a clean audit of nothing")
	}
	for _, p := range b.Pointers {
		if !strings.HasPrefix(p.Path, wsPrefix) {
			t.Errorf("pointer %q is outside the worker's workspace — it names our checkout, not its own", p.Path)
		}
		if strings.HasPrefix(p.Path, r.CWD) {
			t.Errorf("pointer %q still points into the dispatching terva's directory", p.Path)
		}
	}
	// With no lease, the worker shares our directory and there is nothing to
	// translate — re-rooting anyway would invent a path that does not exist.
	shared := Compose(r, demoTask(), Workspace{})
	for _, p := range shared.Pointers {
		if !strings.HasPrefix(p.Path, r.CWD) {
			t.Errorf("no lease means no re-rooting, but %q moved", p.Path)
		}
	}
}

// A file outside our checkout — a global $TERVA_HOME/AGENTS.md, a --context-file
// from elsewhere on disk — has no counterpart inside the lease. Re-rooting it
// would fabricate a path to a file that was never there.
func TestOutOfTreePointersAreNotReRooted(t *testing.T) {
	const outside = "/etc/terva/house.md"
	if got := rerootIntoWorkspace(outside, "/repo", "/leases/wt-7"); got != outside {
		t.Errorf("re-rooted an out-of-tree file to %q; it has no counterpart in the lease", got)
	}
	want := filepath.Join("/leases/wt-7", "pkg", "AGENTS.md") // OS-native, like the code under test
	if got := rerootIntoWorkspace("/repo/pkg/AGENTS.md", "/repo", "/leases/wt-7"); got != want {
		t.Errorf("in-tree file should re-root under the lease, got %q", got)
	}
}

// A composed charter reaches the worker as two segments under one source
// label. carryPortable used to ASSIGN, so the second overwrote the first and a
// worker was briefed with the specialization stripped of the contract it
// qualifies — a briefing that still looked complete.
func TestBothHalvesOfAComposedCharterTravel(t *testing.T) {
	r := loadedRepo(t)
	r.SystemSegments = []build.PromptSegment{
		{Source: build.SourceCharter, Text: "Inspect before changing.", Origin: []string{"embedded:mieli.md"}},
		{Source: build.SourceCharter, Text: "Track what is in flight.", Origin: []string{"/personas/assistant.md"}},
	}
	b := Compose(r, demoTask(), Workspace{})
	for _, want := range []string{"Inspect before changing.", "Track what is in flight."} {
		if !strings.Contains(b.Identity.Charter, want) {
			t.Errorf("briefing charter lost %q:\n%s", want, b.Identity.Charter)
		}
	}
	// Order survives too: the base states the contract, the extension qualifies it.
	if strings.Index(b.Identity.Charter, "Inspect") > strings.Index(b.Identity.Charter, "Track") {
		t.Errorf("charter halves arrived out of order:\n%s", b.Identity.Charter)
	}
}
