package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

func memFixture() (*MemoryDialog, []MemoryScopeInfo, []MemoryRow) {
	scopes := []MemoryScopeInfo{
		{Scope: "user", Label: "User memory", Count: 1, Bytes: 120, MaxBytes: 1536, Bound: true},
		{Scope: "project", Label: "Project memory", Count: 2, Bytes: 5900, MaxBytes: 6144, Bound: true},
	}
	rows := []MemoryRow{
		{Scope: "user", Text: "prefers worktrees over branch switching"},
		{Scope: "project", Text: "uses pnpm, not npm"},
		{Scope: "project", Text: "tests live in crates/*/tests"},
	}
	d := NewMemoryDialog()
	d.Open(scopes, rows)
	return d, scopes, rows
}

// Delete acts on the row under the cursor and names its OWN scope, so a user
// working down one list never has to select a pane first.
func TestMemoryDialogDeleteCarriesTheRowsScope(t *testing.T) {
	d, _, rows := memFixture()
	act := d.HandleKey(rune_('d'))
	if !act.Remove || act.Scope != "user" || act.Entry != rows[0].Text {
		t.Fatalf("delete on row 0 = %+v, want a user-scope remove of %q", act, rows[0].Text)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	act = d.HandleKey(rune_('d'))
	if !act.Remove || act.Scope != "project" || act.Entry != rows[1].Text {
		t.Fatalf("delete on row 1 = %+v, want a project-scope remove of %q", act, rows[1].Text)
	}
}

// The entry must be the FULL text: the store matches by substring, and a
// truncated one could resolve to a different entry than the row displayed.
func TestMemoryDialogSendsTheFullEntry(t *testing.T) {
	long := strings.Repeat("a fact that is quite long ", 8)
	d := NewMemoryDialog()
	d.Open([]MemoryScopeInfo{{Scope: "project", Label: "Project memory", Count: 1}},
		[]MemoryRow{{Scope: "project", Text: long}})
	// Render clips for display; the action must not.
	_ = d.Render(tui.Theme{}, 60)
	act := d.HandleKey(rune_('d'))
	if act.Entry != long {
		t.Fatalf("delete sent %d chars, want the full %d — a clipped match can hit the wrong entry", len(act.Entry), len(long))
	}
}

// Clearing is irreversible and the model spent real turns deciding those facts
// were worth keeping, so it takes two keystrokes. Delete does not, because it
// removes exactly the one row the user is looking at.
func TestMemoryDialogClearNeedsConfirmation(t *testing.T) {
	d, _, _ := memFixture()
	if act := d.HandleKey(rune_('c')); act.Clear {
		t.Fatal("first c cleared without confirmation")
	}
	if d.status == "" {
		t.Error("first c gave no indication that a confirmation is pending")
	}
	act := d.HandleKey(rune_('c'))
	if !act.Clear || act.Scope != "user" {
		t.Fatalf("second c = %+v, want a user-scope clear", act)
	}
}

// A confirmation that survives an arrow key is a confirmation of nothing — the
// user has moved to a different row, possibly in a different scope.
func TestMemoryDialogClearConfirmationIsCancelledByAnyOtherKey(t *testing.T) {
	d, _, _ := memFixture()
	d.HandleKey(rune_('c'))                 // arm
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // move
	if act := d.HandleKey(rune_('c')); act.Clear {
		t.Fatal("a pending clear survived a cursor move and fired on the next c")
	}
}

// Moving to another scope and confirming must clear THAT scope, not the one the
// confirmation was armed on.
func TestMemoryDialogClearFollowsTheCursorScope(t *testing.T) {
	d, _, _ := memFixture()
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // onto a project row
	d.HandleKey(rune_('c'))
	act := d.HandleKey(rune_('c'))
	if !act.Clear || act.Scope != "project" {
		t.Fatalf("clear = %+v, want project scope", act)
	}
}

func TestMemoryDialogReloadAndClose(t *testing.T) {
	d, _, _ := memFixture()
	if act := d.HandleKey(rune_('r')); !act.Reload {
		t.Error("r did not request a reload")
	}
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); !act.Close || d.Active() {
		t.Errorf("esc = %+v, active=%v; want closed", act, d.Active())
	}
}

// An empty memory is a real state — it is the state right before the first fact
// lands — and must render as such rather than as an error or a blank frame.
func TestMemoryDialogRendersEmptyState(t *testing.T) {
	d := NewMemoryDialog()
	d.Open(nil, nil)
	out := strings.Join(d.Render(tui.Theme{}, 80), "\n")
	if !strings.Contains(out, "nothing saved yet") {
		t.Fatalf("empty render does not explain itself:\n%s", out)
	}
	// And no key may act on a row that isn't there.
	if act := d.HandleKey(rune_('d')); act.Remove {
		t.Error("delete acted on an empty list")
	}
}

// The cap is what refuses the model's next write; finding that out by hitting it
// is what cost turns in the reviewed session, so the pane shows the fill.
func TestMemoryDialogShowsScopeBudgets(t *testing.T) {
	d, _, _ := memFixture()
	out := strings.Join(d.Render(tui.Theme{}, 100), "\n")
	for _, want := range []string{"User memory", "Project memory", "5.8K", "6.0K"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q:\n%s", want, out)
		}
	}
}

// A scope with nowhere to persist must say so: entries there are accepted and
// then lost, and a list that silently will not survive is worse than an empty one.
func TestMemoryDialogFlagsAnUnboundScope(t *testing.T) {
	d := NewMemoryDialog()
	d.Open([]MemoryScopeInfo{{Scope: "project", Label: "Project memory", Count: 1, MaxBytes: 6144, Bound: false}},
		[]MemoryRow{{Scope: "project", Text: "a fact"}})
	out := strings.Join(d.Render(tui.Theme{}, 100), "\n")
	if !strings.Contains(out, "not saved") {
		t.Fatalf("an unbound scope renders as if it persists:\n%s", out)
	}
}

// The header says "1 entry", not "1 entries". A scope holding exactly one fact
// is ordinary rather than an edge case: the user scope on a fresh machine sits
// at one for as long as it takes to learn a second thing about the person.
//
// The fill and the cap are asserted in their rendered form, not as numbers,
// because that is the half nothing was watching. The Budget doc comment over in
// packages/agent/tools/memory quoted "32 KiB" through two cap raises while
// fmtKiB emitted "32.0K", and no test disagreed.
func TestScopeHeaderPluralAndRenderedCap(t *testing.T) {
	scopes := []MemoryScopeInfo{
		{Scope: "user", Label: "User memory", Count: 1, Bytes: 246, MaxBytes: 8192, Bound: true},
		{Scope: "project", Label: "Project memory", Count: 16, Bytes: 7040, MaxBytes: 32768, Bound: true},
	}

	one := scopeHeader(scopes, "user")
	if strings.Contains(one, "1 entries") {
		t.Errorf("header = %q, want the singular at a count of 1", one)
	}
	if !strings.Contains(one, "1 entry,") {
		t.Errorf("header = %q, want \"1 entry\"", one)
	}
	if !strings.Contains(one, "246B of 8.0K") {
		t.Errorf("header = %q, want the fill rendered against the 8192 cap", one)
	}

	many := scopeHeader(scopes, "project")
	if !strings.Contains(many, "16 entries,") {
		t.Errorf("header = %q, want the plural at a count of 16", many)
	}
	if !strings.Contains(many, "6.9K of 32.0K") {
		t.Errorf("header = %q, want the fill rendered against the 32768 cap", many)
	}

	// Zero takes the plural in English, and an empty scope is the first thing a
	// new project shows.
	empty := scopeHeader([]MemoryScopeInfo{
		{Scope: "project", Label: "Project memory", Count: 0, Bytes: 0, MaxBytes: 32768, Bound: true},
	}, "project")
	if !strings.Contains(empty, "0 entries") {
		t.Errorf("header = %q, want \"0 entries\"", empty)
	}
}

// A delete should leave the selection on the neighbouring row, not jump back to
// the top of a list the user was working down.
func TestMemoryDialogKeepsCursorAcrossRefresh(t *testing.T) {
	d, scopes, rows := memFixture()
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // last row
	d.SetItems(scopes, rows[:2])            // that row deleted
	if d.cursor != 1 {
		t.Fatalf("cursor = %d after the last row went away, want 1 (the new last)", d.cursor)
	}
}
