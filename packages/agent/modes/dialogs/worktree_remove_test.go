package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/worktree"
	"terva.sh/terva/packages/tui"
)

func claimed(name, by string) *worktree.ListItem {
	return &worktree.ListItem{Name: name, Status: "claimed", ClaimedBy: &by}
}

func available(name string) *worktree.ListItem {
	return &worktree.ListItem{Name: name, Status: "available"}
}

// A stale claim is reported by the engine as AVAILABLE with a reason — never
// silently reclaimed — so the panel needs no notion of staleness of its own.
func staleAvailable(name string) *worktree.ListItem {
	return &worktree.ListItem{Name: name, Status: "available", StaleReason: "pid 4242 is gone"}
}

func openWith(items ...*worktree.ListItem) *WorktreeDialog {
	d := NewWorktreeDialog()
	d.Open(func() *worktree.ListResult { return &worktree.ListResult{Worktrees: items} },
		func() *worktree.CollectResult { return &worktree.CollectResult{} }, false)
	return d
}

func pressWt(d *WorktreeDialog, r rune) WorktreeAction {
	return d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: r})
}

// One press arms, the second commits. A list of checkouts is not somewhere a
// single stray keystroke should be able to delete anything.
func TestRemoveNeedsASecondPress(t *testing.T) {
	d := openWith(available("wt-a"), available("wt-b"))

	if act := pressWt(d, 'x'); act.RemoveName != "" {
		t.Fatalf("first press removed %q; it should only arm", act.RemoveName)
	}
	act := pressWt(d, 'x')
	if act.RemoveName != "wt-a" {
		t.Errorf("second press removed %q, want wt-a", act.RemoveName)
	}
}

// Any other key cancels — including one that does something else. An armed
// prompt that survives the next keystroke is a prompt that deletes the wrong
// thing.
func TestAnyOtherKeyDisarmsRemove(t *testing.T) {
	for _, k := range []rune{'r', 'c', 'l', 'q'} {
		d := openWith(available("wt-a"))
		pressWt(d, 'x')
		pressWt(d, k)
		if act := pressWt(d, 'x'); act.RemoveName != "" {
			t.Errorf("%q did not disarm: the next x removed %q immediately", string(k), act.RemoveName)
		}
	}
}

// Moving the selection disarms too. Arming on one row and committing on another
// is the specific way this goes wrong.
func TestMovingTheSelectionDisarms(t *testing.T) {
	d := openWith(available("wt-a"), available("wt-b"))
	pressWt(d, 'x')
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	act := pressWt(d, 'x')
	if act.RemoveName != "" {
		t.Fatalf("armed on wt-a, moved, and the next x removed %q", act.RemoveName)
	}
	if act := pressWt(d, 'x'); act.RemoveName != "wt-b" {
		t.Errorf("after re-arming on the new row, removed %q, want wt-b", act.RemoveName)
	}
}

// A live claim is refused: deleting a checkout out from under a running
// sub-agent is not a thing to offer behind one keypress, and the claim is the
// only evidence terva has that somebody is in there.
func TestALiveClaimIsRefused(t *testing.T) {
	d := openWith(claimed("wt-busy", "swarm:agent-1"))
	if act := pressWt(d, 'x'); act.RemoveName != "" {
		t.Fatalf("removed a claimed worktree: %q", act.RemoveName)
	}
	if act := pressWt(d, 'x'); act.RemoveName != "" {
		t.Fatalf("a second press pushed the removal through anyway: %q", act.RemoveName)
	}
	if got := d.Render(tui.Theme{}, 100); !strings.Contains(strings.Join(got, "\n"), "swarm:agent-1") {
		t.Errorf("the refusal does not say who holds it:\n%s", strings.Join(got, "\n"))
	}
}

// A stale row IS removable — that is the whole point. The engine already calls
// it available, so the panel does not re-decide.
func TestAStaleClaimIsRemovable(t *testing.T) {
	d := openWith(staleAvailable("wt-stale"))
	pressWt(d, 'x')
	if act := pressWt(d, 'x'); act.RemoveName != "wt-stale" {
		t.Errorf("a stale-but-available worktree was not removable, got %q", act.RemoveName)
	}
}

// X sweeps, and it counts only what it would actually take: the prompt must not
// promise to remove worktrees it will refuse.
func TestSweepPromptsWithTheAvailableCountOnly(t *testing.T) {
	d := openWith(available("a"), claimed("b", "swarm:x"), staleAvailable("c"))
	pressWt(d, 'X')
	body := strings.Join(d.Render(tui.Theme{}, 120), "\n")
	if !strings.Contains(body, "2") {
		t.Errorf("sweep prompt does not name the 2 it would remove:\n%s", body)
	}
	if act := pressWt(d, 'X'); !act.RemoveAvailable {
		t.Error("the second X did not commit the sweep")
	}
}

// Nothing to sweep says so rather than arming a no-op the user then confirms.
func TestSweepWithNothingAvailableRefuses(t *testing.T) {
	d := openWith(claimed("b", "swarm:x"))
	if act := pressWt(d, 'X'); act.RemoveAvailable {
		t.Fatal("armed a sweep with nothing available")
	}
	if act := pressWt(d, 'X'); act.RemoveAvailable {
		t.Fatal("a second X swept anyway")
	}
}

// The two prompts are separate: arming one must not commit the other.
func TestArmingOneRemovalDoesNotCommitTheOther(t *testing.T) {
	d := openWith(available("a"), available("b"))
	pressWt(d, 'x')
	if act := pressWt(d, 'X'); act.RemoveAvailable {
		t.Fatal("X committed a sweep off the single-remove arming")
	}
	d2 := openWith(available("a"))
	pressWt(d2, 'X')
	if act := pressWt(d2, 'x'); act.RemoveName != "" {
		t.Fatalf("x committed a single removal off the sweep arming: %q", act.RemoveName)
	}
}

// Reopening the panel starts clean — an arming that survived a close would fire
// on whatever row happened to be selected next time.
func TestReopenDisarms(t *testing.T) {
	d := openWith(available("a"))
	pressWt(d, 'x')
	d.Close()
	d.Open(func() *worktree.ListResult {
		return &worktree.ListResult{Worktrees: []*worktree.ListItem{available("a")}}
	},
		func() *worktree.CollectResult { return &worktree.CollectResult{} }, false)
	if act := pressWt(d, 'x'); act.RemoveName != "" {
		t.Fatalf("a reopened panel was still armed and removed %q", act.RemoveName)
	}
}
