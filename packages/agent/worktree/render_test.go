package worktree

import (
	"strings"
	"testing"
)

func sp(s string) *string { return &s }

func TestPanelLinesEmpty(t *testing.T) {
	lines := PanelLines(&ListResult{}, 0)
	if len(lines) == 0 || !strings.Contains(lines[0], "No worktrees") {
		t.Errorf("empty panel should explain itself: %v", lines)
	}
	if PanelLines(nil, 0) == nil {
		t.Error("nil result should still render lines, not nil")
	}
}

func TestPanelLinesContent(t *testing.T) {
	res := &ListResult{
		RepoKey:     "myrepo-abc123",
		CWDWorktree: sp("feat"),
		Worktrees: []*ListItem{
			{Name: "feat", Status: "claimed", ClaimedBy: sp("self"), BaseRef: "main", BaseCommit: "a1b2c3d4e5", HeadCommit: "f6e5d4c3b2", Dirty: true},
			{Name: "idle", Status: "available", BaseRef: "main", BaseCommit: "a1b2c3d4e5", HeadCommit: "a1b2c3d4e5"},
			{Name: "old", Status: "available", BaseRef: "dev", BaseCommit: "9988776655", HeadCommit: "9988776655", StaleReason: "session s9 ended"},
			{Name: "rogue", Status: "available", BaseRef: "", HeadCommit: "1212121212", Unmanaged: true},
		},
	}
	lines := PanelLines(res, 1)
	if len(lines) != 4 {
		t.Fatalf("want 4 rows, got %d: %v", len(lines), lines)
	}
	// Row 0: claimed-by-self, dirty, base@sha, head drift, and the cwd marker.
	if !strings.Contains(lines[0], "claimed(self)") || !strings.Contains(lines[0], "main@a1b2c3d") ||
		!strings.Contains(lines[0], "→f6e5d4c") || !strings.Contains(lines[0], "dirty") || !strings.Contains(lines[0], "(here)") {
		t.Errorf("row0 missing expected fields: %q", lines[0])
	}
	// Row 1 is selected → cursor; no head drift (head==base).
	if !strings.HasPrefix(lines[1], "▶ ") {
		t.Errorf("row1 should be selected: %q", lines[1])
	}
	if strings.Contains(lines[1], "→") {
		t.Errorf("row1 has no drift, should not show an arrow: %q", lines[1])
	}
	// Row 2: stale.
	if !strings.Contains(lines[2], "stale") {
		t.Errorf("row2 should show stale: %q", lines[2])
	}
	// Row 3: unmanaged.
	if !strings.Contains(lines[3], "unmanaged") {
		t.Errorf("row3 should show unmanaged: %q", lines[3])
	}
}

func TestStatusGlance(t *testing.T) {
	if StatusGlance(&ListResult{}) != "" {
		t.Error("empty glance should be empty")
	}
	res := &ListResult{Worktrees: []*ListItem{
		{Name: "a", ClaimedBy: sp("self")},
		{Name: "b", ClaimedBy: sp("other")},
		{Name: "c"},
	}}
	g := StatusGlance(res)
	if !strings.Contains(g, "worktrees 3") || !strings.Contains(g, "1 yours") {
		t.Errorf("glance: %q", g)
	}
}

func TestShortAndTrunc(t *testing.T) {
	if shortSHA("a1b2c3d4e5f6") != "a1b2c3d" {
		t.Errorf("shortSHA: %q", shortSHA("a1b2c3d4e5f6"))
	}
	if shortSHA("abc") != "abc" {
		t.Errorf("shortSHA short input: %q", shortSHA("abc"))
	}
	if got := trunc("abcdefghij", 5); got != "abcd…" {
		t.Errorf("trunc: %q", got)
	}
	if trunc("short", 10) != "short" {
		t.Error("trunc should leave short strings alone")
	}
}

func TestCollectLines(t *testing.T) {
	// Empty.
	if l := CollectLines(&CollectResult{}); len(l) == 0 || !strings.Contains(l[0], "No worktrees") {
		t.Errorf("empty collect: %v", l)
	}
	// All even → reassuring closing line, no merge hint.
	even := &CollectResult{Worktrees: []*CollectItem{{Name: "a", Branch: "wt/a", BaseRef: "main"}}}
	le := CollectLines(even)
	joinedEven := strings.Join(le, "\n")
	if !strings.Contains(joinedEven, "nothing to collect") || !strings.Contains(joinedEven, "even with their base") {
		t.Errorf("all-even collect should reassure: %v", le)
	}
	// Pending work → header, commit lines, flags, and the manual-merge reminder.
	pend := &CollectResult{Worktrees: []*CollectItem{
		{Name: "feat", Branch: "wt/feat", BaseRef: "main", Ahead: 2, Commits: []string{"abc feat: a", "def feat: b"}, Dirty: true, Unpushed: true},
	}}
	lp := CollectLines(pend)
	joined := strings.Join(lp, "\n")
	for _, want := range []string{"feat", "[wt/feat]", "+2 ahead of main", "✱dirty", "⇡unpushed", "feat: a", "merge manually"} {
		if !strings.Contains(joined, want) {
			t.Errorf("collect view missing %q in:\n%s", want, joined)
		}
	}
}
