package chat

import (
	"context"
	"path/filepath"
	"strings"
	"terva.sh/terva/packages/testsupport"
	"testing"
)

// groupGate builds a v2 gate over the fake connector with a paired
// owner and a disk-backed admissions store.
func groupGate(t *testing.T, conn *fakeConnector) (*gate, *Admissions) {
	t.Helper()
	adm := LoadAdmissions(filepath.Join(testsupport.TempDir(t), "admissions.json"))
	g := &gate{
		pairing:     Pairing{AllowedUserID: "7"},
		admissions:  adm,
		botUsername: "tervabot",
		helpText:    "help!",
		pairedText:  "paired with @%s.",
	}
	return g, adm
}

func groupMsg(user, text string) Message {
	return Message{ID: "m1", ChatID: "g100", ChatKind: "group", ChatTitle: "ops",
		UserID: user, Username: "u" + user, Text: text}
}

// TestGateGroupSilentByDefault: an unapproved group produces neither
// turns nor replies, from anyone — including the owner's plain text.
func TestGateGroupSilentByDefault(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, _ := groupGate(t, conn)

	for _, m := range []Message{
		groupMsg("9", "hello bot"),
		groupMsg("7", "just chatting"),
		groupMsg("9", "@tervabot do something"), // mention ≠ admission
		groupMsg("9", "/approve"),               // non-owner cannot admit...
	} {
		if act := g.route(context.Background(), conn, m); act != actHandled {
			t.Errorf("route(%q from %s) = %v, want actHandled", m.Text, m.UserID, act)
		}
	}
	// ...and the only reply in all of that is the owner-only notice
	// for the /approve attempt.
	sends := conn.sends()
	if len(sends) != 1 || !strings.Contains(sends[0].Text, "owner") {
		t.Errorf("sends = %v, want just the owner-only notice", sends)
	}
}

// TestGateGroupApproveFlow: the owner admits in-chat; mention mode
// gates plain text but routes mentions (entity or @username); /revoke
// silences again.
func TestGateGroupApproveFlow(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	ctx := context.Background()

	if act := g.route(ctx, conn, groupMsg("7", "/approve")); act != actHandled {
		t.Fatalf("approve route = %v", act)
	}
	if mode, ok := adm.Mode("g100"); !ok || mode != ModeMention {
		t.Fatalf("admission = %q,%v want mention", mode, ok)
	}
	if sends := conn.sends(); len(sends) != 1 || !strings.Contains(sends[0].Text, "approved") {
		t.Fatalf("ack = %v", sends)
	}

	// Plain text: gated. Entity mention: routed. @username text: routed.
	if act := g.route(ctx, conn, groupMsg("9", "what's the weather")); act != actHandled {
		t.Errorf("unmentioned = %v, want actHandled", act)
	}
	withEntity := groupMsg("9", "hey bot, weather?")
	withEntity.Entities = []Entity{{Kind: "bot_mention", Offset: 0, Length: 3}}
	if act := g.route(ctx, conn, withEntity); act != actPrompt {
		t.Errorf("entity mention = %v, want actPrompt", act)
	}
	if act := g.route(ctx, conn, groupMsg("9", "@TervaBot weather?")); act != actPrompt {
		t.Errorf("@username mention = %v, want actPrompt", act)
	}

	if act := g.route(ctx, conn, groupMsg("7", "/revoke")); act != actHandled {
		t.Errorf("revoke route = %v", act)
	}
	if _, ok := adm.Mode("g100"); ok {
		t.Error("revoke did not clear the admission")
	}
	if act := g.route(ctx, conn, groupMsg("9", "@tervabot weather?")); act != actHandled {
		t.Errorf("post-revoke mention = %v, want silence", act)
	}
}

// TestGateGroupApproveAll: `all` mode routes unmentioned messages.
func TestGateGroupApproveAll(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	ctx := context.Background()

	if act := g.route(ctx, conn, groupMsg("7", "/approve all")); act != actHandled {
		t.Fatalf("approve all route = %v", act)
	}
	if mode, _ := adm.Mode("g100"); mode != ModeAll {
		t.Fatalf("mode = %q, want all", mode)
	}
	if act := g.route(ctx, conn, groupMsg("9", "no mention here")); act != actPrompt {
		t.Errorf("all-mode plain text = %v, want actPrompt", act)
	}
}

// TestGateGroupLeadingMentionCommand: "@bot /approve" admits — on
// mention-gated services the owner can only reach the bot by
// addressing it. Entity-located and plain-text forms both work.
func TestGateGroupLeadingMentionCommand(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	ctx := context.Background()

	m := groupMsg("7", "<@bot123> /approve")
	m.Entities = []Entity{{Kind: "bot_mention", Offset: 0, Length: 9}}
	if act := g.route(ctx, conn, m); act != actHandled {
		t.Fatalf("entity-prefixed approve = %v", act)
	}
	if _, ok := adm.Mode("g100"); !ok {
		t.Error("entity-prefixed /approve did not admit")
	}
	_ = adm.Revoke("g100")

	if act := g.route(ctx, conn, groupMsg("7", "@tervabot /approve all")); act != actHandled {
		t.Fatalf("username-prefixed approve = %v", act)
	}
	if mode, ok := adm.Mode("g100"); !ok || mode != ModeAll {
		t.Errorf("username-prefixed /approve all = %q,%v", mode, ok)
	}
}

// TestGateGroupAuthority: reach ≠ authority — non-owner members can
// prompt in approved chats but never command.
func TestGateGroupAuthority(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, _ := groupGate(t, conn)
	ctx := context.Background()
	_ = g.route(ctx, conn, groupMsg("7", "/approve all"))

	if act := g.route(ctx, conn, groupMsg("9", "/stop")); act != actHandled {
		t.Errorf("non-owner /stop = %v, want refused", act)
	}
	if act := g.route(ctx, conn, groupMsg("9", "/status")); act != actHandled {
		t.Errorf("non-owner /status = %v, want refused", act)
	}
	// Plain "stop" from a non-owner is conversation, not authority.
	if act := g.route(ctx, conn, groupMsg("9", "stop")); act != actPrompt {
		t.Errorf("non-owner plain stop = %v, want actPrompt", act)
	}
	if act := g.route(ctx, conn, groupMsg("7", "/stop")); act != actStop {
		t.Errorf("owner /stop = %v, want actStop", act)
	}
	if act := g.route(ctx, conn, groupMsg("7", "/status")); act != actStatus {
		t.Errorf("owner /status = %v, want actStatus", act)
	}
}

// TestGateGroupUnpaired: with no owner at all, groups are silent even
// for /start — pairing happens in a DM, never in public.
func TestGateGroupUnpaired(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	adm := LoadAdmissions("")
	g := &gate{pairing: Pairing{}, admissions: adm, helpText: "h", pairedText: "p %s"}
	if act := g.route(context.Background(), conn, groupMsg("9", "/start")); act != actHandled {
		t.Errorf("group /start = %v, want silence", act)
	}
	if g.pairing.AllowedUserID != "" {
		t.Error("group /start must not claim the bot")
	}
	if len(conn.sends()) != 0 {
		t.Errorf("sends = %v, want none", conn.sends())
	}
}

// TestGateDMApproveByID: the owner can admit and revoke a chat
// remotely from the DM.
func TestGateDMApproveByID(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	ctx := context.Background()

	dm := func(text string) Message {
		return Message{ID: "m2", ChatID: "7", ChatKind: "dm", UserID: "7", Username: "u7", Text: text}
	}
	if act := g.route(ctx, conn, dm("/approve g200 all")); act != actHandled {
		t.Fatalf("dm approve = %v", act)
	}
	if mode, ok := adm.Mode("g200"); !ok || mode != ModeAll {
		t.Errorf("dm-approved mode = %q,%v", mode, ok)
	}
	if act := g.route(ctx, conn, dm("/revoke g200")); act != actHandled {
		t.Fatalf("dm revoke = %v", act)
	}
	if _, ok := adm.Mode("g200"); ok {
		t.Error("dm /revoke did not clear")
	}
	// Bare /approve gets usage, not a crash and not an admission.
	if act := g.route(ctx, conn, dm("/approve")); act != actHandled {
		t.Errorf("bare /approve = %v", act)
	}
	sends := conn.sends()
	if !strings.Contains(sends[len(sends)-1].Text, "usage") {
		t.Errorf("bare /approve reply = %v", sends[len(sends)-1])
	}
}

// TestAdmissionsPersistence: approvals survive a reload.
func TestAdmissionsPersistence(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "admissions.json")
	a := LoadAdmissions(path)
	if err := a.Approve("g1", ModeAll); err != nil {
		t.Fatal(err)
	}
	if err := a.Approve("g2", "bogus-mode"); err != nil { // normalizes to mention
		t.Fatal(err)
	}
	b := LoadAdmissions(path)
	if mode, ok := b.Mode("g1"); !ok || mode != ModeAll {
		t.Errorf("g1 = %q,%v", mode, ok)
	}
	if mode, ok := b.Mode("g2"); !ok || mode != ModeMention {
		t.Errorf("g2 = %q,%v", mode, ok)
	}
	if err := b.Revoke("g1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadAdmissions(path).Mode("g1"); ok {
		t.Error("revocation did not persist")
	}
}
