package tenant

import (
	"strings"
	"testing"
)

// The id reaches a Linux user name via `User=terva-%i` in the tenant unit
// template, and a login name is capped at 31 characters — measured on systemd
// 255, where 32 is refused. Before this was known the id was 34 characters and
// EVERY systemd-contained tenant failed to start, with "Failed to spawn 'start'
// task: Invalid argument" and nothing an operator could act on.
//
// So the id's length is a constraint, not a preference, and lengthening it is a
// change that breaks a boundary rather than a cosmetic one.
func TestATenantIDFitsInsideALinuxUserName(t *testing.T) {
	id, err := newID()
	if err != nil {
		t.Fatal(err)
	}
	// The longest prefix the shipped unit template puts in front of it.
	const prefix = "terva-"
	if got := len(prefix + id); got > MaxUserNameLen {
		t.Errorf("%q is %d characters, and a Linux login name accepts %d — every systemd-contained tenant would fail to start",
			prefix+id, got, MaxUserNameLen)
	}
	if !ValidID(id) {
		t.Errorf("newID minted %q, which ValidID rejects", id)
	}
	if !strings.HasPrefix(id, "t-") {
		t.Errorf("id %q is not recognisable in a directory listing", id)
	}
}
