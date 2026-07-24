package dialogs

import (
	"strings"
	"testing"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

func archiveDialog(t *testing.T) *SessionDialog {
	t.Helper()
	d := NewSessionDialog()
	d.List = func() []core.SessionSummary {
		return []core.SessionSummary{
			{Path: "/s/live-a.jsonl", Title: "live a", MessageCount: 3},
			{Path: "/s/live-b.jsonl", Title: "live b", MessageCount: 2},
		}
	}
	d.ListArchived = func() []core.ArchivedSession {
		return []core.ArchivedSession{
			{SessionSummary: core.SessionSummary{Title: "old one", MessageCount: 12}, ID: "20260101-120000-aaaaaaaa", Bytes: 2048},
			{SessionSummary: core.SessionSummary{Title: "older", MessageCount: 40}, ID: "20260101-110000-bbbbbbbb", Bytes: 9000},
		}
	}
	d.Open("/root", "/cwd")
	return d
}

// `a` archives the row under the cursor and hands back its PATH — the handle the
// live picker rows carry.
func TestSessionDialogArchiveKey(t *testing.T) {
	d := archiveDialog(t)
	act := d.HandleKey(rune_('a'))
	if !act.Archive || act.Path != "/s/live-a.jsonl" {
		t.Fatalf("act = %+v, want Archive on the cursor row's path", act)
	}
	if act.Delete || act.Select {
		t.Error("archive also asked for a delete or a resume")
	}
}

// `d` does NOT delete on the spot: it arms a confirmation. Delete is the only key
// in this picker that destroys a transcript and it sits next to one that does not.
func TestSessionDialogDeleteAsksFirst(t *testing.T) {
	d := archiveDialog(t)
	act := d.HandleKey(rune_('d'))
	if act.Delete {
		t.Fatal("`d` deleted immediately, with no confirmation")
	}
	if !d.confirming {
		t.Fatal("`d` did not arm a confirmation")
	}
	// The prompt has to name the session — the rows are near-identical.
	body := strings.Join(d.Render(tui.Dark, 80), "\n")
	if !strings.Contains(body, "live a") {
		t.Errorf("confirm prompt does not name the session:\n%s", body)
	}

	act = d.HandleKey(rune_('y'))
	if !act.Delete || act.Path != "/s/live-a.jsonl" {
		t.Fatalf("act = %+v, want Delete on the confirmed row", act)
	}
	if d.confirming {
		t.Error("still confirming after the answer")
	}
}

// Any other key cancels — including the navigation keys, which must not be read
// as an answer just because they are not 'y'.
func TestSessionDialogDeleteConfirmCancels(t *testing.T) {
	for _, k := range []tui.Key{rune_('n'), {Kind: tui.KeyDown}, {Kind: tui.KeyEnter}, {Kind: tui.KeyEsc}} {
		d := archiveDialog(t)
		d.HandleKey(rune_('d'))
		act := d.HandleKey(k)
		if act.Delete {
			t.Errorf("key %+v was taken as confirmation of a delete", k)
		}
		if d.confirming {
			t.Errorf("key %+v left the confirmation armed", k)
		}
		// A cancelled confirm must not leak into another verb either.
		if act.Select || act.Close || act.Archive {
			t.Errorf("key %+v resolved to %+v while cancelling a confirm", k, act)
		}
	}
}

// `A` switches to the archive and back, and the archive lists what ListArchived
// returned rather than the live sessions.
func TestSessionDialogArchiveView(t *testing.T) {
	d := archiveDialog(t)
	d.HandleKey(rune_('A'))
	if !d.ShowingArchived() {
		t.Fatal("`A` did not switch to the archive")
	}
	if d.rowCount() != 2 {
		t.Fatalf("archive shows %d rows, want the 2 archived", d.rowCount())
	}
	body := strings.Join(d.Render(tui.Dark, 100), "\n")
	if !strings.Contains(body, "old one") {
		t.Errorf("archive view does not render the archived rows:\n%s", body)
	}
	// The size is the justification for compressing at all; show it.
	if !strings.Contains(body, "2K") {
		t.Errorf("archive view does not show the compressed size:\n%s", body)
	}

	d.HandleKey(rune_('A'))
	if d.ShowingArchived() {
		t.Fatal("`A` did not switch back to the live sessions")
	}
	if d.rowCount() != 2 || d.sessions[0].Path != "/s/live-a.jsonl" {
		t.Error("the live list did not come back intact")
	}
}

// In the archive, both enter and `r` restore, and they carry the archived ID —
// never a path, which for an archived row points at a .jsonl.gz.
func TestSessionDialogRestoreFromArchive(t *testing.T) {
	for _, k := range []tui.Key{{Kind: tui.KeyEnter}, rune_('r')} {
		d := archiveDialog(t)
		d.HandleKey(rune_('A'))
		act := d.HandleKey(k)
		if !act.Restore {
			t.Fatalf("key %+v in the archive did not restore", k)
		}
		if act.ID != "20260101-120000-aaaaaaaa" {
			t.Errorf("restore id = %q, want the archived session's id", act.ID)
		}
		if act.Select {
			t.Error("an archived row resolved as a resume; there is nothing to resume yet")
		}
	}
}

// The destructive and rename verbs are LIVE-list verbs. In the archive they must
// not fire: there is no live transcript under the cursor to rename or delete.
func TestSessionDialogArchiveViewHasNoLiveVerbs(t *testing.T) {
	d := archiveDialog(t)
	d.HandleKey(rune_('A'))
	for _, k := range []rune{'d', 'a', 'g'} {
		act := d.HandleKey(rune_(k))
		if act.Delete || act.Archive || act.GenerateTitle {
			t.Errorf("key %q fired a live-list verb from inside the archive: %+v", k, act)
		}
		if d.confirming {
			t.Errorf("key %q armed a delete confirmation inside the archive", k)
		}
	}
	if d.renaming {
		t.Error("the archive view entered rename mode")
	}
}

// A frontend with no archive (a replay carrier) must not offer the keys at all —
// an `a` that reports "unavailable" after the fact is worse than no binding.
func TestSessionDialogWithoutArchiveOffersNoArchiveKeys(t *testing.T) {
	d := NewSessionDialog()
	d.List = func() []core.SessionSummary {
		return []core.SessionSummary{{Path: "/s/a.jsonl", Title: "a", MessageCount: 1}}
	}
	d.Open("/root", "/cwd")

	if act := d.HandleKey(rune_('a')); act.Archive {
		t.Error("archive fired on a frontend that cannot archive")
	}
	if act := d.HandleKey(rune_('A')); act.Restore || d.ShowingArchived() {
		t.Error("the archive view opened on a frontend that has no archive")
	}
	hint := strings.Join(d.Render(tui.Dark, 100), "\n")
	if strings.Contains(hint, "a archive") {
		t.Errorf("the hint advertises archiving where it is unavailable:\n%s", hint)
	}
}

// Re-opening the picker always lands on the live list. A dialog that reopened
// into the archive would look like every session had vanished.
func TestSessionDialogOpenResetsToLiveList(t *testing.T) {
	d := archiveDialog(t)
	d.HandleKey(rune_('A'))
	d.HandleKey(rune_('d')) // also leaves a confirm armed in the old state
	d.Close()

	d.Open("/root", "/cwd")
	if d.ShowingArchived() {
		t.Error("the picker reopened into the archive")
	}
	if d.confirming {
		t.Error("a stale delete confirmation survived a reopen")
	}
}

// Refresh must re-list whichever view is showing. Re-running the session scan
// under the archive would silently drop the user back onto the live list.
func TestSessionDialogRefreshKeepsTheArchiveView(t *testing.T) {
	d := archiveDialog(t)
	d.HandleKey(rune_('A'))
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // cursor on the second archived row

	d.Refresh("/root", "/cwd")
	if !d.ShowingArchived() {
		t.Fatal("a refresh knocked the picker out of the archive")
	}
	if d.rowCount() != 2 {
		t.Fatalf("archive shows %d rows after refresh, want 2", d.rowCount())
	}
	if d.archivedRows[d.cursor].ID != "20260101-110000-bbbbbbbb" {
		t.Error("the refresh lost the cursor's place in the archive")
	}
}
