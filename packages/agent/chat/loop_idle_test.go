package chat

import (
	"context"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
)

// startIdleLoop runs a Loop with the proactive idle nudge enabled.
func startIdleLoop(t *testing.T, conn *fakeConnector, reply string, idle time.Duration) *Loop {
	t.Helper()
	l := &Loop{
		Connector: conn,
		Agent:     core.NewAgent(&scriptedClient{reply: reply}, "fake-model", "sys", core.Registry{}),
		Provider:  "fake",
		CWD:       "/ws",
		Pairing:   pairedWith("7"),
		IdleAfter: idle,
		IdlePoll:  idle / 4,
		Info:      func(string) {},
		Warn:      func(string) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = l.Run(ctx) }()
	return l
}

// TestLoopIdleNudgeFires proves the bot speaks with NO inbound message once the
// channel has been quiet past IdleAfter (the paired chat is seeded from
// pairing, so it can open the conversation cold).
func TestLoopIdleNudgeFires(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	startIdleLoop(t, conn, "proactive-ping", 60*time.Millisecond)

	// No inbound at all — the only way a send appears is a proactive nudge.
	s := conn.waitSends(t, 1)
	if s[0].Text != "proactive-ping" {
		t.Fatalf("proactive send = %q, want the agent reply", s[0].Text)
	}
	if s[0].ChatID != "7" {
		t.Fatalf("nudge went to chat %q, want the paired chat 7", s[0].ChatID)
	}
}

// TestLoopIdleNudgeOncePerSilence proves it nudges once, then waits — it does
// not nag — and re-arms only after the paired user speaks.
func TestLoopIdleNudgeOncePerSilence(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	startIdleLoop(t, conn, "ping", 60*time.Millisecond)

	// First nudge fires from the cold-start silence.
	conn.waitSends(t, 1)

	// Stay silent well past several idle windows: still exactly one send.
	time.Sleep(300 * time.Millisecond)
	if n := len(conn.sends()); n != 1 {
		t.Fatalf("expected exactly 1 send during continued silence (no nagging), got %d", n)
	}

	// The paired user speaks — that re-arms the nudge and yields a reply.
	conn.inbound <- msgFrom("7", "hello")
	conn.waitSends(t, 2)

	// After re-arming, a fresh silence produces another nudge.
	s := conn.waitSends(t, 3)
	if s[2].Text != "ping" {
		t.Fatalf("re-armed nudge = %q", s[2].Text)
	}
}

// TestLoopIdleNudgeDisabled confirms the reactive loop is unchanged when
// IdleAfter is 0: no inbound, no send, ever.
func TestLoopIdleNudgeDisabled(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	startIdleLoop(t, conn, "ping", 0)

	time.Sleep(200 * time.Millisecond)
	if n := len(conn.sends()); n != 0 {
		t.Fatalf("idle nudge disabled but %d sends appeared", n)
	}
}
