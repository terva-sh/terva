package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
)

// startPerChatLoop runs a Loop with per-chat sessions on, the owner
// paired, and the given groups pre-approved in `all` mode.
func startPerChatLoop(t *testing.T, conn *fakeConnector, client *scriptedClient, maxAgents int, groups ...string) *Loop {
	t.Helper()
	adm := LoadAdmissions("")
	for _, g := range groups {
		if err := adm.Approve(g, ModeAll); err != nil {
			t.Fatal(err)
		}
	}
	l := &Loop{
		Connector:     conn,
		Agent:         core.NewAgent(client, "fake-model", "sys", core.Registry{}),
		NewChatAgent:  func() *core.Agent { return core.NewAgent(client, "fake-model", "sys", core.Registry{}) },
		MaxChatAgents: maxAgents,
		Admissions:    adm,
		Pairing:       pairedWith("7"),
		Provider:      "fake",
		Info:          func(string) {},
		Warn:          func(string) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = l.Run(ctx) }()
	return l
}

func gmsg(chatID, user, text string) Message {
	return Message{ID: "m-" + chatID, ChatID: chatID, ChatKind: "group",
		UserID: user, Username: "u" + user, Text: text}
}

// liveAgents snapshots the per-chat pool.
func liveAgents(l *Loop) map[string]*core.Agent {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]*core.Agent{}
	for id, st := range l.chatAgents {
		out[id] = st.agent
	}
	return out
}

// TestLoopPerChatAgents: each group gets its own agent, the DM keeps
// the primary, and transcripts never share a core.Agent.
func TestLoopPerChatAgents(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := startPerChatLoop(t, conn, &scriptedClient{reply: "ok"}, 0, "g1", "g2")

	conn.inbound <- gmsg("g1", "9", "hello from g1")
	conn.inbound <- gmsg("g2", "9", "hello from g2")
	conn.inbound <- msgFrom("7", "dm hello") // ChatID 100, kind dm
	conn.waitSends(t, 3)

	agents := liveAgents(l)
	if len(agents) != 2 {
		t.Fatalf("live agents = %d, want 2 (%v)", len(agents), agents)
	}
	if agents["g1"] == nil || agents["g2"] == nil || agents["g1"] == agents["g2"] {
		t.Errorf("groups must have distinct agents: %v", agents)
	}
	for id, a := range agents {
		if a == l.Agent {
			t.Errorf("group %s shares the primary agent", id)
		}
	}
}

// TestLoopPerChatStop: /stop only touches its own chat — it cannot
// kill another chat's running turn, and it drops only its own queued
// prompts.
func TestLoopPerChatStop(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	gate := make(chan struct{})
	client := &scriptedClient{reply: "done", gate: gate}
	l := startPerChatLoop(t, conn, client, 0, "g1", "g2")
	_ = l

	conn.inbound <- gmsg("g1", "9", "long job")   // becomes the active turn
	conn.inbound <- gmsg("g2", "9", "queued job") // waits behind it

	// Wait until g1's turn is the active one.
	deadline := time.Now().Add(3 * time.Second)
	for {
		l.mu.Lock()
		active := l.activeChatID
		l.mu.Unlock()
		if active == "g1" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("g1 never became the active turn")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Owner /stop in g2: drops g2's queued prompt, leaves g1 running.
	conn.inbound <- gmsg("g2", "7", "/stop")
	sends := conn.waitSends(t, 1)
	if last := sends[len(sends)-1]; last.ChatID != "g2" || !strings.Contains(last.Text, "dropped 1 queued") {
		t.Fatalf("g2 stop reply = %+v", last)
	}
	l.mu.Lock()
	stillActive := l.activeChatID
	l.mu.Unlock()
	if stillActive != "g1" {
		t.Fatalf("g1's turn was disturbed by g2's /stop (active=%q)", stillActive)
	}

	// Owner /stop in g1: cancels the running turn.
	conn.inbound <- gmsg("g1", "7", "/stop")
	deadline = time.Now().Add(3 * time.Second)
	for {
		found := false
		for _, s := range conn.sends() {
			if s.ChatID == "g1" && strings.Contains(s.Text, "cancelled") {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("g1 /stop never cancelled; sends=%v", conn.sends())
		}
		time.Sleep(5 * time.Millisecond)
	}
	close(gate) // release the client for cleanup
}

// TestLoopPerChatLRU: the pool is bounded; the least recently used
// chat's agent is dropped to make room.
func TestLoopPerChatLRU(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := startPerChatLoop(t, conn, &scriptedClient{reply: "ok"}, 2, "g1", "g2", "g3")

	conn.inbound <- gmsg("g1", "9", "one")
	conn.waitSends(t, 1)
	conn.inbound <- gmsg("g2", "9", "two")
	conn.waitSends(t, 2)
	conn.inbound <- gmsg("g3", "9", "three")
	conn.waitSends(t, 3)

	agents := liveAgents(l)
	if len(agents) != 2 {
		t.Fatalf("live agents = %d, want the cap of 2", len(agents))
	}
	if agents["g1"] != nil {
		t.Error("g1 (least recently used) should have been evicted")
	}
	if agents["g2"] == nil || agents["g3"] == nil {
		t.Errorf("g2 and g3 should be live: %v", agents)
	}
}

// TestLoopPerChatOffByDefault: without a factory, every chat shares
// the primary agent — the v1 behavior, byte for byte.
func TestLoopPerChatOffByDefault(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	adm := LoadAdmissions("")
	_ = adm.Approve("g1", ModeAll)
	l := &Loop{
		Connector:  conn,
		Agent:      core.NewAgent(&scriptedClient{reply: "ok"}, "fake-model", "sys", core.Registry{}),
		Admissions: adm,
		Pairing:    pairedWith("7"),
		Info:       func(string) {},
		Warn:       func(string) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = l.Run(ctx) }()

	conn.inbound <- gmsg("g1", "9", "hello")
	conn.waitSends(t, 1)
	if agents := liveAgents(l); len(agents) != 0 {
		t.Errorf("agents minted without a factory: %v", agents)
	}
}
