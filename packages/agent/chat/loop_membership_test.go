package chat

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

type admissionResult struct {
	answer Answer
	err    error
}

type admissionCall struct {
	ctx    context.Context
	ask    Ask
	result chan admissionResult
}

type delayedAdmissionConnector struct {
	*fakeConnector
	calls chan *admissionCall
}

func (c *delayedAdmissionConnector) Ask(ctx context.Context, ask Ask) (Answer, error) {
	call := &admissionCall{ctx: ctx, ask: ask, result: make(chan admissionResult, 1)}
	c.calls <- call
	// Deliberately ignore cancellation to exercise stale-result protection.
	r := <-call.result
	return r.answer, r.err
}

func nextAdmissionCall(t *testing.T, c *delayedAdmissionConnector) *admissionCall {
	t.Helper()
	select {
	case call := <-c.calls:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("admission ask did not arrive")
		return nil
	}
}

func waitMembership(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("membership handling did not finish")
	}
}

func startMembership(l *Loop, ctx context.Context, mb Membership) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		l.onMembership(ctx, mb)
		close(done)
	}()
	return done
}

func holdMembershipMessage(g *gate, conn Connector, id, chatID, scope string) {
	g.route(context.Background(), conn, Message{
		ID: id, ChatID: chatID, ChatKind: "group", ScopeID: scope,
		UserID: "9", Text: "@tervabot hello",
	})
}

func heldMembershipIDs(g *gate, chatID string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var ids []string
	if c := g.held.chats[chatID]; c != nil {
		for _, hm := range c.msgs {
			ids = append(ids, hm.m.ID)
		}
	}
	return ids
}

func TestLoopMembershipStaleResults(t *testing.T) {
	for name, result := range map[string]admissionResult{
		"approve":     {answer: Answer{Key: "approve_all"}},
		"ignore":      {answer: Answer{Key: "ignore"}},
		"timeout":     {err: ErrAskTimeout},
		"undelivered": {err: errors.New("delivery failed")},
	} {
		t.Run(name, func(t *testing.T) {
			conn := &delayedAdmissionConnector{newFakeConnector(Capabilities{Asks: true}), make(chan *admissionCall, 4)}
			adm := LoadAdmissions("")
			l := newAdmissionLoop(conn, adm, "7", "100")
			g := &gate{pairing: pairedWith("7"), admissions: adm, botUsername: "tervabot"}
			l.attachGate(g)
			var released []Message
			g.onAdmitted = func(_ context.Context, msgs []Message) { released = append(released, msgs...) }
			mb := Membership{ChatID: "group", ScopeID: "scope", Change: "added"}
			holdMembershipMessage(g, conn, "old", mb.ChatID, mb.ScopeID)
			oldDone := startMembership(l, context.Background(), mb)
			old := nextAdmissionCall(t, conn)
			l.onMembership(context.Background(), Membership{ChatID: mb.ChatID, ScopeID: mb.ScopeID, Change: "removed"})
			if old.ctx.Err() == nil {
				t.Fatal("removal did not cancel outstanding ask")
			}
			if ids := heldMembershipIDs(g, mb.ChatID); len(ids) != 0 {
				t.Fatalf("removal retained held messages: %v", ids)
			}
			freshDone := startMembership(l, context.Background(), mb)
			fresh := nextAdmissionCall(t, conn)
			holdMembershipMessage(g, conn, "fresh", mb.ChatID, mb.ScopeID)
			old.result <- result
			waitMembership(t, oldDone)
			if _, ok := adm.Mode(mb.ChatID); ok || len(released) != 0 {
				t.Fatal("stale result admitted or replayed content")
			}
			if ids := heldMembershipIDs(g, mb.ChatID); !reflect.DeepEqual(ids, []string{"fresh"}) {
				t.Fatalf("stale result changed fresh held content: %v", ids)
			}
			l.onMembership(context.Background(), mb)
			if len(conn.calls) != 0 {
				t.Fatal("stale result released the fresh suppression claim")
			}
			fresh.result <- admissionResult{answer: Answer{Key: "approve_all"}}
			waitMembership(t, freshDone)
			if mode, ok := adm.Mode(mb.ChatID); !ok || mode != ModeAll {
				t.Fatalf("fresh approval = %q,%v", mode, ok)
			}
			if len(released) != 1 || released[0].ID != "fresh" {
				t.Fatalf("fresh approval replay = %+v", released)
			}
		})
	}
}

func TestLoopMembershipScopePendingAsks(t *testing.T) {
	conn := &delayedAdmissionConnector{newFakeConnector(Capabilities{Asks: true}), make(chan *admissionCall, 4)}
	adm := LoadAdmissions("")
	l := newAdmissionLoop(conn, adm, "7", "100")
	g := &gate{pairing: pairedWith("7"), admissions: adm, botUsername: "tervabot"}
	l.attachGate(g)
	g.onAdmitted = func(context.Context, []Message) {}
	type pending struct {
		mb   Membership
		call *admissionCall
		done <-chan struct{}
	}
	var asks []pending
	for _, mb := range []Membership{
		{ChatID: "one", ScopeID: "removed", Change: "added"},
		{ChatID: "two", ScopeID: "removed", Change: "added"},
		{ChatID: "other", ScopeID: "kept", Change: "added"},
	} {
		done := startMembership(l, context.Background(), mb)
		asks = append(asks, pending{mb, nextAdmissionCall(t, conn), done})
		holdMembershipMessage(g, conn, mb.ChatID, mb.ChatID, mb.ScopeID)
	}
	l.onMembership(context.Background(), Membership{ChatID: "container", ScopeID: "removed", Change: "removed"})
	for _, p := range asks {
		removed := p.mb.ScopeID == "removed"
		if (p.call.ctx.Err() != nil) != removed {
			t.Fatalf("ask cancellation in scope %s = %v", p.mb.ScopeID, p.call.ctx.Err())
		}
		if ids := heldMembershipIDs(g, p.mb.ChatID); (len(ids) == 0) != removed {
			t.Fatalf("scope cleanup for %s: %v", p.mb.ChatID, ids)
		}
		p.call.result <- admissionResult{answer: Answer{Key: "approve_all"}}
		waitMembership(t, p.done)
		if _, ok := adm.Mode(p.mb.ChatID); ok == removed {
			t.Fatalf("scope answer applied incorrectly to %s", p.mb.ChatID)
		}
		if removed {
			done := startMembership(l, context.Background(), p.mb)
			fresh := nextAdmissionCall(t, conn)
			fresh.result <- admissionResult{answer: Answer{Key: "ignore"}}
			waitMembership(t, done)
		}
	}
}

func TestLoopMembershipScopeSuppression(t *testing.T) {
	for _, outcome := range []string{"ignore", "timeout"} {
		t.Run(outcome, func(t *testing.T) {
			conn := &askFakeConnector{fakeConnector: newFakeConnector(Capabilities{Asks: true}), asked: make(chan Ask, 8), answer: Answer{Key: "ignore"}}
			if outcome == "timeout" {
				conn.err = ErrAskTimeout
			}
			adm := LoadAdmissions("")
			l := newAdmissionLoop(conn, adm, "7", "100")
			g := &gate{pairing: pairedWith("7"), admissions: adm, botUsername: "tervabot"}
			l.attachGate(g)
			chats := []Membership{
				{ChatID: "one", Change: "added"},
				{ChatID: "two", ScopeID: "removed", Change: "added"},
				{ChatID: "other", ScopeID: "kept", Change: "added"},
			}
			for _, mb := range chats {
				l.onMembership(context.Background(), mb)
				<-conn.asked
				holdMembershipMessage(g, conn, mb.ChatID, mb.ChatID, mb.ScopeID)
			}
			// Persisted siblings, including an already-muted target, also reset.
			if err := adm.ApproveScoped("one", ModeAll, "removed"); err != nil {
				t.Fatal(err)
			}
			if err := adm.Revoke("one"); err != nil {
				t.Fatal(err)
			}
			if err := adm.ApproveScoped("kept-approved", ModeAll, "kept"); err != nil {
				t.Fatal(err)
			}
			l.onMembership(context.Background(), Membership{ChatID: "container", ScopeID: "removed", Change: "removed"})
			for _, mb := range chats {
				wantAsk := mb.ChatID != "other"
				if ids := heldMembershipIDs(g, mb.ChatID); (len(ids) == 0) != wantAsk {
					t.Fatalf("held cleanup for %s: %v", mb.ChatID, ids)
				}
				l.onMembership(context.Background(), mb)
				if got := len(conn.asked) == 1; got != wantAsk {
					t.Fatalf("re-invite ask for %s = %v", mb.ChatID, got)
				}
				if wantAsk {
					<-conn.asked
				}
			}
			if mode, ok := adm.Mode("kept-approved"); !ok || mode != ModeAll {
				t.Fatal("unrelated scope approval changed")
			}
		})
	}
}

type delayedTextAdmissionConnector struct {
	*fakeConnector
	firstStarted chan struct{}
	firstRelease chan struct{}
	sendCount    atomic.Int32
}

func (c *delayedTextAdmissionConnector) Send(ctx context.Context, out Outgoing) error {
	if c.sendCount.Add(1) == 1 {
		close(c.firstStarted)
		<-c.firstRelease
	}
	return c.fakeConnector.Send(ctx, out)
}

func TestLoopMembershipTextFallbackReinvite(t *testing.T) {
	conn := &delayedTextAdmissionConnector{fakeConnector: newFakeConnector(Capabilities{}), firstStarted: make(chan struct{}), firstRelease: make(chan struct{})}
	adm := LoadAdmissions("")
	l := newAdmissionLoop(conn, adm, "7", "100")
	mb := Membership{ChatID: "group", Change: "added"}
	oldDone := startMembership(l, context.Background(), mb)
	waitMembership(t, conn.firstStarted)
	l.onMembership(context.Background(), Membership{ChatID: mb.ChatID, Change: "removed"})
	freshDone := startMembership(l, context.Background(), mb)
	conn.waitSends(t, 1)
	close(conn.firstRelease)
	waitMembership(t, oldDone)
	if !l.takeTextAnswer(Message{ChatID: "100", UserID: "7", Text: "approve_all"}) {
		t.Fatal("old text ask erased the new question")
	}
	waitMembership(t, freshDone)
	if mode, ok := adm.Mode(mb.ChatID); !ok || mode != ModeAll {
		t.Fatalf("new text approval = %q,%v", mode, ok)
	}
}

func TestLoopMembershipCallbackOrder(t *testing.T) {
	conn := &delayedAdmissionConnector{newFakeConnector(Capabilities{Asks: true}), make(chan *admissionCall, 4)}
	adm := LoadAdmissions("")
	if err := adm.Approve("group", ModeAll); err != nil {
		t.Fatal(err)
	}
	l := newAdmissionLoop(conn, adm, "7", "100")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handler := l.membershipHandler(ctx)
	// Hold preparation so every callback must return before any disk work.
	l.membershipMu.Lock()
	for i := 0; i < 50; i++ {
		handler(Membership{ChatID: "group", Change: "added"})
	}
	handler(Membership{ChatID: "group", Change: "removed"})
	handler(Membership{ChatID: "group", Change: "added"})
	l.membershipMu.Unlock()
	call := nextAdmissionCall(t, conn)
	if _, ok := adm.Mode("group"); ok {
		t.Fatal("re-add ask started before removal revoked approval")
	}
	call.result <- admissionResult{answer: Answer{Key: "approve_all"}}
	conn.waitSends(t, 1)
	if mode, ok := adm.Mode("group"); !ok || mode != ModeAll {
		t.Fatalf("callback re-invite approval = %q,%v", mode, ok)
	}
}

func TestLoopMembershipRemovalBeforeAnswer(t *testing.T) {
	conn := &delayedAdmissionConnector{newFakeConnector(Capabilities{Asks: true}), make(chan *admissionCall, 4)}
	adm := LoadAdmissions("")
	l := newAdmissionLoop(conn, adm, "7", "100")
	g := &gate{pairing: pairedWith("7"), admissions: adm, botUsername: "tervabot"}
	l.attachGate(g)
	var replayed atomic.Bool
	g.onAdmitted = func(context.Context, []Message) { replayed.Store(true) }
	holdMembershipMessage(g, conn, "old", "group", "")
	done := startMembership(l, context.Background(), Membership{ChatID: "group", Change: "added"})
	call := nextAdmissionCall(t, conn)
	handler := l.membershipHandler(context.Background())
	l.membershipMu.Lock()
	handler(Membership{ChatID: "group", Change: "removed"})
	call.result <- admissionResult{answer: Answer{Key: "approve_all"}}
	l.membershipMu.Unlock()
	waitMembership(t, done)
	if _, ok := adm.Mode("group"); ok || replayed.Load() {
		t.Fatal("answer overtook a removal already delivered")
	}
	if ids := heldMembershipIDs(g, "group"); len(ids) != 0 {
		t.Fatalf("removal retained held content: %v", ids)
	}
}
