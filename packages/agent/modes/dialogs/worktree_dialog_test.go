package dialogs

import (
	"fmt"
	"strings"
	"testing"

	"terva.sh/terva/packages/agent/worktree"
	"terva.sh/terva/packages/tui"
)

func worktreeFixture() *worktree.ListResult {
	self := "self"
	other := "sess-other"
	here := "feat-a"
	return &worktree.ListResult{
		RepoKey:     "terva-abc1234567",
		CWDWorktree: &here,
		Worktrees: []*worktree.ListItem{
			{Name: "feat-a", Path: "/wt/feat-a", Branch: "wt/feat-a", BaseRef: "main", BaseCommit: "aaaa1111bbbb", HeadCommit: "cccc2222dddd", Status: "claimed", ClaimedBy: &self, Dirty: true},
			{Name: "feat-b", Path: "/wt/feat-b", Branch: "wt/feat-b", BaseRef: "main", BaseCommit: "aaaa1111bbbb", HeadCommit: "aaaa1111bbbb", Status: "claimed", ClaimedBy: &other},
			{Name: "idle", Path: "/wt/idle", Branch: "wt/idle", BaseRef: "main", BaseCommit: "aaaa1111bbbb", Status: "available"},
		},
	}
}

func worktreeCollectFixture() *worktree.CollectResult {
	return &worktree.CollectResult{
		RepoKey: "terva-abc1234567",
		Worktrees: []*worktree.CollectItem{
			{Name: "feat-a", Branch: "wt/feat-a", BaseRef: "main", Ahead: 2, Commits: []string{"abc one", "def two"}, Dirty: true},
			{Name: "idle", Branch: "wt/idle", BaseRef: "main"},
		},
	}
}

// The list view renders every worktree through the shared engine renderer,
// tags the cwd row, and moves the selection cursor with the arrow keys.
func TestWorktreeDialogListAndSelection(t *testing.T) {
	th := tui.Dark
	d := NewWorktreeDialog()
	d.Open(func() *worktree.ListResult { return worktreeFixture() }, func() *worktree.CollectResult { return worktreeCollectFixture() }, false)

	body := stripANSILines(d.Render(th, 80))
	joined := strings.Join(body, "\n")
	for _, want := range []string{"feat-a", "claimed(self)", "(here)", "✱dirty", "feat-b", "idle", "available"} {
		if !strings.Contains(joined, want) {
			t.Errorf("list view missing %q:\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, "▶ feat-a") {
		t.Errorf("selection cursor should start on the first row:\n%s", joined)
	}

	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	body = stripANSILines(d.Render(th, 80))
	if !strings.Contains(strings.Join(body, "\n"), "▶ feat-b") {
		t.Errorf("↓ should move the cursor to the second row:\n%s", strings.Join(body, "\n"))
	}
}

// ↵ on a row asks the host to cd into that worktree; the collect toggle
// switches views; r asks for a refresh; esc closes.
func TestWorktreeDialogActions(t *testing.T) {
	d := NewWorktreeDialog()
	d.Open(func() *worktree.ListResult { return worktreeFixture() }, func() *worktree.CollectResult { return worktreeCollectFixture() }, false)

	if act := d.HandleKey(tui.Key{Kind: tui.KeyEnter}); act.CdPath != "/wt/feat-a" {
		t.Errorf("↵ should cd into the selected worktree, got %+v", act)
	}
	if act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'r'}); !act.Refresh {
		t.Errorf("r should request a refresh, got %+v", act)
	}

	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'c'})
	body := strings.Join(stripANSILines(d.Render(tui.Dark, 80)), "\n")
	for _, want := range []string{"+2 ahead of main", "abc one", "nothing to collect", "merge manually"} {
		if !strings.Contains(body, want) {
			t.Errorf("collect view missing %q:\n%s", want, body)
		}
	}
	// ↵ in collect view must NOT cd (no selection there).
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEnter}); act.CdPath != "" {
		t.Errorf("↵ in collect view should be inert, got %+v", act)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'l'})
	if body := strings.Join(stripANSILines(d.Render(tui.Dark, 80)), "\n"); !strings.Contains(body, "claimed(self)") {
		t.Errorf("l should return to the list view:\n%s", body)
	}

	if act := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); !act.Close {
		t.Errorf("esc should close, got %+v", act)
	}
}

// An empty repo renders the friendly empty state, and ↵ is inert.
// When the list is taller than the window, moving the selection scrolls the
// window to keep the ▶ cursor visible (so ↵ never cd-s into an off-screen
// worktree), and the list-view footer describes the keys as "select", not "scroll".
func TestWorktreeDialogSelectionStaysVisibleWhenScrolled(t *testing.T) {
	th := tui.Dark
	many := &worktree.ListResult{RepoKey: "r"}
	for i := 0; i < 10; i++ {
		many.Worktrees = append(many.Worktrees, &worktree.ListItem{
			Name: fmt.Sprintf("wt-%d", i), Path: fmt.Sprintf("/wt/%d", i), BaseRef: "main", Status: "available",
		})
	}
	d := NewWorktreeDialog()
	d.Open(func() *worktree.ListResult { return many }, func() *worktree.CollectResult { return nil }, false)
	d.MaxRows = 3

	for i := 0; i < 9; i++ { // move onto the last row, well past the 3-row window
		d.HandleKey(tui.Key{Kind: tui.KeyDown})
	}
	joined := strings.Join(stripANSILines(d.Render(th, 80)), "\n")
	if !strings.Contains(joined, "▶ wt-9") {
		t.Errorf("selection scrolled off-screen; the window did not follow:\n%s", joined)
	}
	if strings.Contains(joined, "wt-0") {
		t.Errorf("a row far above the window is still drawn:\n%s", joined)
	}
	if strings.Contains(joined, "↑/↓ scroll") {
		t.Errorf("list-view footer mislabels the arrows as scroll:\n%s", joined)
	}
	if !strings.Contains(joined, "↑/↓ select") {
		t.Errorf("list-view footer should describe the arrows as select:\n%s", joined)
	}
}

func TestWorktreeDialogEmpty(t *testing.T) {
	d := NewWorktreeDialog()
	d.Open(func() *worktree.ListResult { return &worktree.ListResult{RepoKey: "r-x"} }, func() *worktree.CollectResult { return nil }, false)
	body := strings.Join(stripANSILines(d.Render(tui.Dark, 80)), "\n")
	if !strings.Contains(body, "No worktrees yet") {
		t.Errorf("empty list state missing:\n%s", body)
	}
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEnter}); act.CdPath != "" {
		t.Errorf("↵ with no rows should be inert, got %+v", act)
	}
}
