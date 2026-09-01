package chat

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
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

// TestGateGroupHoldsAndReplaysOnApprove: what an un-admitted chat said is
// held, and approval answers the part that fits the mode — the mention
// that raised the question, not the chatter around it.
func TestGateGroupHoldsAndReplaysOnApprove(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, _ := groupGate(t, conn)
	ctx := context.Background()
	var released []Message
	g.onAdmitted = func(_ context.Context, msgs []Message) { released = append(released, msgs...) }

	plain := groupMsg("9", "just chatting")
	plain.ID = "m1"
	mention := groupMsg("9", "@tervabot what's the plan?")
	mention.ID = "m2"
	mention.Files = []FileAttachment{{Path: "/staged/gone"}}
	cmd := groupMsg("9", "/status")
	cmd.ID = "m3"
	for _, m := range []Message{plain, mention, cmd} {
		if act := g.route(ctx, conn, m); act != actHandled {
			t.Fatalf("route(%q) before approval = %v, want actHandled", m.Text, act)
		}
	}
	if len(released) != 0 {
		t.Fatalf("released before approval: %+v", released)
	}

	// Mention mode: only the mention comes back — without the file the
	// receive path cleaned up when the gate declined.
	if act := g.route(ctx, conn, groupMsg("7", "/approve")); act != actHandled {
		t.Fatalf("approve route = %v", act)
	}
	if len(released) != 1 || released[0].ID != "m2" || released[0].Files != nil {
		t.Fatalf("released = %+v, want just the mention, files stripped", released)
	}
	sends := conn.sends()
	if last := sends[len(sends)-1].Text; !strings.Contains(last, "starting with the message that was waiting") {
		t.Errorf("confirmation = %q, want it to say what it is answering", last)
	}

	// Held content has one chance: the plain message did not fit and is
	// gone, not kept for a later `all`.
	released = nil
	_ = g.route(ctx, conn, groupMsg("7", "/revoke"))
	_ = g.route(ctx, conn, groupMsg("7", "/approve all"))
	if len(released) != 0 {
		t.Errorf("a second approval replayed %+v, want nothing", released)
	}
}

// TestGateGroupApproveAllReplaysEverything: `all` replays what was held
// in arrival order, mentioned or not.
func TestGateGroupApproveAllReplaysEverything(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, _ := groupGate(t, conn)
	ctx := context.Background()
	var released []Message
	g.onAdmitted = func(_ context.Context, msgs []Message) { released = append(released, msgs...) }

	for i, text := range []string{"first", "@tervabot second", "third"} {
		m := groupMsg("9", text)
		m.ID = "m" + string(rune('1'+i))
		g.route(ctx, conn, m)
	}
	// Approved by id from the owner's DM — the other approval path.
	dm := Message{ID: "d1", ChatID: "700", ChatKind: "dm", UserID: "7", Username: "u7", Text: "/approve g100 all"}
	if act := g.route(ctx, conn, dm); act != actHandled {
		t.Fatalf("DM approve route = %v", act)
	}
	if len(released) != 3 || released[0].ID != "m1" || released[1].ID != "m2" || released[2].ID != "m3" {
		t.Fatalf("released = %+v, want all three in order", released)
	}
	sends := conn.sends()
	if last := sends[len(sends)-1].Text; !strings.Contains(last, "starting with the 3 messages that were waiting") {
		t.Errorf("confirmation = %q", last)
	}
}

// TestGateGroupHeldBounds: the buffer is bounded in count and age, and a
// revoke or an edit/delete of a held message is honoured before replay.
func TestGateGroupHeldBounds(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, _ := groupGate(t, conn)
	ctx := context.Background()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	g.held.now = func() time.Time { return now }
	var released []Message
	g.onAdmitted = func(_ context.Context, msgs []Message) { released = msgs }

	// Two early messages, then the clock passes heldMaxAge, then heldMax+2
	// more: the early ones expire, and only the newest heldMax survive.
	for i := 0; i < 2; i++ {
		m := groupMsg("9", fmt.Sprintf("early %d", i))
		m.ID = fmt.Sprintf("e%d", i)
		g.route(ctx, conn, m)
	}
	now = now.Add(heldMaxAge + time.Second)
	for i := 0; i < heldMax+2; i++ {
		m := groupMsg("9", fmt.Sprintf("late %d", i))
		m.ID = fmt.Sprintf("l%d", i)
		g.route(ctx, conn, m)
	}
	// An edit rewrites a held message; a delete withdraws one.
	if !g.heldEdited(MessageEdited{ChatID: "g100", ID: "l6", Text: "late 6, edited"}) {
		t.Fatal("edit of a held message not applied")
	}
	if !g.heldDeleted(MessageDeleted{ChatID: "g100", ID: "l5"}) {
		t.Fatal("delete of a held message not applied")
	}
	g.route(ctx, conn, groupMsg("7", "/approve all"))
	var got []string
	for _, m := range released {
		got = append(got, m.ID+":"+m.Text)
	}
	want := []string{"l2:late 2", "l3:late 3", "l4:late 4", "l6:late 6, edited"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("released = %v, want %v", got, want)
	}

	// Revoke drops what is waiting.
	released = nil
	_ = g.route(ctx, conn, groupMsg("7", "/revoke"))
	g.route(ctx, conn, groupMsg("9", "@tervabot still there?"))
	_ = g.route(ctx, conn, groupMsg("7", "/revoke"))
	_ = g.route(ctx, conn, groupMsg("7", "/approve"))
	if len(released) != 0 {
		t.Errorf("revoke kept %+v waiting", released)
	}
}

// TestGateGroupHeldChatCap: a bot sitting in many un-admitted chats holds
// for the most recent heldMaxChats of them and forgets the stalest.
func TestGateGroupHeldChatCap(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, _ := groupGate(t, conn)
	ctx := context.Background()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	g.held.now = func() time.Time { return now }
	for i := 0; i <= heldMaxChats; i++ {
		m := groupMsg("9", "@tervabot hi")
		m.ID = fmt.Sprintf("m%d", i)
		m.ChatID = fmt.Sprintf("g%d", i)
		now = now.Add(time.Second)
		g.route(ctx, conn, m)
	}
	g.mu.Lock()
	n, _, hasFirst := len(g.held.chats), 0, g.held.chats["g0"] != nil
	_, hasLast := g.held.chats[fmt.Sprintf("g%d", heldMaxChats)]
	g.mu.Unlock()
	if n != heldMaxChats || hasFirst || !hasLast {
		t.Errorf("held chats = %d (first kept %v, last kept %v), want %d with the stalest evicted", n, hasFirst, hasLast, heldMaxChats)
	}
}
