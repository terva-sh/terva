package chat

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/core"
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
		// DeepEqual, not ==: Answer carries a Keys slice since multi-select
		// landed, so the struct is no longer comparable.
		want := Answer{Key: "approve", UserID: "7", Username: "u7", Attestation: AttestationBestEffort}
		if !reflect.DeepEqual(r.ans, want) {
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
	// hold, when non-nil, keeps the ask outstanding until it is closed — a
	// question nobody has answered yet. Honouring ctx while parked is what a
	// real connector does, and a fake that did not would let a confirmer pass
	// these tests while ignoring cancellation.
	hold chan struct{}
	// err, when non-nil, fails the ask instead of answering it: a question
	// that never reached a human (a chat id the connector rejects, a dead
	// session), which is a different outcome from one that expired.
	err error
}

func (f *askFakeConnector) Ask(ctx context.Context, a Ask) (Answer, error) {
	f.asked <- a
	if f.err != nil {
		return Answer{}, f.err
	}
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return Answer{}, ctx.Err()
		}
	}
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

// parseAskReply is where the floor earns its keep: it is the only path
// that can carry a multi-select or a written-in answer, so every shape
// of reply has to resolve here or nowhere.
func TestParseAskReply(t *testing.T) {
	opts := []AskOption{
		{Key: "logs", Label: "Logs"},
		{Key: "traces", Label: "Traces"},
		{Key: "metrics", Label: "Metrics"},
	}
	cases := []struct {
		name string
		ask  Ask
		in   string
		want Answer
		ok   bool
	}{
		{"single unchanged", Ask{Options: opts}, "2", Answer{Key: "traces"}, true},
		{"single rejects prose", Ask{Options: opts}, "the second one", Answer{}, false},

		{"multi one", Ask{Options: opts, MultiSelect: true},
			"2", Answer{Key: "traces", Keys: []string{"traces"}}, true},
		{"multi several", Ask{Options: opts, MultiSelect: true},
			"1,3", Answer{Key: "logs", Keys: []string{"logs", "metrics"}}, true},
		{"multi spaces and labels", Ask{Options: opts, MultiSelect: true},
			" Logs , 2 ", Answer{Key: "logs", Keys: []string{"logs", "traces"}}, true},
		// Order is the responder's, not the option list's — "3,1" means they
		// named metrics first and nothing should reorder that.
		{"multi keeps reply order", Ask{Options: opts, MultiSelect: true},
			"3,1", Answer{Key: "metrics", Keys: []string{"metrics", "logs"}}, true},
		{"multi dedupes", Ask{Options: opts, MultiSelect: true},
			"2,2", Answer{Key: "traces", Keys: []string{"traces"}}, true},
		// All-or-nothing: taking the recognised half would silently drop a
		// choice the user made, and they would never learn it.
		{"multi rejects partial", Ask{Options: opts, MultiSelect: true},
			"1,banana", Answer{}, false},

		// Option matching wins over custom text: the list was offered, so a
		// bare number is a pick, not the literal character.
		{"custom yields to an option", Ask{Options: opts, AllowCustom: true},
			"2", Answer{Key: "traces"}, true},
		{"custom takes prose", Ask{Options: opts, AllowCustom: true},
			"none of those, use syslog", Answer{Text: "none of those, use syslog"}, true},
		{"custom trims", Ask{Options: opts, AllowCustom: true},
			"  syslog  ", Answer{Text: "syslog"}, true},
		{"custom rejects empty", Ask{Options: opts, AllowCustom: true}, "   ", Answer{}, false},
		// No options at all: pure free text, which is what a question with an
		// empty option list means in core.UserQuestion.
		{"custom with no options", Ask{AllowCustom: true},
			"call it gamma", Answer{Text: "call it gamma"}, true},

		{"multi and custom together", Ask{Options: opts, MultiSelect: true, AllowCustom: true},
			"1,2", Answer{Key: "logs", Keys: []string{"logs", "traces"}}, true},
		{"multi and custom falls to text", Ask{Options: opts, MultiSelect: true, AllowCustom: true},
			"whatever you think", Answer{Text: "whatever you think"}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseAskReply(c.ask, c.in)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v (answer %+v)", ok, c.ok, got)
			}
			if ok && !reflect.DeepEqual(got, c.want) {
				t.Errorf("answer = %+v, want %+v", got, c.want)
			}
		})
	}
}

// The routing inversion, and the reason widgetCanExpress is not just a
// capability check: a connector WITH the native widget must still be
// handed a multi-select question over the text floor, because the wire's
// answer frame returns one key and would silently narrow the question.
func TestLoopRichAskBypassesTheWidget(t *testing.T) {
	for _, c := range []struct {
		name string
		ask  Ask
	}{
		{"multi-select", Ask{ChatID: "100", Text: "which?", Options: approvalOptions(), MultiSelect: true}},
		{"free text", Ask{ChatID: "100", Text: "what name?", AllowCustom: true}},
	} {
		t.Run(c.name, func(t *testing.T) {
			conn := &askFakeConnector{
				fakeConnector: newFakeConnector(Capabilities{Asks: true}),
				asked:         make(chan Ask, 1),
				answer:        Answer{Key: "approve"},
			}
			l := &Loop{Connector: conn, Info: func(string) {}, Warn: func(string) {}}

			ask := c.ask
			ask.Timeout = 60 * time.Millisecond
			_, _ = l.Ask(context.Background(), ask)

			select {
			case a := <-conn.asked:
				t.Fatalf("the widget was handed a question it cannot express: %+v", a)
			default:
			}
			// It went to the floor instead — the question was SENT as text.
			if got := conn.sends(); len(got) == 0 {
				t.Fatal("neither path ran: the ask reached nobody")
			} else if !strings.Contains(got[0].Text, "reply with") {
				t.Errorf("floor question = %q", got[0].Text)
			}
		})
	}
}

// The floor's instruction line is the ONLY place a responder learns what
// shapes of reply this question takes, so each combination must say its
// own thing rather than defaulting to "reply with a number".
//
// 🪤 The Contains checks alone are NOT enough, and this test proved it by
// passing while the strings were broken: i18n.T takes the English text as
// its source and everything after as printf args, so an id-plus-default
// call rendered "ask.reply_multi%!(EXTRA string=…, or several separated by
// commas.)" — which still contains "commas". A substring assertion cannot
// see a mangled string that happens to embed the words it wants, so the
// rendering is checked for damage separately.
func TestAskReplyInstruction(t *testing.T) {
	cases := []struct {
		ask       Ask
		wantParts []string
	}{
		{Ask{}, []string{"a number"}},
		{Ask{MultiSelect: true}, []string{"several", "commas"}},
		{Ask{AllowCustom: true}, []string{"your own"}},
		{Ask{MultiSelect: true, AllowCustom: true}, []string{"commas", "your own"}},
	}
	seen := make(map[string]bool, len(cases))
	for _, c := range cases {
		got := askReplyInstruction(c.ask)
		for _, part := range c.wantParts {
			if !strings.Contains(got, part) {
				t.Errorf("instruction for %+v = %q, missing %q", c.ask, got, part)
			}
		}
		// A leftover formatting verb or an unconsumed argument means the
		// string was assembled wrongly, however well it greps.
		if strings.Contains(got, "%!") || strings.Contains(got, "%s") || strings.Contains(got, "%d") {
			t.Errorf("instruction for %+v is mangled: %q", c.ask, got)
		}
		// A dotted id leaking through means i18n.T was called as if it took
		// a key and a default. It does not.
		if strings.HasPrefix(got, "ask.") {
			t.Errorf("instruction for %+v rendered an id, not text: %q", c.ask, got)
		}
		if seen[got] {
			t.Errorf("instruction for %+v duplicates another case: %q", c.ask, got)
		}
		seen[got] = true
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

// newAdmissionLoop builds a Loop wired for admission asks. owner/ownerDM
// empty models a run where nobody has paired (or the owner has not DM'd)
// yet — the state in which the ask cannot be delivered.
func newAdmissionLoop(conn Connector, adm *Admissions, owner, ownerDM string) *Loop {
	l := &Loop{Connector: conn, Admissions: adm, Info: func(string) {}, Warn: func(string) {}}
	l.mu.Lock()
	l.ownerID = owner
	l.pairedChatID = ownerDM
	l.mu.Unlock()
	return l
}

// TestLoopAdmissionAskUnpairedStaysPromptable: a membership frame that
// arrives before anyone is paired must not consume the chat's one ask.
// It used to — the suppression flag was written before the owner guard —
// so the owner was never prompted for that chat again, and the
// re-announcement the contract advertises as a self-heal hit the
// suppression rather than the ask.
func TestLoopAdmissionAskUnpairedStaysPromptable(t *testing.T) {
	conn := &askFakeConnector{
		fakeConnector: newFakeConnector(Capabilities{Asks: true}),
		asked:         make(chan Ask, 4),
		answer:        Answer{Key: "approve", UserID: "7", Username: "u7"},
	}
	adm := LoadAdmissions("")
	l := newAdmissionLoop(conn, adm, "", "") // unpaired

	mb := Membership{ChatID: "g9", ChatKind: "group", ChatTitle: "ops", Change: "added"}
	l.onMembership(context.Background(), mb)
	select {
	case a := <-conn.asked:
		t.Fatalf("asked while unpaired: %+v", a)
	default:
	}

	// The owner pairs, the connector re-announces: NOW the ask must fire.
	l.mu.Lock()
	l.ownerID, l.pairedChatID = "7", "100"
	l.mu.Unlock()
	l.onMembership(context.Background(), mb)
	select {
	case a := <-conn.asked:
		if a.ChatID != "100" {
			t.Errorf("ask chat = %q, want the owner DM", a.ChatID)
		}
	default:
		t.Fatal("re-announcement after pairing did not prompt — the ask was burned while unpaired")
	}
	if mode, ok := adm.Mode("g9"); !ok || mode != ModeMention {
		t.Errorf("admission = %q,%v want mention", mode, ok)
	}
}

// TestLoopAdmissionAskUndeliveredStaysPromptable: an ask the owner never
// saw (the connector rejected the chat id — the paired chat is seeded
// from the USER id until the first inbound DM) must not count as asked.
func TestLoopAdmissionAskUndeliveredStaysPromptable(t *testing.T) {
	conn := &askFakeConnector{
		fakeConnector: newFakeConnector(Capabilities{Asks: true}),
		asked:         make(chan Ask, 4),
		answer:        Answer{Key: "approve", UserID: "7", Username: "u7"},
		err:           errors.New(`bad chat id "@bot:example.org"`),
	}
	adm := LoadAdmissions("")
	l := newAdmissionLoop(conn, adm, "7", "@bot:example.org")

	mb := Membership{ChatID: "g9", ChatKind: "group", Change: "added"}
	l.onMembership(context.Background(), mb)
	if _, ok := <-conn.asked; !ok {
		t.Fatal("no ask attempted")
	}
	if _, ok := adm.Mode("g9"); ok {
		t.Error("a failed ask must not approve")
	}

	// The owner's first DM corrects the chat id; the retry must land.
	conn.err = nil
	l.mu.Lock()
	l.pairedChatID = "!dm:example.org"
	l.mu.Unlock()
	l.onMembership(context.Background(), mb)
	select {
	case a := <-conn.asked:
		if a.ChatID != "!dm:example.org" {
			t.Errorf("retry chat = %q, want the corrected DM", a.ChatID)
		}
	default:
		t.Fatal("retry did not prompt — the undelivered ask was burned")
	}
	if _, ok := adm.Mode("g9"); !ok {
		t.Error("the retry's approval did not persist")
	}
}

// TestLoopAdmissionAskTimeoutStaysBurned is the other half of the rule:
// a question the owner SAW and let expire is an answer, so it must not
// come back. Without this, releasing the claim on error would turn every
// ignored invite into a nag on the next membership frame.
func TestLoopAdmissionAskTimeoutStaysBurned(t *testing.T) {
	conn := &askFakeConnector{
		fakeConnector: newFakeConnector(Capabilities{Asks: true}),
		asked:         make(chan Ask, 4),
		err:           ErrAskTimeout,
	}
	adm := LoadAdmissions("")
	l := newAdmissionLoop(conn, adm, "7", "100")

	mb := Membership{ChatID: "g9", ChatKind: "group", Change: "added"}
	l.onMembership(context.Background(), mb)
	if _, ok := <-conn.asked; !ok {
		t.Fatal("no ask attempted")
	}

	l.onMembership(context.Background(), mb)
	select {
	case a := <-conn.asked:
		t.Fatalf("expired ask was re-asked: %+v", a)
	default:
	}
	if _, ok := adm.Mode("g9"); ok {
		t.Error("an expired admission must not approve")
	}
}

// TestLoopAdmissionAskReplaysHeld: the mention that made the owner say yes
// becomes the chat's first turn — through the real gate the Loop built,
// then the ask's approval path, which does not go through the gate.
func TestLoopAdmissionAskReplaysHeld(t *testing.T) {
	conn := &askFakeConnector{
		fakeConnector: newFakeConnector(Capabilities{Asks: true}),
		asked:         make(chan Ask, 4),
		answer:        Answer{Key: "approve", UserID: "7", Username: "u7", Attestation: AttestationAttested},
	}
	adm := LoadAdmissions("")
	l := &Loop{
		Connector:  conn,
		Admissions: adm,
		Agent:      core.NewAgent(&scriptedClient{reply: "ok"}, "fake-model", "sys", core.Registry{}),
		Provider:   "fake",
		CWD:        "/ws",
		Pairing:    pairedWith("7"),
		Info:       func(string) {},
		Warn:       func(string) {},
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = l.Run(ctx) }()

	// Two messages in a chat nobody admitted: one mention, one not.
	plain := Message{ID: "p1", ChatID: "g9", ChatKind: "group", UserID: "9", Username: "u9", Text: "chatter"}
	mention := Message{ID: "p2", ChatID: "g9", ChatKind: "group", UserID: "9", Username: "u9", Text: "hey, what's up?",
		Entities: []Entity{{Kind: "bot_mention", Offset: 0, Length: 3}}}
	conn.inbound <- plain
	conn.inbound <- mention
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if g := l.heldGate(); g != nil {
			g.mu.Lock()
			c := g.held.chats["g9"]
			n := 0
			if c != nil {
				n = len(c.msgs)
			}
			g.mu.Unlock()
			if n == 2 {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(conn.sends()) != 0 {
		t.Fatalf("an un-admitted chat got replies: %+v", conn.sends())
	}

	l.mu.Lock()
	l.pairedChatID = "100"
	l.mu.Unlock()
	l.onMembership(ctx, Membership{ChatID: "g9", ChatKind: "group", ChatTitle: "ops", Change: "added", ByUsername: "drew"})

	// The DM confirmation names what it is answering, and the mention
	// (only — mention mode) runs as a turn in the group.
	sends := conn.waitSends(t, 2)
	var confirm, reply *Outgoing
	for i := range sends {
		switch sends[i].ChatID {
		case "100":
			confirm = &sends[i]
		case "g9":
			reply = &sends[i]
		}
	}
	if confirm == nil || !strings.Contains(confirm.Text, "starting with the message that was waiting") {
		t.Errorf("confirmation = %+v", confirm)
	}
	if reply == nil || reply.Text != "ok" {
		t.Errorf("group reply = %+v, want the turn's answer in g9", reply)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(conn.sends()); n != 2 {
		t.Errorf("sends = %d, want exactly the confirmation and one reply (the plain message must not replay in mention mode)", n)
	}
}

// TestLoopAdmissionAskIgnoredDropsHeld: a no — or an expiry — drops what
// the chat had waiting; a later /approve starts clean.
func TestLoopAdmissionAskIgnoredDropsHeld(t *testing.T) {
	conn := &askFakeConnector{
		fakeConnector: newFakeConnector(Capabilities{Asks: true}),
		asked:         make(chan Ask, 4),
		answer:        Answer{Key: "ignore", UserID: "7", Username: "u7", Attestation: AttestationAttested},
	}
	adm := LoadAdmissions("")
	l := newAdmissionLoop(conn, adm, "7", "100")
	g := &gate{pairing: Pairing{AllowedUserID: "7"}, admissions: adm, botUsername: "tervabot"}
	var released []Message
	l.attachGate(g)
	g.onAdmitted = func(_ context.Context, msgs []Message) { released = append(released, msgs...) }
	ctx := context.Background()
	g.route(ctx, conn, Message{ID: "p1", ChatID: "g9", ChatKind: "group", UserID: "9", Text: "@tervabot hello?"})

	l.onMembership(ctx, Membership{ChatID: "g9", ChatKind: "group", Change: "added"})
	<-conn.asked
	g.route(ctx, conn, Message{ID: "a1", ChatID: "g9", ChatKind: "group", UserID: "7", Text: "/approve all"})
	if len(released) != 0 {
		t.Errorf("ignored ask kept %+v waiting for the later approval", released)
	}
}
