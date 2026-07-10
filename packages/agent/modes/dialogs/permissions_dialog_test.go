package dialogs

import (
	"testing"

	"terva.sh/terva/packages/tui"
)

func TestPermissionsDialogRevokeSelectedGrant(t *testing.T) {
	d := NewPermissionsDialog()
	d.Open([]string{"info"}, []PermGrant{{Tool: "edit"}, {Tool: "bash"}})

	// Cursor starts on the first grant; move to the second.
	d.HandleKey(tui.Key{Kind: tui.KeyDown})
	act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'r'})
	if !act.Revoke || act.Grant.Tool != "bash" {
		t.Fatalf("r should revoke the selected grant; got %+v", act)
	}
	// del is an alias for revoke.
	act = d.HandleKey(tui.Key{Kind: tui.KeyDelete})
	if !act.Revoke || act.Grant.Tool != "bash" {
		t.Fatalf("del should revoke the selected grant; got %+v", act)
	}
}

func TestPermissionsDialogRevokeAllowAllGrant(t *testing.T) {
	d := NewPermissionsDialog()
	d.Open([]string{"info"}, []PermGrant{{AllowAll: true}, {Tool: "edit"}})

	act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'r'})
	if !act.Revoke || !act.Grant.AllowAll {
		t.Fatalf("revoking the first entry should target the allowAll grant; got %+v", act)
	}
}

func TestPermissionsDialogClearAllAndClose(t *testing.T) {
	d := NewPermissionsDialog()
	d.Open([]string{"info"}, []PermGrant{{Tool: "edit"}})

	if act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'R'}); !act.ClearAll {
		t.Fatalf("R should request clear-all; got %+v", act)
	}
	if act := d.HandleKey(tui.Key{Kind: tui.KeyEsc}); !act.Close || d.Active() {
		t.Fatalf("esc should close; got %+v active=%v", act, d.Active())
	}
}

func TestPermissionsDialogClearAllNoopWithoutGrants(t *testing.T) {
	d := NewPermissionsDialog()
	d.Open([]string{"info"}, nil)
	if act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'R'}); act.ClearAll {
		t.Error("R with no grants should be a no-op, not a clear-all")
	}
	// Nothing to revoke either.
	if act := d.HandleKey(tui.Key{Kind: tui.KeyRune, Rune: 'r'}); act.Revoke {
		t.Error("r with no grants should be a no-op")
	}
}

func TestPermissionsDialogRefreshClampsCursor(t *testing.T) {
	d := NewPermissionsDialog()
	d.Open([]string{"info"}, []PermGrant{{Tool: "edit"}, {Tool: "bash"}})
	d.HandleKey(tui.Key{Kind: tui.KeyDown}) // cursor -> 1
	if d.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", d.cursor)
	}
	// Revoking bash leaves one grant; the cursor must clamp back to 0.
	d.Refresh([]string{"info"}, []PermGrant{{Tool: "edit"}})
	if d.cursor != 0 {
		t.Errorf("cursor = %d after refresh, want 0 (clamped)", d.cursor)
	}
	// Refreshing to an empty list keeps the cursor in bounds.
	d.Refresh([]string{"info"}, nil)
	if d.cursor != 0 {
		t.Errorf("cursor = %d for empty grants, want 0", d.cursor)
	}
}
