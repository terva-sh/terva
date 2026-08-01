package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/tui"
)

// memArchiveFixture is a pane holding both tiers in both scopes, in the order
// the view builds them: a scope's active entries, then its archived ones.
func memArchiveFixture() *MemoryDialog {
	scopes := []MemoryScopeInfo{
		{Scope: "user", Label: "User memory", Count: 1, Bytes: 120, MaxBytes: 4096, Bound: true,
			ArchivedCount: 1, ArchivedBytes: 2048},
		{Scope: "project", Label: "Project memory", Count: 1, Bytes: 5900, MaxBytes: 16384, Bound: true,
			ArchivedCount: 3, ArchivedBytes: 9216},
	}
	rows := []MemoryRow{
		{Scope: "user", Text: "prefers worktrees over branch switching"},
		{Scope: "user", Text: "review style", Archived: true, Ref: "user:review-style", Keys: []string{"review"}},
		{Scope: "project", Text: "uses pnpm, not npm"},
		{Scope: "project", Text: "cutting a release", Archived: true, Ref: "project:cutting-a-release",
			Keys: []string{"release", "tag"}, Fired: true},
		{Scope: "project", Text: "the CI gate", Archived: true, Ref: "project:the-ci-gate",
			Keys: []string{"ci"}, Fired: true, Dropped: true},
		{Scope: "project", Text: "an orphan", Archived: true, Ref: "project:an-orphan"},
	}
	d := NewMemoryDialog()
	d.Open(scopes, rows)
	return d
}

// moveTo walks the cursor down n rows.
func memMoveTo(d *MemoryDialog, n int) {
	for i := 0; i < n; i++ {
		d.HandleKey(tui.Key{Kind: tui.KeyDown})
	}
}

// Deleting an ARCHIVED row must emit forget-by-Ref, not remove-by-text.
//
// The two tiers address entries differently: the active store matches a
// substring of the entry text, the archive matches an id. Sending an archived
// row down the active path would look for its TITLE among the one-line facts and
// miss — or, worse, hit an unrelated active entry that happens to contain those
// words and delete that instead.
func TestDeletingAnArchivedRowForgetsItByRef(t *testing.T) {
	d := memArchiveFixture()
	memMoveTo(d, 1) // the user scope's archived row

	act := d.HandleKey(rune_('d'))
	if act.Remove {
		t.Error("an archived row was sent down the active tier's remove path")
	}
	if !act.Forget {
		t.Fatalf("archived delete did not forget: %+v", act)
	}
	if act.Scope != "user" || act.Entry != "user:review-style" {
		t.Errorf("forget = scope %q entry %q, want the row's own scope and Ref", act.Scope, act.Entry)
	}
}

// And an ACTIVE row still takes the old path, unchanged — the archived branch
// must not have captured both.
func TestDeletingAnActiveRowStillRemovesByText(t *testing.T) {
	d := memArchiveFixture()
	act := d.HandleKey(rune_('d'))
	if act.Forget {
		t.Error("an active row was sent down the archive's forget path")
	}
	if !act.Remove || act.Entry != "prefers worktrees over branch switching" {
		t.Errorf("active delete = %+v, want remove carrying the full entry text", act)
	}
}

// Clear is the active tier only, and the confirmation says so. A confirmation
// that does not name what it is about to destroy is how someone confirms more
// than they meant to — and the archived half is the expensive one to rebuild.
func TestClearNamesTheActiveTierAndSparesTheArchive(t *testing.T) {
	d := memArchiveFixture()
	d.HandleKey(rune_('c'))
	status := d.status
	if !strings.Contains(status, "active") {
		t.Errorf("the clear confirmation does not say which tier it clears: %q", status)
	}
	if !strings.Contains(status, "archived") {
		t.Errorf("the clear confirmation does not say the archive survives: %q", status)
	}
	// Still confirms and still clears — the wording change must not have broken
	// the two-keystroke contract.
	if act := d.HandleKey(rune_('c')); !act.Clear || act.Scope != "user" {
		t.Errorf("second c did not clear the cursor's scope: %+v", act)
	}
}

// An archived entry is NOT in the model's context, and a list that renders it
// identically to one that is answers this pane's central question wrongly. The
// lead marker carries that, and it distinguishes the three states an archived
// entry can be in.
func TestArchivedRowsAreMarkedByWhetherTheyReachedTheModel(t *testing.T) {
	d := memArchiveFixture()
	render := func() string { return strings.Join(d.Render(tui.Dark, 100), "\n") }

	out := render()
	if !strings.Contains(out, "· review style") {
		t.Errorf("an archived row that did not fire is unmarked:\n%s", out)
	}
	if !strings.Contains(out, "▸ cutting a release") {
		t.Errorf("an archived row that fired is not marked as injected:\n%s", out)
	}
	if !strings.Contains(out, "✗ the CI gate") {
		t.Errorf("a budget-dropped row is not marked:\n%s", out)
	}
	// Active entries carry no marker: they are the unmarked, default case.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "uses pnpm") && strings.ContainsAny(line, "·▸✗") {
			t.Errorf("an ACTIVE entry was marked as archived: %q", line)
		}
	}
}

// The triggers appear under the SELECTED archived row. Under the selection only,
// because printing them on every row doubles the list's height; and at all,
// because the archive's failure mode is silence — a spec keyed on the answer's
// vocabulary produces nothing to notice.
func TestTriggersShowUnderTheSelectedArchivedRow(t *testing.T) {
	d := memArchiveFixture()
	memMoveTo(d, 3) // "cutting a release"

	out := strings.Join(d.Render(tui.Dark, 100), "\n")
	if !strings.Contains(out, "keys: release, tag") {
		t.Errorf("the selected archived row does not show its triggers:\n%s", out)
	}
	if strings.Contains(out, "keys: review") {
		t.Errorf("triggers rendered for an unselected row too:\n%s", out)
	}
	// An active row has no triggers to show and must not grow a blank detail line.
	d2 := memArchiveFixture()
	if got := strings.Join(d2.Render(tui.Dark, 100), "\n"); strings.Contains(got, "keys:") {
		t.Errorf("an active row rendered a trigger line:\n%s", got)
	}
}

// Fired-and-cut versus never-fired look identical from outside — neither
// reaches the model — and they need opposite fixes: one wants less competition,
// the other wants different keys. The detail line has to separate them.
func TestTheDetailLineSeparatesADropFromAMiss(t *testing.T) {
	d := memArchiveFixture()
	memMoveTo(d, 4) // "the CI gate": fired, then dropped for budget
	out := strings.Join(d.Render(tui.Dark, 120), "\n")
	if !strings.Contains(out, "cut to fit the budget") {
		t.Errorf("a budget drop is not distinguished from never firing:\n%s", out)
	}

	// An entry with no keys at all is the silent-inert case, and saying so is the
	// only way anyone finds out.
	d2 := memArchiveFixture()
	memMoveTo(d2, 5) // "an orphan": no keys
	out2 := strings.Join(d2.Render(tui.Dark, 120), "\n")
	if !strings.Contains(out2, "can never fire") {
		t.Errorf("a keyless archived entry is not flagged as unreachable:\n%s", out2)
	}
}

// The archived count sits OUTSIDE the fill fraction. Folding it in would suggest
// archiving something brings the scope closer to refusing the next write, when
// the entire point of the tier is that it does the opposite.
func TestTheScopeHeaderKeepsTheArchiveOutOfTheFillFraction(t *testing.T) {
	d := memArchiveFixture()
	out := strings.Join(d.Render(tui.Dark, 120), "\n")

	if !strings.Contains(out, "1 entries, 5.8K of 16.0K") {
		t.Errorf("the project fill fraction changed shape:\n%s", out)
	}
	if !strings.Contains(out, "3 archived") {
		t.Errorf("the archived count is missing from the scope header:\n%s", out)
	}
	if !strings.Contains(out, "out of context") {
		t.Errorf("the header does not say archived entries are out of context:\n%s", out)
	}
}

// Nothing this pane emits may be wider than the frame it is drawn in.
//
// Found by rendering the pane through a VT emulator and looking at it: the scope
// header was the one line never clipped, and the archived count plus the
// unreadable-file warning pushed it past the frame, where it wrapped mid-word
// and broke the border ("…unreadable ar" / "chive file(s)"). Every entry row was
// already clipped, so no existing test could see it.
//
// Asserts the RULE rather than that one line's length, so the next thing added
// to a header fails here instead of on someone's screen.
func TestNoRenderedLineOverflowsTheFrame(t *testing.T) {
	d := memArchiveFixture()
	d.SetStatus("a status line long enough to be worth clipping as well, several times over")
	// The problems warning is on the project scope in this fixture's sibling; add
	// it here so the widest possible header participates.
	d.scopes[1].Problems = []string{"broken.md: parse frontmatter: did not find expected node content"}

	for _, w := range []int{40, 62, 80, 100, 200} {
		for cursor := 0; cursor < 6; cursor++ {
			d2 := memArchiveFixture()
			d2.scopes[1].Problems = d.scopes[1].Problems
			memMoveTo(d2, cursor)
			for i, line := range d2.Render(tui.Dark, w) {
				if n := paneCols(line); n > w {
					t.Fatalf("width %d, cursor %d: line %d is %d columns wide:\n%q",
						w, cursor, i, n, stripSGR(line))
				}
			}
		}
	}
}

// stripSGR removes colour escapes so a line can be measured as it lands on a
// terminal. The dialog emits SGR only — no cursor motion — so this is the whole
// of the difference between the string and the cells.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func paneCols(s string) int { return len([]rune(stripSGR(s))) }

// An unreadable archive file is INERT — present, counted against the budget,
// unable to fire, with no other symptom anywhere. This header is the only place
// it is ever mentioned.
func TestUnreadableArchiveFilesAreSurfaced(t *testing.T) {
	scopes := []MemoryScopeInfo{{
		Scope: "project", Label: "Project memory", Count: 1, Bytes: 100, MaxBytes: 16384, Bound: true,
		Problems: []string{"broken.md: parse frontmatter: bad yaml"},
	}}
	d := NewMemoryDialog()
	d.Open(scopes, []MemoryRow{{Scope: "project", Text: "a fact"}})

	out := strings.Join(d.Render(tui.Dark, 120), "\n")
	if !strings.Contains(out, "could not be read") {
		t.Errorf("an unreadable archive file is invisible in the pane:\n%s", out)
	}
	// On its own line, not appended to the header — appending it is what pushed
	// the header past the frame, and clipping the combined line would have
	// dropped this warning first.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "could not be read") && strings.Contains(line, "Project memory") {
			t.Errorf("the warning is riding the scope header again: %q", stripSGR(line))
		}
	}
}
