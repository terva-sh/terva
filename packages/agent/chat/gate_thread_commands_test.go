package chat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func TestThreadCommandsRejectRemoteTargets(t *testing.T) {
	for _, kind := range []string{"dm", "group"} {
		t.Run(kind, func(t *testing.T) {
			conn := newFakeConnector(Capabilities{})
			g, adm := groupGate(t, conn)
			ctx := context.Background()
			for _, id := range []string{"parent", "other-group"} {
				if err := adm.Approve(id, ModeAll); err != nil {
					t.Fatal(err)
				}
			}
			m := threadMsg("parent", kind, "7", "hello")
			if got := g.route(ctx, conn, m); got != actPrompt {
				t.Fatal("positive control did not admit the thread")
			}
			before := adm.chats[m.ChatID]
			for _, command := range []string{"/revoke other-group", "/approve other-group all", "/approve all other-group"} {
				m.Text = command
				if got := g.route(ctx, conn, m); got != actHandled {
					t.Fatal("invalid command was not handled")
				}
				if adm.chats[m.ChatID] != before {
					t.Fatalf("%q changed the current thread", command)
				}
				if mode, ok := adm.Mode("other-group"); !ok || mode != ModeAll {
					t.Fatalf("%q changed the named group", command)
				}
				sends := conn.sends()
				if !strings.Contains(sends[len(sends)-1].Text, "Use the owner DM") {
					t.Fatal("invalid command did not explain how to name another chat")
				}
				m.Text = "hello"
				if got := g.route(ctx, conn, m); got != actPrompt {
					t.Fatal("invalid command changed inherited routing")
				}
			}
			m.Text = "/revoke"
			g.route(ctx, conn, m)
			m.Text = "hello"
			if got := g.route(ctx, conn, m); got != actHandled {
				t.Fatal("valid local revoke did not silence the thread")
			}
			m.Text = "/approve ALL"
			g.route(ctx, conn, m)
			m.Text = "hello"
			if got := g.route(ctx, conn, m); got != actPrompt {
				t.Fatal("valid local approval did not recover the thread")
			}
			dm := Message{ChatID: "owner-room", ChatKind: "dm", UserID: "7", Text: "/revoke other-group"}
			g.route(ctx, conn, dm)
			if _, approved := adm.Mode("other-group"); approved {
				t.Fatal("the owner-DM recovery command did not revoke the named group")
			}
			if got := g.route(ctx, conn, m); got != actPrompt {
				t.Fatal("the owner-DM recovery command changed the current thread")
			}
			dm.Text = "/approve other-group all"
			g.route(ctx, conn, dm)
			if mode, approved := adm.Mode("other-group"); !approved || mode != ModeAll {
				t.Fatal("the owner-DM recovery command did not approve the named group")
			}
		})
	}
}

func TestThreadRemoteTargetDuringBindingSaveFailure(t *testing.T) {
	for _, kind := range []string{"dm", "group"} {
		t.Run(kind, func(t *testing.T) {
			conn := newFakeConnector(Capabilities{})
			g, adm := groupGate(t, conn)
			ctx := context.Background()
			if err := adm.Approve("parent", ModeAll); err != nil {
				t.Fatal(err)
			}
			m := threadMsg("parent", kind, "7", "hello")
			m.ScopeID = "old-scope"
			if got := g.route(ctx, conn, m); got != actPrompt {
				t.Fatal("positive control did not admit the thread")
			}
			before := adm.chats[m.ChatID]
			blocker := filepath.Join(testsupport.TempDir(t), "file")
			if err := os.WriteFile(blocker, []byte("not a directory"), 0600); err != nil {
				t.Fatal(err)
			}
			adm.path = filepath.Join(blocker, "admissions.json")
			m.ScopeID, m.Text = "new-scope", "/revoke other-group"
			var revoked int
			g.onRevoked = func(string, string) { revoked++ }
			g.route(ctx, conn, m)
			if adm.chats[m.ChatID] != before || revoked != 0 {
				t.Fatal("unsupported target changed or cancelled the current thread after a failed binding save")
			}
			sends := conn.sends()
			if !strings.Contains(sends[len(sends)-1].Text, "Use the owner DM") {
				t.Fatal("failed-save fallback did not explain unsupported target syntax")
			}
			m.Text = "/revoke"
			g.route(ctx, conn, m)
			if adm.chats[m.ChatID].Mode != modeMuted || revoked != 1 {
				t.Fatal("valid local revoke stopped working during a binding save failure")
			}
		})
	}
}

func TestMentionModeDMThreadOwnerControls(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, _ := groupGate(t, conn)
	ctx := context.Background()
	m := threadMsg("parent", "dm", "7", "/approve")
	g.route(ctx, conn, m)
	for _, tc := range []struct {
		text string
		want action
	}{{"hello", actHandled}, {"/status", actStatus}, {"/stop", actStop}, {"stop", actStop}, {"/help", actHandled}, {"/start", actHandled}} {
		m.Text = tc.text
		sent := len(conn.sends())
		if got := g.route(ctx, conn, m); got != tc.want {
			t.Fatalf("%q = %v, want %v", tc.text, got, tc.want)
		}
		if (tc.text == "/help" || tc.text == "/start") && len(conn.sends()) != sent+1 {
			t.Fatalf("%q did not send help", tc.text)
		}
		m.UserID = "9"
		if got := g.route(ctx, conn, m); got != actHandled {
			t.Fatalf("nonowner %q bypassed the DM thread policy", tc.text)
		}
		m.UserID = "7"
	}
	m.Text = "@tervabot hello"
	if got := g.route(ctx, conn, m); got != actPrompt {
		t.Fatal("positive mention control did not admit a prompt")
	}
	m.Text = "/revoke"
	g.route(ctx, conn, m)
	m.Text = "/status"
	if got := g.route(ctx, conn, m); got != actHandled {
		t.Fatal("owner control bypassed a muted thread")
	}
}
