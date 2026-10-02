package chat

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/internal/coretest"
	"terva.sh/terva/packages/core"
)

func TestLoopThreadConversationsAndOwnerDM(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := startPerChatLoop(t, conn, &scriptedClient{reply: "ok"}, 0)
	conn.inbound <- msgFrom("7", "parent hello")
	conn.waitSends(t, 1)
	m := threadMsg("100", "dm", "7", "thread hello")
	conn.inbound <- m
	m.ChatID, m.ID = "another-opaque-thread", "other-message"
	conn.inbound <- m
	sends := conn.waitSends(t, 3)
	if sends[1].ChatID != "opaque-thread" || sends[2].ChatID != m.ChatID {
		t.Fatalf("thread replies = %+v", sends)
	}
	agents := liveAgents(l)
	if len(agents) != 2 || agents["opaque-thread"] == agents[m.ChatID] || agents[m.ChatID] == l.Agent {
		t.Fatalf("thread agents = %+v", agents)
	}
	l.mu.Lock()
	l.activeChatID, l.activeMsgID, l.activeChatKind = m.ChatID, m.ID, m.ChatKind
	l.mu.Unlock()
	if chatID, replyTo := l.AskTarget(); chatID != "100" || replyTo != "" {
		t.Fatalf("owner question target = %q/%q", chatID, replyTo)
	}
}

func TestLoopAdmittedReplySurvivesMentionModeChange(t *testing.T) {
	for _, kind := range []string{"group", "thread"} {
		t.Run(kind, func(t *testing.T) {
			conn := newFakeConnector(Capabilities{})
			block := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(block) }) }
			t.Cleanup(unblock)
			l := startPerChatLoop(t, conn, &scriptedClient{reply: "finished", gate: block}, 0, "parent")
			m := Message{ID: "m", ChatID: "parent", ChatKind: "group", UserID: "9", Text: "work"}
			if kind == "thread" {
				m = threadMsg("parent", "group", "9", "work")
			}
			conn.inbound <- m
			deadline := time.Now().Add(3 * time.Second)
			for {
				l.mu.Lock()
				active := l.activeChatID == m.ChatID
				l.mu.Unlock()
				if active {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("control turn did not start")
				}
				time.Sleep(5 * time.Millisecond)
			}
			conn.inbound <- Message{ChatID: "parent", ChatKind: "group", UserID: "7", Text: "/approve"}
			conn.waitSends(t, 1)
			unblock()
			sends := conn.waitSends(t, 2)
			if sends[1].ChatID != m.ChatID || sends[1].Text != "finished" {
				t.Fatalf("admitted reply was lost after mode change: %+v", sends)
			}
		})
	}
}

func TestLoopQueuedThreadRechecksParent(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	g, adm := groupGate(t, conn)
	m := threadMsg("parent", "group", "9", "hello")
	_ = adm.Approve("parent", ModeAll)
	if g.route(context.Background(), conn, m) != actPrompt {
		t.Fatal("control not admitted")
	}
	l := &Loop{Connector: conn, gate: g}
	_ = adm.Revoke("parent")
	// This would dereference the absent agent if the revoked prompt ran.
	l.runTurn(context.Background(), m)
	if len(conn.sends()) != 0 {
		t.Fatal("revoked queued prompt produced a reply")
	}
}

func TestLoopQueuedStandalonePromptRechecksMentionMode(t *testing.T) {
	for _, kind := range []string{"group", "channel"} {
		t.Run(kind, func(t *testing.T) {
			conn := newFakeConnector(Capabilities{})
			g, adm := groupGate(t, conn)
			if err := adm.Approve("parent", ModeAll); err != nil {
				t.Fatal(err)
			}
			m := Message{ID: "queued", ChatID: "parent", ChatKind: kind, UserID: "9", Text: "plain"}
			if got := g.route(t.Context(), conn, m); got != actPrompt {
				t.Fatal("control prompt was not admitted under all mode")
			}
			client := &scriptedClient{reply: "finished"}
			l := &Loop{Connector: conn, gate: g, Agent: coretest.NewAgent(client, "fake-model", "sys", core.Registry{}),
				Info: func(string) {}, Warn: func(string) {}}
			if err := adm.Approve("parent", ModeMention); err != nil {
				t.Fatal(err)
			}
			l.runTurn(t.Context(), m)
			if client.calls.Load() != 0 || len(conn.sends()) != 0 {
				t.Fatal("a queued plain prompt ran after mention mode was selected")
			}
			m.Text = "@tervabot hello"
			l.runTurn(t.Context(), m)
			if client.calls.Load() != 1 || len(conn.sends()) != 1 || conn.sends()[0].Text != "finished" {
				t.Fatal("an eligible queued mention did not reach the provider and reply")
			}
		})
	}
}

func TestThreadEventsKeepOpaqueConversationAfterRestart(t *testing.T) {
	first := &Loop{Info: func(string) {}}
	m := threadMsg("parent", "group", "7", "original")
	first.queue = []Message{m, {ID: m.ID, ChatID: "parent", Text: "parent original"}}
	first.onMessageEdited(MessageEdited{ChatID: m.ChatID, ID: m.ID, Text: "thread edited"})
	if first.queue[0].Text != "thread edited" || first.queue[0].ParentChatID != m.ParentChatID || first.queue[1].Text != "parent original" {
		t.Fatalf("edit correlation = %+v", first.queue)
	}
	first.onMessageDeleted(MessageDeleted{ChatID: m.ChatID, ID: m.ID})
	if len(first.queue) != 1 || first.queue[0].ChatID != "parent" {
		t.Fatal("thread delete touched parent")
	}
	// A fresh loop accepts late events under the same opaque conversation ID.
	restarted := &Loop{Info: func(string) {}}
	restarted.onMessageEdited(MessageEdited{ChatID: m.ChatID, ID: m.ID, Text: "late edit"})
	restarted.onReaction(Reaction{ChatID: m.ChatID, MessageID: "bot-message", Username: "owner", Key: "yes", OwnMessage: true})
	if parent := restarted.promptText(Message{ChatID: "parent", Text: "next"}); strings.Contains(parent, "late edit") || strings.Contains(parent, "reaction") {
		t.Fatal("thread events leaked into parent")
	}
	thread := restarted.promptText(m)
	if !strings.Contains(thread, "late edit") || !strings.Contains(thread, "reaction") {
		t.Fatalf("late thread events missing: %s", thread)
	}
}

func TestLoopParentRevocationStopsRunningThread(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	l := startPerChatLoop(t, conn, &scriptedClient{reply: "should not send", gate: block}, 0, "parent")
	m := threadMsg("parent", "group", "9", "work")
	conn.inbound <- m
	deadline := time.Now().Add(3 * time.Second)
	for {
		l.mu.Lock()
		active := l.activeChatID == m.ChatID
		l.mu.Unlock()
		if active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control thread did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	conn.inbound <- Message{ChatID: "100", UserID: "7", Text: "/revoke parent"}
	sends := conn.waitSends(t, 1)
	if sends[0].ChatID != "100" {
		t.Fatalf("revoked turn sent a reply: %+v", sends)
	}
	for {
		l.mu.Lock()
		busy := l.busy
		l.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("parent revoke did not cancel thread")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(conn.sends()) != 1 {
		t.Fatal("canceled thread sent a response")
	}
}

func TestLoopScopeRemovalStopsThreadsWithoutScopeMetadata(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	client := &scriptedClient{reply: "should not send", gate: block}
	l := startPerChatLoop(t, conn, client, 0)
	if err := l.Admissions.ApproveScoped("parent", ModeAll, "container"); err != nil {
		t.Fatal(err)
	}
	m := threadMsg("parent", "group", "9", "active work")
	conn.inbound <- m
	deadline := time.Now().Add(3 * time.Second)
	for client.calls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("control provider turn did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}
	queued := m
	queued.ChatID, queued.ID, queued.Text = "queued-thread", "queued", "queued work"
	conn.inbound <- queued
	for {
		l.mu.Lock()
		n := len(l.queue)
		l.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control thread was not queued")
		}
		time.Sleep(5 * time.Millisecond)
	}
	g := l.heldGate()
	held := m
	held.ChatID, held.ID = "held-thread", "held"
	g.mu.Lock()
	g.held.add(held)
	g.mu.Unlock()
	l.onMembership(t.Context(), Membership{ChatID: "sibling-channel", ChatKind: "group", ScopeID: "container", Change: "removed"})
	l.mu.Lock()
	n := len(l.queue)
	l.mu.Unlock()
	if n != 0 {
		t.Fatal("scope removal retained parent queued work without message scope")
	}
	g.mu.Lock()
	n = len(g.held.chats)
	g.mu.Unlock()
	if n != 0 {
		t.Fatal("scope removal retained parent held work without message scope")
	}
	for {
		l.mu.Lock()
		busy := l.busy
		l.mu.Unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scope removal did not cancel the active parent turn without message scope")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if client.calls.Load() != 1 || len(conn.sends()) != 0 {
		t.Fatal("scope removal ran queued work or sent a revoked reply")
	}
}
