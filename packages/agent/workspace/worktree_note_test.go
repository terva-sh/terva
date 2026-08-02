package workspace

import (
	"path/filepath"
	"strings"
	"testing"
)

func leased(path string, trusted bool) worktreeProvenance {
	return worktreeProvenance{
		Repo: "dol-clone", Path: path, Base: "main", Commit: "808adab80015",
		Trusted: trusted, Reason: "no trust entry",
	}
}

func batchUnder(dir string, n int, trusted bool) []worktreeProvenance {
	var out []worktreeProvenance
	for i := 0; i < n; i++ {
		out = append(out, leased(filepath.Join(dir, string(rune('a'+i))+"-worktree"), trusted))
	}
	return out
}

// The bug this note exists to fix: the old per-lease record said the sub-agent
// "runs without this project's extensions" in the present tense, and nothing
// ever retracted it. A swarm that finished half an hour ago left that claim
// pinned above the input until the next prompt.
func TestTheNoteStopsClaimingTheSubAgentsAreRunning(t *testing.T) {
	batch := batchUnder("/wt", 3, false)

	live := renderSwarmWorktreeNote(batch, 3)
	if !strings.Contains(live, "leased") {
		t.Errorf("a live batch does not say it is leased:\n%s", live)
	}
	if !strings.Contains(live, "running without") {
		t.Errorf("a live batch does not say what restricted costs:\n%s", live)
	}

	done := renderSwarmWorktreeNote(batch, 0)
	if strings.Contains(done, "running without") {
		t.Errorf("a finished batch still claims the sub-agents are running:\n%s", done)
	}
	if !strings.Contains(done, "ran") || !strings.Contains(done, "released") {
		t.Errorf("a finished batch does not say it finished:\n%s", done)
	}
	// The paths survive: a released worktree is kept for review, so granting it
	// trust is still something you might want to do.
	if !strings.Contains(done, "terva trust") {
		t.Errorf("the grant hint vanished with the last lease:\n%s", done)
	}
}

// One line, not one per worktree. Three spawns used to be three notices of
// roughly four wrapped rows each.
func TestABatchIsOneRecordNotOnePerWorktree(t *testing.T) {
	got := renderSwarmWorktreeNote(batchUnder("/wt", 3, false), 3)
	if n := strings.Count(got, "dol-clone"); n != 1 {
		t.Errorf("the shared repo is repeated %d×, want once:\n%s", n, got)
	}
	if n := strings.Count(got, "terva trust"); n != 1 {
		t.Errorf("%d grant commands, want one:\n%s", n, got)
	}
	if n := len(strings.Split(got, "\n")); n > 2 {
		t.Errorf("record is %d lines, want at most 2:\n%s", n, got)
	}
	if !strings.Contains(got, "3 swarm worktrees") {
		t.Errorf("the count is missing:\n%s", got)
	}
}

// --parent trusts every future worktree under the directory, not only the ones
// on screen. Offering it is worth it; offering it silently would be a quiet
// widening of trust in the one record whose whole job is reporting the
// trust boundary.
func TestTheParentGrantSaysThatItIsWiderThanWhatIsOnScreen(t *testing.T) {
	got := renderSwarmWorktreeNote(batchUnder("/wt/dol-clone-7c1/worktrees", 3, false), 3)
	if !strings.Contains(got, "--parent") {
		t.Fatalf("three worktrees under one directory got no --parent grant:\n%s", got)
	}
	if !strings.Contains(got, "every future worktree") {
		t.Errorf("--parent is offered without saying it widens past these three:\n%s", got)
	}
}

// A single lease gets its EXACT path. --parent there would grant strictly more
// than the one worktree that exists, for no saved keystrokes.
func TestASingleLeaseGetsItsExactPathNotAParentGrant(t *testing.T) {
	got := renderSwarmWorktreeNote([]worktreeProvenance{leased("/wt/only", false)}, 1)
	if strings.Contains(got, "--parent") {
		t.Errorf("one worktree was offered a parent grant:\n%s", got)
	}
	if !strings.Contains(got, "/wt/only") {
		t.Errorf("the exact path is missing:\n%s", got)
	}
	if !strings.Contains(got, "1 swarm worktree ") {
		t.Errorf("singular reads as plural:\n%s", got)
	}
}

// The hint is meant to be pasted back. macOS puts the worktree root under
// "Application Support", so an unquoted path is a command that trusts the
// wrong directory — and an abbreviated one is a command that does not run.
func TestTheGrantHintIsPasteable(t *testing.T) {
	dir := "/Users/x/Library/Application Support/terva/worktrees/dol-clone-7c1/worktrees"
	got := renderSwarmWorktreeNote(batchUnder(dir, 2, false), 2)
	if !strings.Contains(got, "'"+dir+"'") {
		t.Errorf("the path is not quoted whole:\n%s", got)
	}
	if strings.Contains(got, "…") {
		t.Errorf("the path is elided, so the command will not run:\n%s", got)
	}
}

// A fact that is not shared is not a fact about the batch. Reporting the first
// worktree's commit as all of theirs would be a guess.
func TestFactsAreOnlyPrintedWhenEveryWorktreeAgrees(t *testing.T) {
	batch := batchUnder("/wt", 2, false)
	batch[1].Commit = "deadbeef0000"
	got := renderSwarmWorktreeNote(batch, 2)
	for _, sha := range []string{"808adab80015", "deadbeef0000"} {
		if strings.Contains(got, sha) {
			t.Errorf("a commit only one worktree has is reported for the batch:\n%s", got)
		}
	}
	if !strings.Contains(got, "dol-clone") {
		t.Errorf("the repo IS shared and should still show:\n%s", got)
	}
}

// Mixed trust is the case a count hides. "all RESTRICTED" over a batch where
// one is trusted is simply false, and the grant hint must cover only the ones
// that need it.
func TestAMixedBatchReportsHowManyAreRestricted(t *testing.T) {
	batch := batchUnder("/wt/shared", 3, false)
	batch[0].Trusted = true
	got := renderSwarmWorktreeNote(batch, 3)
	if strings.Contains(got, "all RESTRICTED") {
		t.Errorf("a mixed batch is reported as uniformly restricted:\n%s", got)
	}
	if !strings.Contains(got, "2 of 3 RESTRICTED") {
		t.Errorf("the restricted count is missing:\n%s", got)
	}
	if !strings.Contains(got, "grant all 2") {
		t.Errorf("the hint does not scope itself to the restricted ones:\n%s", got)
	}
}

// A fully trusted batch has nothing to act on, so it must not carry a grant
// hint telling the operator to fix something that is not broken.
func TestATrustedBatchCarriesNoGrantHint(t *testing.T) {
	got := renderSwarmWorktreeNote(batchUnder("/wt", 2, true), 2)
	if strings.Contains(got, "terva trust") {
		t.Errorf("a trusted batch was told to grant trust:\n%s", got)
	}
	if !strings.Contains(got, "TRUSTED") {
		t.Errorf("a trusted batch does not say so:\n%s", got)
	}
}

// Paths that do not share an immediate parent must not be collapsed onto some
// common ancestor: walking up would land on a directory holding far more than
// these worktrees, and this string goes into a trust grant.
func TestUnrelatedPathsAreNeverCollapsedOntoAnAncestor(t *testing.T) {
	batch := []worktreeProvenance{leased("/a/one/wt", false), leased("/b/two/wt", false)}
	got := renderSwarmWorktreeNote(batch, 2)
	if strings.Contains(got, "--parent") {
		t.Errorf("two unrelated paths were collapsed into one parent grant:\n%s", got)
	}
	if !strings.Contains(got, "no shared parent") {
		t.Errorf("the operator is not told why there is no single command:\n%s", got)
	}
}
