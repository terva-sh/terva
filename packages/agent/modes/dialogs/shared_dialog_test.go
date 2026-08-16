package dialogs

// The /shared panel: the session's deliverables plus the three verbs that make
// a listing useful rather than merely informative.
//
// The properties worth pinning are the ones a user meets when something is
// wrong: a key must act on the row the cursor is actually on, an expired file
// must be refused rather than handed to an action that will fail on it, and an
// empty drawer must read as an ordinary state rather than as a broken panel.

import (
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/tui"
)

// sharedDialogNow is the clock the expiry tests read, so a rendered "expired"
// is an assertion rather than a race against the store's TTL.
var sharedDialogNow = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

func sharedEntry(id, name, kind string, size int64) ctrlproto.SharedFileEntry {
	return ctrlproto.SharedFileEntry{
		SharedFile: core.SharedFile{ID: id, Name: name, Kind: kind, Size: size},
		Path:       "/home/u/.local/state/terva/shared/s1/" + id + "-" + name,
	}
}

func sharedFixture() []ctrlproto.SharedFileEntry {
	return []ctrlproto.SharedFileEntry{
		sharedEntry("shr_a", "report.pdf", "document", 2048),
		sharedEntry("shr_b", "chart.png", "image", 51200),
		sharedEntry("shr_c", "take.mp3", "audio", 1048576),
	}
}

// openShared builds a panel over a fixture with the clock pinned.
func openShared(files []ctrlproto.SharedFileEntry) *SharedDialog {
	d := NewSharedDialog()
	d.Now = func() time.Time { return sharedDialogNow }
	d.Open(func() []ctrlproto.SharedFileEntry { return files })
	return d
}

func sharedBody(d *SharedDialog) string {
	return strings.Join(stripANSILines(d.Render(tui.Dark, 80)), "\n")
}

// The panel lists what the session handed over, with enough about each file to
// tell them apart.
func TestSharedDialogListsTheSessionsFiles(t *testing.T) {
	d := openShared(sharedFixture())

	body := sharedBody(d)
	for _, want := range []string{"report.pdf", "chart.png", "take.mp3", "document", "2.0 KB", "50.0 KB", "1.0 MB"} {
		if !strings.Contains(body, want) {
			t.Errorf("panel missing %q:\n%s", want, body)
		}
	}
	// The keys have to be discoverable: a list with hidden verbs is a list.
	for _, want := range []string{"copy path", "open", "save here"} {
		if !strings.Contains(body, want) {
			t.Errorf("the hint does not mention %q:\n%s", want, body)
		}
	}
}

// A session that shared nothing gets a sentence, not an error and not a blank
// pane. Most sessions never hand anything over.
func TestSharedDialogSaysWhenThereIsNothing(t *testing.T) {
	d := openShared(nil)

	body := sharedBody(d)
	if !strings.Contains(body, "has not shared any files") {
		t.Errorf("an empty drawer should say so:\n%s", body)
	}
}

// Each action key must carry the id of the row the cursor is on. Acting on the
// wrong row is the failure that matters here: every verb is destructive of the
// user's attention, and save writes a file.
func TestSharedDialogActionsCarryTheSelectedRow(t *testing.T) {
	d := openShared(sharedFixture())
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // onto chart.png

	if act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'c'}); act.CopyID != "shr_b" {
		t.Errorf("c copied %q, want the selected row shr_b", act.CopyID)
	}
	if act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'o'}); act.OpenID != "shr_b" {
		t.Errorf("o opened %q, want shr_b", act.OpenID)
	}
	if act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 's'}); act.SaveID != "shr_b" {
		t.Errorf("s saved %q, want shr_b", act.SaveID)
	}
	// Enter is open: the obvious thing to do with a file you selected.
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEnter}); act.OpenID != "shr_b" {
		t.Errorf("enter opened %q, want shr_b", act.OpenID)
	}
}

// Home/End and the arrows move the selection, and the actions follow.
func TestSharedDialogNavigation(t *testing.T) {
	d := openShared(sharedFixture())

	d.HandleKey(tui.Key{Kind: tui.KeyEnd})
	if act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'c'}); act.CopyID != "shr_c" {
		t.Errorf("End then c = %q, want the last row", act.CopyID)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyHome})
	if act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'c'}); act.CopyID != "shr_a" {
		t.Errorf("Home then c = %q, want the first row", act.CopyID)
	}
	// Up at the top and down at the bottom must not walk off the list.
	d.HandleKey(tui.Key{Kind: tui.KeyUp})
	if act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'c'}); act.CopyID != "shr_a" {
		t.Errorf("↑ past the top moved to %q, want to stay on the first row", act.CopyID)
	}
	d.HandleKey(tui.Key{Kind: tui.KeyEnd})
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	if act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'c'}); act.CopyID != "shr_c" {
		t.Errorf("↓ past the bottom moved to %q, want to stay on the last row", act.CopyID)
	}
}

// An expired row keeps its place — the session did share it — but every action
// on it is refused with the reason, rather than dispatched into a filesystem
// error the user has to interpret.
func TestSharedDialogRefusesAnExpiredFile(t *testing.T) {
	dead := sharedEntry("shr_gone", "old.pdf", "document", 10)
	dead.ExpiresAt = sharedDialogNow.Add(-time.Hour).Format(time.RFC3339)
	d := openShared([]ctrlproto.SharedFileEntry{dead})

	act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'o'})
	if act.OpenID != "" {
		t.Errorf("opened %q, want an expired file refused", act.OpenID)
	}
	body := sharedBody(d)
	if !strings.Contains(body, "old.pdf") {
		t.Errorf("an expired share still belongs in the list:\n%s", body)
	}
	if !strings.Contains(body, "expired") {
		t.Errorf("the panel does not say the bytes are gone:\n%s", body)
	}
}

// An expiry the daemon did not send is UNKNOWN, and refusing on unknown would
// make every action fail against a daemon that does not send the field.
func TestSharedDialogActsWhenTheExpiryIsUnknown(t *testing.T) {
	d := openShared([]ctrlproto.SharedFileEntry{sharedEntry("shr_a", "report.pdf", "document", 10)})

	if act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'o'}); act.OpenID != "shr_a" {
		t.Errorf("a file with no expiry was refused; want it treated as live")
	}
}

// The host performs every action, so it is also the only side that knows how
// one went. The notice it reports has to reach the panel.
func TestSharedDialogShowsTheHostsNotice(t *testing.T) {
	d := openShared(sharedFixture())

	d.Notice("copied /tmp/report.pdf", false)
	if body := sharedBody(d); !strings.Contains(body, "copied /tmp/report.pdf") {
		t.Errorf("the action's result is not shown:\n%s", body)
	}

	// And it belongs to the act that raised it: moving the selection must not
	// leave a stale "copied" hanging under a different row.
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	if body := sharedBody(d); strings.Contains(body, "copied /tmp/report.pdf") {
		t.Errorf("the notice outlived the row it described:\n%s", body)
	}
}

// r asks the host to refetch; esc closes. Neither is row-scoped.
func TestSharedDialogRefreshAndClose(t *testing.T) {
	d := openShared(sharedFixture())

	if act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'r'}); !act.Refresh {
		t.Error("r should ask the host to refetch the listing")
	}
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); !act.Close {
		t.Error("esc should close the panel")
	}
}

// The list is refetched every render, so a sweep can shorten it under the
// cursor. An action then has no row to act on and must say so rather than
// index past the end.
func TestSharedDialogSurvivesTheListShrinkingUnderTheCursor(t *testing.T) {
	files := sharedFixture()
	d := NewSharedDialog()
	d.Now = func() time.Time { return sharedDialogNow }
	d.Open(func() []ctrlproto.SharedFileEntry { return files })

	d.HandleKey(tui.Key{Kind: tui.KeyEnd}) // onto the third row
	files = files[:1]                      // the sweeper takes the rest

	act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'c'})
	if act.CopyID != "" {
		t.Errorf("copied %q from a row that no longer exists", act.CopyID)
	}
	if body := sharedBody(d); !strings.Contains(body, "nothing to act on") {
		t.Errorf("the panel should say there is no row:\n%s", body)
	}
}

// A long list must not paint past the height the overlay gave it — the budget
// is what keeps the panel inside the bottom band.
func TestSharedDialogRespectsItsRowBudget(t *testing.T) {
	var many []ctrlproto.SharedFileEntry
	for i := range 40 {
		many = append(many, sharedEntry("shr_"+string(rune('a'+i%26))+string(rune('0'+i/26)), "file.txt", "document", 10))
	}
	d := openShared(many)
	d.MaxRows = 12

	rows := d.Render(tui.Dark, 80)
	if len(rows) > d.MaxRows {
		t.Errorf("panel rendered %d rows against a %d-row budget", len(rows), d.MaxRows)
	}
	if !strings.Contains(sharedBody(d), "more below") {
		t.Errorf("a clipped list should advertise the remainder:\n%s", sharedBody(d))
	}
}
