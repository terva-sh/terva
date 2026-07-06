package chat

import (
	"context"
	"os"
	"path/filepath"
	"terva.sh/terva/packages/testsupport"
	"testing"
)

func TestAdmissionsScopedApproveAndRevokeScope(t *testing.T) {
	a := LoadAdmissions(filepath.Join(testsupport.TempDir(t), "adm.json"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}

	must(a.ApproveScoped("c1", ModeMention, "g1"))
	must(a.ApproveScoped("c2", ModeAll, "g1"))
	must(a.ApproveScoped("c3", ModeMention, "g2"))
	must(a.Approve("c4", ModeMention)) // scopeless, e.g. DM-approved by id

	// A "" scope matches nothing — scopeless chats are never bulk-revoked.
	if n, _ := a.RevokeScope(""); n != 0 {
		t.Fatalf(`RevokeScope("") revoked %d, want 0`, n)
	}

	// Kicked from g1: both g1 channels go; g2 and the scopeless chat stay.
	n, err := a.RevokeScope("g1")
	must(err)
	if n != 2 {
		t.Fatalf("RevokeScope(g1) revoked %d, want 2", n)
	}
	for _, gone := range []string{"c1", "c2"} {
		if _, ok := a.Mode(gone); ok {
			t.Errorf("%s still approved after its guild was revoked", gone)
		}
	}
	if mode, ok := a.Mode("c3"); !ok || mode != ModeMention {
		t.Errorf("c3 (other guild) = %q,%v; want it left approved", mode, ok)
	}
	if _, ok := a.Mode("c4"); !ok {
		t.Error("c4 (scopeless) was revoked by an unrelated guild kick")
	}
}

func TestAdmissionsLegacyFormatLoadsAndUpgrades(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "adm.json")
	// A pre-scope store is a bare {chatID: mode} map; it must still load.
	if err := os.WriteFile(path, []byte(`{"c1":"mention","c2":"all"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	a := LoadAdmissions(path)
	if mode, ok := a.Mode("c1"); !ok || mode != ModeMention {
		t.Fatalf("legacy c1 = %q,%v; want mention", mode, ok)
	}
	if mode, ok := a.Mode("c2"); !ok || mode != ModeAll {
		t.Fatalf("legacy c2 = %q,%v; want all", mode, ok)
	}
	// The next save upgrades the file to the scoped object format; a reload sees
	// the same modes and can now carry (and revoke by) scope.
	if err := a.ApproveScoped("c3", ModeMention, "g9"); err != nil {
		t.Fatal(err)
	}
	reload := LoadAdmissions(path)
	if mode, ok := reload.Mode("c1"); !ok || mode != ModeMention {
		t.Errorf("after upgrade c1 = %q,%v; want mention preserved", mode, ok)
	}
	if n, _ := reload.RevokeScope("g9"); n != 1 {
		t.Errorf("scoped c3 not revocable by scope after reload: n=%d, want 1", n)
	}
}

// TestOnMembershipGuildRemovalRevokesSiblings pins the wiring: a "removed"
// membership event carrying a guild scope drops every channel approved under
// that guild, not just the one the event happens to name.
func TestOnMembershipGuildRemovalRevokesSiblings(t *testing.T) {
	adm := LoadAdmissions("")
	_ = adm.ApproveScoped("chanA", ModeMention, "g1") // system channel of g1
	_ = adm.ApproveScoped("chanB", ModeAll, "g1")     // another g1 channel
	_ = adm.ApproveScoped("chanC", ModeMention, "g2") // a different guild

	l := &Loop{Connector: newFakeConnector(Capabilities{}), Admissions: adm, Info: func(string) {}, Warn: func(string) {}}
	l.mu.Lock()
	l.ownerID = "7"
	l.pairedChatID = "100"
	l.mu.Unlock()

	// Kicked from g1 — the event names the system channel but scopes the guild.
	l.onMembership(context.Background(), Membership{ChatID: "chanA", ChatKind: "group", ScopeID: "g1", Change: "removed"})

	for _, gone := range []string{"chanA", "chanB"} {
		if _, ok := adm.Mode(gone); ok {
			t.Errorf("%s survived the g1 kick; sibling channels must be revoked too", gone)
		}
	}
	if _, ok := adm.Mode("chanC"); !ok {
		t.Error("chanC (guild g2) was revoked by a g1 kick")
	}
}
