package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/build"
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
