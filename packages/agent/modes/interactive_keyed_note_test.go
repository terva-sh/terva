package modes

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/modes/widgets"
	"terva.sh/terva/packages/tui"
)

func newNoteHost() *Interactive {
	return &Interactive{cfg: InteractiveConfig{Theme: tui.Theme{Muted: 8, Tool: 2, Accent: 4, Warning: 214, Error: 1}}}
}

func (i *Interactive) plainNotes() string {
	var b strings.Builder
	for _, line := range i.extNotes {
		b.WriteString(widgets.StripANSIBytes(line))
		b.WriteByte('\n')
	}
	return b.String()
}

// The complaint, exactly: a note that tracks a changing fact used to append a
// fresh line per change, stacking a history of superseded claims above the
// input. One key, one note, however many times it is written.
func TestAKeyedNoteRewritesItselfInsteadOfStacking(t *testing.T) {
	i := newNoteHost()
	i.ReplaceNote("workspace", "swarm.worktrees", "1 swarm worktree leased", "info")
	i.ReplaceNote("workspace", "swarm.worktrees", "2 swarm worktrees leased", "info")
	i.ReplaceNote("workspace", "swarm.worktrees", "3 swarm worktrees leased", "info")

	got := i.plainNotes()
	if n := len(i.extNotes); n != 1 {
		t.Errorf("%d lines in the block, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, "3 swarm worktrees") {
		t.Errorf("the latest state is not what is shown:\n%s", got)
	}
	for _, stale := range []string{"1 swarm worktree leased", "2 swarm worktrees"} {
		if strings.Contains(got, stale) {
			t.Errorf("superseded claim %q is still up:\n%s", stale, got)
		}
	}
}

// An empty message retracts. ClearNotes cannot do this job: it strips by
// EXTENSION, so a workspace note clearing itself would also take the
// permission-policy warnings and extension-load errors sharing that label.
func TestAnEmptyKeyedNoteRetractsWithoutTouchingItsNeighbours(t *testing.T) {
	i := newNoteHost()
	i.appendExtensionNote("workspace", "permission policy: 2 rules could not be parsed", "warn")
	i.ReplaceNote("workspace", "swarm.worktrees", "2 swarm worktrees leased", "info")
	i.ReplaceNote("workspace", "swarm.worktrees", "", "info")

	got := i.plainNotes()
	if strings.Contains(got, "swarm worktrees") {
		t.Errorf("the note did not retract:\n%s", got)
	}
	if !strings.Contains(got, "permission policy") {
		t.Errorf("retracting one workspace note took an unrelated one with it:\n%s", got)
	}
}

// A multi-line note is owned whole. The swarm record is two lines — the state
// and the grant command — and retracting one of them would leave a bare
// `terva trust …` hint under nothing.
func TestAMultiLineKeyedNoteIsRetractedWhole(t *testing.T) {
	i := newNoteHost()
	i.ReplaceNote("workspace", "swarm.worktrees", "2 swarm worktrees leased — all RESTRICTED\ngrant all 2 with `terva trust /wt --parent`", "info")
	if len(i.extNotes) != 2 {
		t.Fatalf("want 2 rendered lines, got %d", len(i.extNotes))
	}
	i.ReplaceNote("workspace", "swarm.worktrees", "2 swarm worktrees released, kept for review", "info")
	got := i.plainNotes()
	if strings.Contains(got, "terva trust") {
		t.Errorf("half the old note survived the rewrite:\n%s", got)
	}
	if n := len(i.extNotes); n != 1 {
		t.Errorf("%d lines after the rewrite, want 1:\n%s", n, got)
	}
}

// resetNotes must clear the block AND the keyed bookkeeping together. A stale
// entry naming a line that is gone makes the next rewrite append rather than
// replace — one note becomes two, which is the bug wearing a different hat.
func TestResettingTheBlockDoesNotStrandTheKeyedBookkeeping(t *testing.T) {
	i := newNoteHost()
	i.ReplaceNote("workspace", "swarm.worktrees", "2 swarm worktrees leased", "info")
	i.mu.Lock()
	i.resetNotes()
	i.mu.Unlock()
	if len(i.notesByKey) != 0 {
		t.Fatalf("keyed bookkeeping survived the reset: %v", i.notesByKey)
	}

	i.ReplaceNote("workspace", "swarm.worktrees", "1 swarm worktree leased", "info")
	i.ReplaceNote("workspace", "swarm.worktrees", "2 swarm worktrees leased", "info")
	if n := len(i.extNotes); n != 1 {
		t.Errorf("%d lines after a reset then two writes, want 1:\n%s", n, i.plainNotes())
	}
}

// Two keys are two notes. The swarm record rewriting itself must not evict
// whatever else is keyed alongside it.
func TestKeysDoNotEvictEachOther(t *testing.T) {
	i := newNoteHost()
	i.ReplaceNote("workspace", "swarm.worktrees", "2 swarm worktrees leased", "info")
	i.ReplaceNote("workspace", "other.thing", "something else entirely", "info")
	i.ReplaceNote("workspace", "swarm.worktrees", "2 swarm worktrees released, kept for review", "info")

	got := i.plainNotes()
	if !strings.Contains(got, "something else entirely") {
		t.Errorf("rewriting one key dropped another's note:\n%s", got)
	}
	if !strings.Contains(got, "released, kept for review") {
		t.Errorf("the rewritten note is missing:\n%s", got)
	}
	if n := len(i.extNotes); n != 2 {
		t.Errorf("%d lines, want 2 (one per key):\n%s", n, got)
	}
}
