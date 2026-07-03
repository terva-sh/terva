package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func approvalOptions() []AskOption {
	return []AskOption{
		{Key: "approve", Label: "Approve", Style: "affirm"},
		{Key: "deny", Label: "Deny", Style: "deny"},
	}
}

// TestLoopTextFallbackAsk: a connector without native asks gets the
// numbered-text floor — the question is a plain send, a non-matching
// message flows through as a prompt, a matching one from the right
// chat answers best-effort.
func TestLoopTextFallbackAsk(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := startLoop(t, conn, &scriptedClient{reply: "ok"}, pairedWith("7"))

	type result struct {
		ans Answer
		err error
	}
	done := make(chan result, 1)
	go func() {
		ans, err := l.Ask(context.Background(), Ask{
			ChatID: "100", Text: "run the thing?", Options: approvalOptions(),
			RestrictTo: []string{"7"}, Timeout: 5 * time.Second,
		})
		done <- result{ans, err}
	}()

	sends := conn.waitSends(t, 1)
	if q := sends[0]; q.ChatID != "100" || !strings.Contains(q.Text, "1 — Approve") || !strings.Contains(q.Text, "2 — Deny") {
		t.Fatalf("question = %+v", q)
	}

	// A matching reply in the WRONG chat is not an answer — and stays
	// a normal prompt (the agent replies to it there).
	conn.inbound <- Message{ChatID: "200", UserID: "7", Username: "u7", Text: "1"}
	// A non-matching message in the right chat passes through as a
	// prompt too; asks must not eat conversation.
	conn.inbound <- msgFrom("7", "actually, hold on")
	conn.waitSends(t, 3) // two agent replies

	select {
	case r := <-done:
		t.Fatalf("Ask resolved early: %+v", r)
	default:
	}

	// The real answer.
	conn.inbound <- msgFrom("7", "1")
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("Ask: %v", r.err)
		}
		want := Answer{Key: "approve", UserID: "7", Username: "u7", Attestation: AttestationBestEffort}
		if r.ans != want {
			t.Errorf("answer = %+v, want %+v", r.ans, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ask never resolved")
	}

	// The answer was consumed, not enqueued: no agent turn ran for it.
	time.Sleep(50 * time.Millisecond)
	for _, s := range conn.sends()[3:] {
		t.Errorf("unexpected send after answer: %+v", s)
	}
}

// TestLoopTextAskTimeout: expiry is fail-closed and visible — the
// withdrawal text lands in the chat and ErrAskTimeout comes back.
func TestLoopTextAskTimeout(t *testing.T) {
	conn := newFakeConnector(Capabilities{})
	l := startLoop(t, conn, &scriptedClient{reply: "ok"}, pairedWith("7"))

	_, err := l.Ask(context.Background(), Ask{
		ChatID: "100", Text: "anyone?", Options: approvalOptions(),
		Timeout: 80 * time.Millisecond, TimeoutOutcome: "no answer — denied",
	})
	if !errors.Is(err, ErrAskTimeout) {
		t.Fatalf("Ask = %v, want ErrAskTimeout", err)
	}
	sends := conn.waitSends(t, 2)
	if last := sends[len(sends)-1]; last.Text != "no answer — denied" {
		t.Errorf("withdrawal = %+v", last)
	}
}

// askFakeConnector upgrades the fake with a native ask surface.
type askFakeConnector struct {
	*fakeConnector
	asked  chan Ask
	answer Answer
}

func (f *askFakeConnector) Ask(ctx context.Context, a Ask) (Answer, error) {
	f.asked <- a
	return f.answer, nil
}

// TestLoopNativeAsk: when the connector declares asks, Loop.Ask
// delegates instead of falling back to text.
func TestLoopNativeAsk(t *testing.T) {
	conn := &askFakeConnector{
		fakeConnector: newFakeConnector(Capabilities{Asks: true}),
		asked:         make(chan Ask, 1),
		answer:        Answer{Key: "approve", UserID: "7", Username: "u7", Attestation: AttestationAttested},
	}
	// startLoop is typed for the plain fake; build the loop by hand.
	l := &Loop{Connector: conn, Info: func(string) {}, Warn: func(string) {}}

	ans, err := l.Ask(context.Background(), Ask{ChatID: "100", Text: "?", Options: approvalOptions()})
	if err != nil {
		t.Fatalf("Ask: %v", err)
	}
	if ans.Attestation != AttestationAttested || ans.Key != "approve" {
		t.Errorf("answer = %+v", ans)
	}
	select {
	case <-conn.asked:
	default:
		t.Error("native Ask never reached the connector")
	}
	if len(conn.sends()) != 0 {
		t.Errorf("native path must not send fallback text: %v", conn.sends())
	}
}

func TestMatchAskOption(t *testing.T) {
	opts := []AskOption{
		{Key: "approve", Label: "Approve"},
		{Key: "deny", Label: "Deny it all"},
	}
	cases := []struct {
		in  string
		key string
		ok  bool
	}{
		{"1", "approve", true},
		{" 2 ", "deny", true},
		{"approve", "approve", true},
		{"APPROVE", "approve", true},
		{"deny it all", "deny", true},
		{"3", "", false},
		{"0", "", false},
		{"sure, go ahead", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		key, ok := matchAskOption(opts, c.in)
		if key != c.key || ok != c.ok {
			t.Errorf("matchAskOption(%q) = %q,%v want %q,%v", c.in, key, ok, c.key, c.ok)
		}
	}
}

// TestLoopAdmissionAsk: being added to a group fires ONE owner-DM ask;
// approval persists the admission and confirms; removal revokes.
func TestLoopAdmissionAsk(t *testing.T) {
	conn := &askFakeConnector{
		fakeConnector: newFakeConnector(Capabilities{Asks: true}),
		asked:         make(chan Ask, 4),
		answer:        Answer{Key: "approve_all", UserID: "7", Username: "u7", Attestation: AttestationAttested},
	}
	adm := LoadAdmissions("")
	l := &Loop{Connector: conn, Admissions: adm, Info: func(string) {}, Warn: func(string) {}}
	l.mu.Lock()
	l.ownerID = "7"
	l.pairedChatID = "100"
	l.mu.Unlock()

	mb := Membership{ChatID: "g9", ChatKind: "group", ChatTitle: "ops",
		Change: "added", ByUserID: "u1", ByUsername: "drew"}
	l.onMembership(context.Background(), mb)

	select {
	case a := <-conn.asked:
		if a.ChatID != "100" || !strings.Contains(a.Text, `"ops"`) || !strings.Contains(a.Text, "@drew") {
			t.Errorf("ask = %+v", a)
		}
		if len(a.RestrictTo) != 1 || a.RestrictTo[0] != "7" {
			t.Errorf("restrict = %v", a.RestrictTo)
		}
	default:
		t.Fatal("no admission ask fired")
	}
	if mode, ok := adm.Mode("g9"); !ok || mode != ModeAll {
		t.Errorf("admission = %q,%v want all", mode, ok)
	}
	sends := conn.waitSends(t, 1)
	if !strings.Contains(sends[0].Text, "approved") || sends[0].ChatID != "100" {
		t.Errorf("confirmation = %+v", sends[0])
	}

	// Same chat again: the dedupe map keeps us from nagging.
	_ = adm.Revoke("g9")
	l.onMembership(context.Background(), mb)
	select {
	case a := <-conn.asked:
		t.Fatalf("second ask fired: %+v", a)
	default:
	}

	// Removal revokes a standing approval.
	_ = adm.Approve("g9", ModeMention)
	l.onMembership(context.Background(), Membership{ChatID: "g9", Change: "removed"})
	if _, ok := adm.Mode("g9"); ok {
		t.Error("removal did not revoke")
	}
}

// TestLoopAdmissionAskIgnored: "ignore" (or a timeout) leaves the
// chat silent and unapproved.
func TestLoopAdmissionAskIgnored(t *testing.T) {
	conn := &askFakeConnector{
		fakeConnector: newFakeConnector(Capabilities{Asks: true}),
		asked:         make(chan Ask, 1),
		answer:        Answer{Key: "ignore", UserID: "7", Username: "u7", Attestation: AttestationAttested},
	}
	adm := LoadAdmissions("")
	l := &Loop{Connector: conn, Admissions: adm, Info: func(string) {}, Warn: func(string) {}}
	l.mu.Lock()
	l.ownerID = "7"
	l.pairedChatID = "100"
	l.mu.Unlock()

	l.onMembership(context.Background(), Membership{ChatID: "g9", ChatKind: "group", Change: "added"})
	if _, ok := adm.Mode("g9"); ok {
		t.Error("ignored admission must not approve")
	}
	if len(conn.sends()) != 0 {
		t.Errorf("sends = %v, want none on ignore", conn.sends())
	}
}
