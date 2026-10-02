package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func threadMsg(parent, kind, user, text string) Message {
	return Message{ID: "thread-message", ChatID: "opaque-thread", ChatKind: "thread",
		ParentChatID: parent, ParentChatKind: kind, UserID: user, Text: text}
}

func TestThreadInheritsCurrentParentPolicy(t *testing.T) {
	ctx := context.Background()
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	m := threadMsg("parent", "group", "9", "plain")
	wantRoute := func(want action) {
		t.Helper()
		if got := g.route(ctx, conn, m); got != want {
			t.Fatalf("route(%+v) = %v, want %v", m, got, want)
		}
	}
	wantRoute(actHandled)
	if err := adm.Approve("parent", ModeMention); err != nil {
		t.Fatal(err)
	}
	wantRoute(actHandled)
	m.Text = "@tervabot hello"
	wantRoute(actPrompt)
	if err := adm.Approve("parent", ModeAll); err != nil {
		t.Fatal(err)
	}
	m.Text = "plain"
	wantRoute(actPrompt)
	m.UserID, m.Text = "7", "/approve"
	wantRoute(actHandled)
	m.UserID, m.Text = "9", "plain"
	wantRoute(actHandled)
	m.Text = "@tervabot hello"
	wantRoute(actPrompt)
	m.UserID, m.Text = "7", "/approve all"
	wantRoute(actHandled)
	if err := adm.Approve("parent", ModeMention); err != nil {
		t.Fatal(err)
	}
	m.UserID, m.Text = "9", "plain"
	wantRoute(actHandled)
	if err := adm.Revoke("parent"); err != nil {
		t.Fatal(err)
	}
	m.Text = "@tervabot hello"
	wantRoute(actHandled)
	if _, ok := adm.Mode(m.ChatID); ok {
		t.Fatal("thread restriction became a standalone grant")
	}
}

func TestDMThreadOwnerAndDurableMute(t *testing.T) {
	ctx := context.Background()
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	m := threadMsg("owner-dm", "dm", "7", "hello")
	if got := g.route(ctx, conn, m); got != actPrompt {
		t.Fatalf("owner = %v", got)
	}
	for _, text := range []string{"hello", "/approve all", "/revoke", "/stop", "/status", "/start"} {
		foreign := m
		foreign.UserID, foreign.Text = "9", text
		if got := g.route(ctx, conn, foreign); got != actHandled {
			t.Fatalf("foreign %s = %v", text, got)
		}
	}
	m.Text = "/revoke"
	g.route(ctx, conn, m)
	g.admissions = LoadAdmissions(adm.path)
	m.Text = "hello"
	if got := g.route(ctx, conn, m); got != actHandled {
		t.Fatal("restart lost mute")
	}
	parent := Message{ChatID: "owner-dm", ChatKind: "dm", UserID: "7", Text: "hello"}
	if got := g.route(ctx, conn, parent); got != actPrompt {
		t.Fatal("thread revoke silenced parent")
	}
	m.Text = "/approve all"
	g.route(ctx, conn, m)
	m.Text = "hello"
	if got := g.route(ctx, conn, m); got != actPrompt {
		t.Fatal("reapproval failed")
	}
	if err := g.admissions.Revoke("owner-dm"); err != nil {
		t.Fatal(err)
	}
	if got := g.route(ctx, conn, m); got != actHandled {
		t.Fatal("DM parent revoke did not stop thread")
	}
}

func TestThreadContextFailsClosed(t *testing.T) {
	for _, modify := range []func(*Message){
		func(m *Message) { m.ChatID = "" },
		func(m *Message) { m.ParentChatID = "" },
		func(m *Message) { m.ParentChatKind = "" },
		func(m *Message) { m.ParentChatID = m.ChatID },
		func(m *Message) { m.ParentChatKind = "thread" },
		func(m *Message) { m.ParentChatKind = "unknown" },
		func(m *Message) { m.ChatKind = "dm" },
		func(m *Message) { m.ChatKind = "group" },
	} {
		conn := newFakeConnector(Capabilities{})
		g, adm := groupGate(t, conn)
		m := threadMsg("parent", "group", "7", "/approve all")
		modify(&m)
		_ = adm.Approve(m.ChatID, ModeAll)
		if got := g.route(context.Background(), conn, m); got != actHandled {
			t.Fatalf("invalid context routed: %+v", m)
		}
		m.Text = "hello"
		if got := g.route(context.Background(), conn, m); got != actHandled {
			t.Fatalf("invalid context gained access: %+v", m)
		}
	}
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	_ = adm.Approve("parent", ModeAll)
	m := threadMsg("parent", "group", "7", "hello")
	if got := g.route(context.Background(), conn, m); got != actPrompt {
		t.Fatal("valid control failed")
	}
	g.admissions = LoadAdmissions(adm.path)
	for _, candidate := range []Message{
		{ChatID: m.ChatID, ChatKind: "thread", UserID: "7", Text: "hello"},
		threadMsg("different-parent", "dm", "7", "hello"),
		threadMsg("parent", "dm", "7", "hello"),
	} {
		if got := g.route(context.Background(), conn, candidate); got != actHandled {
			t.Fatalf("binding bypassed: %+v", candidate)
		}
	}
	legacy := Message{ChatID: "legacy-thread", ChatKind: "thread", UserID: "9", Text: "hello", ScopeID: "parent"}
	if got := g.route(context.Background(), conn, legacy); got != actHandled {
		t.Fatal("scope granted permission")
	}
	_ = g.admissions.Approve(legacy.ChatID, ModeAll)
	if got := g.route(context.Background(), conn, legacy); got != actPrompt {
		t.Fatal("legacy explicit approval broke")
	}
}

func TestThreadRemoteApprovalReplayUsesParentPolicy(t *testing.T) {
	ctx := context.Background()
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	var replayed []Message
	g.onAdmitted = func(_ context.Context, msgs []Message) { replayed = append(replayed, msgs...) }
	m := threadMsg("parent", "group", "9", "plain")
	g.route(ctx, conn, m)
	m.ID, m.Text = "mention", "@tervabot hello"
	g.route(ctx, conn, m)
	_ = adm.Approve("parent", ModeMention)
	dm := Message{ChatID: "owner-dm", UserID: "7", Text: "/approve opaque-thread all"}
	g.route(ctx, conn, dm)
	if len(replayed) != 1 || replayed[0].ID != "mention" || replayed[0].ChatID != m.ChatID {
		t.Fatalf("replay = %+v", replayed)
	}
	replayed = nil
	_ = adm.Revoke("parent")
	g.route(ctx, conn, m)
	g.route(ctx, conn, dm)
	if len(replayed) != 0 {
		t.Fatal("approval replay widened revoked parent")
	}
	g.route(ctx, conn, Message{ChatID: "owner-dm", UserID: "7", Text: "/revoke parent"})
	if len(g.held.chats) != 0 {
		t.Fatal("parent revoke retained held child")
	}
}

func TestThreadBindingSaveFailureDoesNotGrant(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), "missing", "admissions.json")
	adm := LoadAdmissions(path)
	// A directory at the record path makes the atomic replacement fail.
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	g := &gate{pairing: Pairing{AllowedUserID: "7"}, admissions: adm}
	m := threadMsg("dm", "dm", "7", "hello")
	if got := g.route(context.Background(), newFakeConnector(Capabilities{}), m); got != actHandled {
		t.Fatal("failed persistence granted inheritance")
	}
	if adm.inherited(m.ChatID) {
		t.Fatal("failed save retained binding")
	}
}

func TestKnownDMThreadLearnsScopeForMembershipRemoval(t *testing.T) {
	for _, initialScope := range []string{"", "old-container"} {
		t.Run("initial="+initialScope, func(t *testing.T) {
			conn := newFakeConnector(Capabilities{})
			g, adm := groupGate(t, conn)
			m := threadMsg("owner-dm", "dm", "7", "hello")
			m.ScopeID = initialScope
			if g.route(t.Context(), conn, m) != actPrompt {
				t.Fatal("control thread was not admitted")
			}
			m.ScopeID = "container"
			if g.route(t.Context(), conn, m) != actPrompt {
				t.Fatal("thread with updated scope was not admitted")
			}
			adm = LoadAdmissions(adm.path)
			g.admissions = adm
			l := &Loop{Connector: conn, Admissions: adm, Info: func(string) {}, Warn: func(string) {}}
			l.onMembership(t.Context(), Membership{ChatID: "sibling", ChatKind: "group", ScopeID: "container", Change: "removed"})
			g.admissions = LoadAdmissions(adm.path)
			m.ScopeID = ""
			if g.route(t.Context(), conn, m) != actHandled {
				t.Fatal("container removal left the known thread admitted after scope discovery and restart")
			}
			other := threadMsg("owner-dm", "dm", "7", "hello")
			other.ChatID = "other-thread"
			if g.route(t.Context(), conn, other) != actPrompt {
				t.Fatal("scope discovery revoked an unrelated DM thread")
			}
		})
	}
}

func TestKnownThreadScopeSaveFailureRejectsMessage(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	m := threadMsg("owner-dm", "dm", "7", "hello")
	if g.route(t.Context(), conn, m) != actPrompt {
		t.Fatal("control thread was not admitted")
	}
	path := adm.path
	adm.path = testsupport.TempDir(t)
	m.ScopeID = "container"
	if g.route(t.Context(), conn, m) != actHandled {
		t.Fatal("failed scope persistence admitted the message")
	}
	adm.path = path
	if g.route(t.Context(), conn, m) != actPrompt {
		t.Fatal("scope persistence retry did not admit the message")
	}
	if _, err := LoadAdmissions(path).RevokeScope("container"); err != nil {
		t.Fatal(err)
	}
	g.admissions = LoadAdmissions(path)
	if g.route(t.Context(), conn, m) != actHandled {
		t.Fatal("scope persistence retry did not make the thread revocable")
	}
}

func TestScopeSaveFailureDoesNotBlockOwnerThreadRevocation(t *testing.T) {
	for _, kind := range []string{"dm", "group"} {
		t.Run(kind, func(t *testing.T) {
			conn := newFakeConnector(Capabilities{})
			g, adm := groupGate(t, conn)
			if err := adm.Approve("parent", ModeAll); err != nil {
				t.Fatal(err)
			}
			m := threadMsg("parent", kind, "7", "hello")
			if g.route(t.Context(), conn, m) != actPrompt {
				t.Fatal("control thread was not admitted")
			}
			active, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			l := &Loop{activeCancel: cancel, activeChatID: m.ChatID, activeParentID: m.ParentChatID,
				queue: []Message{m, {ChatID: "other"}}}
			g.onRevoked = l.cancelRevoked
			g.held.add(m)
			adm.path = testsupport.TempDir(t)
			m.ScopeID, m.Text = "new-container", "@tervabot /revoke"
			foreign := m
			foreign.UserID = "9"
			conflict := m
			conflict.ParentChatID = "different-parent"
			for _, invalid := range []Message{foreign, conflict} {
				g.route(t.Context(), conn, invalid)
				if active.Err() != nil || !g.allowedReply(m) || len(l.queue) != 2 || len(g.held.chats) != 1 {
					t.Fatal("failed scope persistence let invalid context revoke thread work")
				}
			}
			g.route(t.Context(), conn, m)
			if active.Err() != context.Canceled {
				t.Fatal("failed scope persistence blocked owner revocation cancellation")
			}
			if len(l.queue) != 1 || l.queue[0].ChatID != "other" || len(g.held.chats) != 0 {
				t.Fatal("failed scope persistence retained revoked queued or held content")
			}
			m.ScopeID, m.Text = "", "hello"
			if g.allowedReply(m) || g.route(t.Context(), conn, m) != actHandled {
				t.Fatal("failed scope persistence prevented runtime thread mute")
			}
			if sends := conn.sends(); len(sends) != 1 || !strings.Contains(sends[0].Text, "restart") {
				t.Fatal("failed scope persistence hid the revocation warning")
			}
		})
	}
}

func TestAutomaticThreadAssociationsAreBounded(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	adm := LoadAdmissions("")
	for i := 0; i < maxParentRecords; i++ {
		adm.chats[fmt.Sprint(i)] = admission{ParentID: fmt.Sprintf("parent-%d", i), ParentKind: "group"}
	}
	_ = adm.Approve("parent", ModeAll)
	before := len(adm.chats)
	g := &gate{pairing: Pairing{AllowedUserID: "7"}, admissions: adm}
	m := threadMsg("parent", "group", "9", "hello")
	if got := g.route(context.Background(), conn, m); got != actHandled {
		t.Fatal("overflow gained admission")
	}
	if len(adm.chats) != before {
		t.Fatal("overflow grew the store")
	}
	owner := threadMsg("dm", "dm", "7", "hello")
	owner.ChatID = "owner-thread"
	if got := g.route(context.Background(), conn, owner); got != actPrompt {
		t.Fatal("group members blocked a new owner DM thread")
	}
	_ = adm.Approve(m.ChatID, ModeAll)
	if got := g.route(context.Background(), conn, m); got != actPrompt {
		t.Fatal("explicit record could not bind at capacity")
	}
}

func TestThreadAssociationQuotaIsPerParent(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	adm := LoadAdmissions("")
	_ = adm.Approve("parent", ModeAll)
	_ = adm.Approve("other-parent", ModeAll)
	g := &gate{pairing: Pairing{AllowedUserID: "7"}, admissions: adm}
	for i := 0; i < maxParentRecordsPerChat; i++ {
		m := threadMsg("parent", "group", "9", "hello")
		m.ChatID = fmt.Sprint(i)
		if got := g.route(t.Context(), conn, m); got != actPrompt {
			t.Fatal("control thread was not admitted")
		}
	}
	m := threadMsg("parent", "group", "9", "overflow")
	if got := g.route(t.Context(), conn, m); got != actHandled {
		t.Fatal("one parent exceeded its association quota")
	}
	m.ParentChatID = "other-parent"
	if got := g.route(t.Context(), conn, m); got != actPrompt {
		t.Fatal("one parent consumed another parent's thread slots")
	}
}

func TestStandaloneRevocationsDoNotConsumeThreadSlots(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	adm := LoadAdmissions("")
	for i := 0; i < maxParentRecords; i++ {
		if err := adm.Revoke(fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
	g := &gate{pairing: Pairing{AllowedUserID: "7"}, admissions: adm}
	m := threadMsg("dm", "dm", "7", "hello")
	if got := g.route(context.Background(), conn, m); got != actPrompt {
		t.Fatal("standalone mutes blocked an owner DM thread")
	}
}

func TestUnapprovedMembershipChurnCannotWriteAdmissions(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	path := filepath.Join(testsupport.TempDir(t), "admissions.json")
	adm := LoadAdmissions(path)
	l := &Loop{Admissions: adm, Connector: conn, Info: func(string) {}}
	for i := 0; i < maxParentRecords+1; i++ {
		l.onMembership(t.Context(), Membership{ChatID: fmt.Sprint(i), ChatKind: "group", Change: "removed"})
	}
	if len(adm.chats) != 0 {
		t.Fatal("unapproved membership events grew the store")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("unapproved membership events wrote a policy file")
	}
	g := &gate{pairing: Pairing{AllowedUserID: "7"}, admissions: adm}
	m := threadMsg("owner-dm", "dm", "7", "hello")
	if got := g.route(t.Context(), conn, m); got != actPrompt {
		t.Fatal("control DM thread was not admitted")
	}
	l.onMembership(t.Context(), Membership{ChatID: "owner-dm", ChatKind: "dm", Change: "removed"})
	g.admissions = LoadAdmissions(path)
	if g.allowedReply(m) {
		t.Fatal("removal failed to persist denial for a known DM-thread parent")
	}
}

func TestFailedRevocationStillStopsThreadWork(t *testing.T) {
	for _, target := range []string{"parent", "opaque-thread"} {
		t.Run(target, func(t *testing.T) {
			conn := newFakeConnector(Capabilities{})
			g, adm := groupGate(t, conn)
			if err := adm.Approve("parent", ModeAll); err != nil {
				t.Fatal(err)
			}
			m := threadMsg("parent", "group", "9", "hello")
			if got := g.route(t.Context(), conn, m); got != actPrompt {
				t.Fatal("control thread was not admitted")
			}
			active, cancel := context.WithCancel(t.Context())
			t.Cleanup(cancel)
			l := &Loop{activeCancel: cancel, activeChatID: m.ChatID,
				activeParentID: m.ParentChatID, queue: []Message{m, {ChatID: "other"}}}
			g.onRevoked = l.cancelRevoked
			g.held.add(m)
			// Atomic replacement of a directory must fail on every platform.
			adm.path = testsupport.TempDir(t)
			g.route(t.Context(), conn, Message{ChatID: "dm", UserID: "7", Text: "/revoke " + target})
			if active.Err() != context.Canceled {
				t.Fatal("failed persistence left the thread active")
			}
			if len(l.queue) != 1 || l.queue[0].ChatID != "other" || len(g.held.chats) != 0 {
				t.Fatal("failed persistence retained revoked queued or held content")
			}
			if g.allowedReply(m) || g.route(t.Context(), conn, m) != actHandled {
				t.Fatal("failed persistence restored access")
			}
			if sends := conn.sends(); len(sends) != 1 || !strings.Contains(sends[0].Text, "restart") {
				t.Fatal("revocation did not explain the persistence failure")
			}
		})
	}
}

func TestScopeReapprovalRestoresInheritanceAndKeepsThreadRestrictions(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	if err := adm.ApproveScoped("parent", ModeAll, "container"); err != nil {
		t.Fatal(err)
	}
	var threads []Message
	for _, policy := range []string{"inherited", "mention", "muted"} {
		m := threadMsg("parent", "group", "7", "hello")
		m.ChatID, m.ScopeID = policy, "container"
		if got := g.route(t.Context(), conn, m); got != actPrompt {
			t.Fatal("control thread was not admitted")
		}
		if policy == "mention" {
			m.Text = "/approve"
			g.route(t.Context(), conn, m)
		} else if policy == "muted" {
			m.Text = "/revoke"
			g.route(t.Context(), conn, m)
		}
		m.UserID, m.Text = "9", "plain"
		threads = append(threads, m)
	}
	if _, err := adm.RevokeScope("container"); err != nil {
		t.Fatal(err)
	}
	g.admissions = LoadAdmissions(adm.path)
	for _, m := range threads {
		if g.allowedReply(m) {
			t.Fatal("scope removal left inherited access")
		}
	}
	g.route(t.Context(), conn, Message{ChatID: "dm", UserID: "7", Text: "/approve parent all"})
	g.admissions = LoadAdmissions(adm.path)
	for _, m := range threads {
		want := actHandled
		if m.ChatID == "inherited" {
			want = actPrompt
		}
		if got := g.route(t.Context(), conn, m); got != want {
			t.Fatalf("reapproved %s = %v, want %v", m.ChatID, got, want)
		}
		m.Text = "@tervabot hello"
		want = actPrompt
		if m.ChatID == "muted" {
			want = actHandled
		}
		if got := g.route(t.Context(), conn, m); got != want {
			t.Fatalf("reapproved mention in %s = %v, want %v", m.ChatID, got, want)
		}
	}
}

func TestParentApprovalReleasesHeldThreadsUnderCurrentMode(t *testing.T) {
	ctx := context.Background()
	conn := newFakeConnector(Capabilities{})
	g, _ := groupGate(t, conn)
	var got []Message
	g.onAdmitted = func(_ context.Context, msgs []Message) { got = append(got, msgs...) }
	m := threadMsg("parent", "group", "9", "plain")
	g.route(ctx, conn, m)
	m.ID, m.Text = "mention", "@tervabot stale"
	g.route(ctx, conn, m)
	g.heldEdited(MessageEdited{ChatID: m.ChatID, ID: m.ID, Text: "@tervabot updated"})
	g.route(ctx, conn, Message{ChatID: "parent", ChatKind: "group", UserID: "7", Text: "/approve"})
	if len(got) != 1 || got[0].Text != "@tervabot updated" || got[0].ChatID != m.ChatID {
		t.Fatalf("parent replay = %+v", got)
	}
}

func TestRemoteMuteBeforeParentDiscoveryAndScopeRemoval(t *testing.T) {
	ctx := context.Background()
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	g.route(ctx, conn, Message{ChatID: "dm", UserID: "7", Text: "/revoke opaque-thread"})
	m := threadMsg("dm", "dm", "7", "hello")
	m.ScopeID = "container"
	if got := g.route(ctx, conn, m); got != actHandled {
		t.Fatal("parent discovery undid prior mute")
	}
	g.route(ctx, conn, Message{ChatID: "dm", UserID: "7", Text: "/approve opaque-thread all"})
	if got := g.route(ctx, conn, m); got != actPrompt {
		t.Fatal("remote reapproval failed")
	}
	if _, err := adm.RevokeScope("container"); err != nil {
		t.Fatal(err)
	}
	g.admissions = LoadAdmissions(adm.path)
	if got := g.route(ctx, conn, m); got != actHandled {
		t.Fatal("container removal lost mute on restart")
	}
	other := threadMsg("dm", "dm", "7", "hello")
	other.ChatID = "outside-container"
	if got := g.route(ctx, conn, other); got != actPrompt {
		t.Fatal("container removal silenced an unrelated owner DM thread")
	}
}

func TestScopeRemovalDoesNotRevokeParentInAnotherScope(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	if err := adm.ApproveScoped("parent", ModeAll, "other-container"); err != nil {
		t.Fatal(err)
	}
	m := threadMsg("parent", "group", "7", "hello")
	m.ScopeID = "removed-container"
	if got := g.route(t.Context(), conn, m); got != actPrompt {
		t.Fatal("control thread was not admitted")
	}
	if _, err := adm.RevokeScope(m.ScopeID); err != nil {
		t.Fatal(err)
	}
	g.admissions = LoadAdmissions(adm.path)
	if g.allowedReply(m) {
		t.Fatal("scoped thread retained access")
	}
	other := m
	other.ChatID, other.ScopeID = "outside-container", "other-container"
	if got := g.route(t.Context(), conn, other); got != actPrompt {
		t.Fatal("scope removal revoked a parent from another container")
	}
}

func TestLegacyThreadApprovalBecomesAParentRestrictionOnUpgrade(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	m := Message{ChatID: "opaque-thread", ChatKind: "thread", UserID: "9", Text: "hello"}
	if err := adm.Approve(m.ChatID, ModeAll); err != nil {
		t.Fatal(err)
	}
	if got := g.route(t.Context(), conn, m); got != actPrompt {
		t.Fatal("legacy explicit approval was not admitted")
	}
	m.ParentChatID, m.ParentChatKind = "parent", "group"
	if got := g.route(t.Context(), conn, m); got != actHandled {
		t.Fatal("legacy approval bypassed the newly reported parent")
	}
	g.admissions = LoadAdmissions(adm.path)
	g.route(t.Context(), conn, Message{ChatID: "dm", UserID: "7", Text: "/approve parent"})
	if got := g.route(t.Context(), conn, m); got != actHandled {
		t.Fatal("legacy all-mode approval widened the parent")
	}
	m.Text = "@tervabot hello"
	if got := g.route(t.Context(), conn, m); got != actPrompt {
		t.Fatal("upgraded thread could not inherit an approved parent")
	}
	m.ParentChatID, m.ParentChatKind = "", ""
	if got := g.route(t.Context(), conn, m); got != actHandled {
		t.Fatal("downgrade to legacy metadata escaped the parent binding")
	}
}

func TestThreadPolicyStoreFailsClosedOnOlderHosts(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	m := threadMsg("dm", "dm", "7", "hello")
	if g.route(context.Background(), conn, m) != actPrompt {
		t.Fatal("control not admitted")
	}
	if err := adm.Revoke(m.ChatID); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(adm.path)
	if err != nil {
		t.Fatal(err)
	}
	var oldObjects map[string]struct {
		Mode  string
		Scope string
	}
	if err := json.Unmarshal(data, &oldObjects); err == nil {
		t.Fatal("old object-map host could accept the policy file")
	}
	var oldStrings map[string]string
	if err := json.Unmarshal(data, &oldStrings); err == nil {
		t.Fatal("old string-map host could accept the policy file")
	}
	if err := os.WriteFile(adm.path, []byte(`{"version":99,"chats":{"untrusted":{"mode":"all"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadAdmissions(adm.path).Mode("untrusted"); ok {
		t.Fatal("unknown store version granted access")
	}
}

func TestUnapprovedSendersCannotFillThreadAssociationBudget(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	adm := LoadAdmissions("")
	g := &gate{pairing: Pairing{AllowedUserID: "7"}, admissions: adm}
	for _, kind := range []string{"group", "dm"} {
		for i := 0; i < 100; i++ {
			m := threadMsg("unapproved-parent", kind, "9", "hello")
			m.ChatID = fmt.Sprintf("%s-%d", kind, i)
			if got := g.route(context.Background(), conn, m); got != actHandled {
				t.Fatal("unapproved sender gained access")
			}
		}
	}
	if len(adm.chats) != 0 {
		t.Fatal("unapproved sender wrote persistent associations")
	}
	if len(g.held.chats) > heldMaxChats {
		t.Fatal("unapproved content exceeded held bounds")
	}
}
