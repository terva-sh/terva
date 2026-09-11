package fleet

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

// startAggregate brings up a hub with one checked-in member plus a local
// workspace, and returns the aggregate over both.
func startAggregate(t *testing.T, ctx context.Context) (agg *Aggregate, local, remote *fakeSvc) {
	t.Helper()
	up := make(chan ctrlproto.Hello, 4)
	hub, hubURL := startHub(t, ctx, HubOptions{
		OnMemberUp: func(_ string, server ctrlproto.Hello) { up <- server },
	})

	remote = newFakeSvc(ctrlproto.SessionInfo{ID: "m1", Model: "remote-model"})
	startMember(t, ctx, hubURL, "neot", remote)
	select {
	case <-up:
	case <-time.After(10 * time.Second):
		t.Fatal("the member never checked in, so there is no fleet to aggregate")
	}

	local = newFakeSvc(ctrlproto.SessionInfo{ID: "l1", Model: "local-model"})
	agg, err := NewAggregate(hub, local, LocalOrigin)
	if err != nil {
		t.Fatalf("NewAggregate: %v", err)
	}
	return agg, local, remote
}

// TestSessionsListIsTheUnionWithOriginsAndFederatedIDs covers the fan-in, and
// with it the rule that the local workspace is not a special case. Both
// entries have to come back the same shape. A local session carrying a bare id
// or an empty origin would be the special case this forbids.
func TestSessionsListIsTheUnionWithOriginsAndFederatedIDs(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agg, _, _ := startAggregate(t, ctx)

	got, err := agg.Sessions(ctx)
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("the union holds %d sessions, want 2 (one local, one on neot): %+v", len(got), got)
	}

	byOrigin := map[string]ctrlproto.SessionInfo{}
	for _, s := range got {
		if s.Origin == "" {
			t.Errorf("session %q came back with no origin stamped", s.ID)
		}
		if !strings.Contains(s.ID, ctrlproto.FederatedIDSep) {
			t.Errorf("session id %q is not federated; a bare id collides the moment "+
				"two members own the same one", s.ID)
		}
		byOrigin[s.Origin] = s
	}

	localEntry, ok := byOrigin[LocalOrigin]
	if !ok {
		t.Fatalf("the local workspace is missing from the union: %+v", got)
	}
	if localEntry.ID != LocalOrigin+ctrlproto.FederatedIDSep+"l1" {
		t.Errorf("local session id is %q, want %q. The local workspace is an ordinary "+
			"member here, so it federates like every other one",
			localEntry.ID, LocalOrigin+ctrlproto.FederatedIDSep+"l1")
	}

	remoteEntry, ok := byOrigin["neot"]
	if !ok {
		t.Fatalf("the member's session is missing from the union: %+v", got)
	}
	if remoteEntry.ID != "neot/m1" {
		t.Errorf("member session id is %q, want neot/m1", remoteEntry.ID)
	}
	if remoteEntry.Model != "remote-model" {
		t.Errorf("the member's own fields did not survive the fan-in: model is %q", remoteEntry.Model)
	}
}

// TestAFailingMemberDoesNotBlankTheBoard pins the choice that one unreachable
// daemon is skipped rather than failing the whole list. A hub for watching a
// fleet is least useful at the moment a member breaks.
func TestAFailingMemberDoesNotBlankTheBoard(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agg, _, _ := startAggregate(t, ctx)

	var failed []string
	agg.OnSourceError = func(origin string, _ error) { failed = append(failed, origin) }

	// Replace the local source with one that errors, by pointing the aggregate
	// at a workspace whose Sessions fails.
	agg.local = &failingSvc{}

	got, err := agg.Sessions(ctx)
	if err != nil {
		t.Fatalf("one failing member failed the whole fan-in: %v", err)
	}
	if len(got) != 1 || got[0].Origin != "neot" {
		t.Fatalf("want just the member's session to survive, got %+v", got)
	}
	if len(failed) != 1 || failed[0] != LocalOrigin {
		t.Errorf("OnSourceError reported %v, want one report for %q", failed, LocalOrigin)
	}
}

type failingSvc struct {
	ctrlproto.WorkspaceService
}

func (f *failingSvc) Sessions(context.Context) ([]ctrlproto.SessionInfo, error) {
	return nil, errors.New("member is down")
}

// TestSubscribingToAFederatedIDReachesThatMember covers the fan-up. The
// re-stamp is not code here: ServeConn addresses outgoing frames with the sess
// the client subscribed with, so routing the subscription is the whole of it.
// What this proves is the routing half.
func TestSubscribingToAFederatedIDReachesThatMember(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agg, local, remote := startAggregate(t, ctx)

	events, err := agg.Subscribe(ctx, "neot/m1")
	if err != nil {
		t.Fatalf("subscribe to a federated id: %v", err)
	}
	select {
	case <-remote.subscribed:
	case <-time.After(10 * time.Second):
		t.Fatal("the subscription never reached the member")
	}
	select {
	case <-local.subscribed:
		t.Error("a subscription addressed to neot also subscribed the local workspace")
	default:
	}

	const marker = "aggregate-fanup"
	if !remote.emit(ctrlproto.Event{WireEvent: core.WireEvent{Type: marker}}) {
		t.Fatal("the member had no live subscription to emit into")
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("the subscription closed before the member's event arrived")
			}
			if ev.Type == marker {
				return
			}
		case <-deadline:
			t.Fatal("the member's event never reached the hub's subscriber")
		}
	}
}

// TestARemoteCommandIsRefusedAndHandsBackNothing is the criterion that matters
// most. The failure it guards is not a no-op: it is a remote-addressed command
// quietly applying to a LOCAL session because the id was never split.
//
// So the refusal returns a zero Source and an empty id, and this asserts that.
// A caller that ignores the error still cannot reach a workspace. The local
// case at the end is the positive control: without it, a RouteCommand that
// refused everything would pass.
func TestARemoteCommandIsRefusedAndHandsBackNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agg, local, _ := startAggregate(t, ctx)

	src, id, err := agg.RouteCommand("neot/m1")
	if !errors.Is(err, ErrRemoteNotRouted) {
		t.Fatalf("RouteCommand on a member session returned %v, want ErrRemoteNotRouted", err)
	}
	if src.Svc != nil {
		t.Error("the refusal handed back a workspace: a caller ignoring the error " +
			"would drive a session with it")
	}
	if src.Svc == local {
		t.Error("the refusal handed back the LOCAL workspace, which is exactly the " +
			"failure this criterion names")
	}
	if id != "" {
		t.Errorf("the refusal handed back the id %q; paired with a local workspace "+
			"that is how a remote command lands on a local session", id)
	}

	// Positive control: a local session still routes.
	src, id, err = agg.RouteCommand(LocalOrigin + "/l1")
	if err != nil {
		t.Fatalf("RouteCommand refused a local session too: %v", err)
	}
	if id != "l1" {
		t.Errorf("local route gave id %q, want the bare l1", id)
	}
	if src.Svc != ctrlproto.WorkspaceService(local) {
		t.Error("local route did not resolve to the local workspace")
	}
}

// TestTheWorkspaceAddressIsNotFederated covers the last criterion. A member's
// workspace events must not arrive as the hub's own, because the browser reads
// #workspace as "this daemon's session list moved, go re-read it", and a member
// saying so about itself would send the browser back to a list that never
// changed.
//
// This is not a hypothetical. ServeConn.relayWorkspaceEvents subscribes to the
// member's OWN #workspace the moment the hub's client connects, so a member
// really does pump workspace events up the socket at check-in. They land in the
// hub-side ctrlclient. The criterion is that the aggregate never wires that
// stream into the hub's own, and the emit below travels that live relay path.
func TestTheWorkspaceAddressIsNotFederated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agg, local, remote := startAggregate(t, ctx)

	events, err := agg.Subscribe(ctx, ctrlproto.AddrWorkspace)
	if err != nil {
		t.Fatalf("subscribe to %s: %v", ctrlproto.AddrWorkspace, err)
	}
	select {
	case <-local.subscribed:
	case <-time.After(10 * time.Second):
		t.Fatal("the workspace subscription never reached the local workspace")
	}

	const fromMember = "member-workspace-event"
	const fromHub = "hub-workspace-event"

	// The member's relay is a second synchronization point, and startAggregate
	// does not cover it. That function waits for OnMemberUp, which the hub fires
	// the moment the Hello lands. The subscribe this test emits into happens
	// further along on the member, when serveState.relayWorkspaceEvents runs
	// after the handshake. Without this wait the emit below can arrive first,
	// and the guard then reports a race as though it were a regression.
	select {
	case <-remote.subscribed:
	case <-time.After(10 * time.Second):
		t.Fatal("the member never subscribed to its own workspace, so the relay " +
			"this test exercises was never established")
	}

	// The member speaks first, so it has the longer head start of the two.
	if !remote.emit(ctrlproto.Event{WireEvent: core.WireEvent{Type: fromMember}}) {
		t.Fatal("the member had no live workspace relay to emit into, so this test " +
			"would pass without exercising anything")
	}
	if !local.emit(ctrlproto.Event{WireEvent: core.WireEvent{Type: fromHub}}) {
		t.Fatal("the local workspace had no live subscription to emit into")
	}

	// The hub's own event is the barrier. Reaching it proves the stream is live,
	// which is what makes the absence of the member's event mean something.
	deadline := time.After(10 * time.Second)
	for barrier := false; !barrier; {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("the workspace subscription closed before the hub's own event arrived")
			}
			if ev.Type == fromMember {
				t.Fatalf("a member's workspace event arrived on the hub's own #workspace " +
					"stream; the browser would read it as the hub's session list moving")
			}
			if ev.Type == fromHub {
				barrier = true
			}
		case <-deadline:
			t.Fatal("the hub's own workspace event never arrived, so this test proves " +
				"nothing about the member's")
		}
	}

	// The member's event took the longer path, so drain past the barrier before
	// concluding it is absent rather than late.
	grace := time.After(500 * time.Millisecond)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return
			}
			if ev.Type == fromMember {
				t.Fatal("a member's workspace event arrived on the hub's own #workspace " +
					"stream, just after the hub's own")
			}
		case <-grace:
			return
		}
	}
}

// TestAggregateIsNotAWorkspaceService pins the decision that the doc comment on
// Aggregate explains. It is a tripwire, not a rule for all time.
//
// The danger is that satisfying ctrlproto.WorkspaceService is easy and wrong:
// embed a local workspace, inherit every method not written yet, and each one
// answers locally for a member-addressed session id. That is the failure
// criterion 4 names, and the type system would report it as success.
//
// fleet-control will build a routed adapter that does satisfy the interface.
// When it does, that adapter is the thing to check, and deleting this test is
// the right move. Until then, an Aggregate that starts satisfying the interface
// means somebody inherited the 38 session-addressed methods by accident.
func TestAggregateIsNotAWorkspaceService(t *testing.T) {
	var v any = &Aggregate{}
	if _, ok := v.(ctrlproto.WorkspaceService); ok {
		t.Fatal("Aggregate now satisfies ctrlproto.WorkspaceService. If that came " +
			"from embedding a workspace, every method that is not explicitly " +
			"routed now answers from the LOCAL one, including for member-addressed " +
			"session ids. Route or refuse each one, or build the adapter in " +
			"fleet-control and delete this test deliberately")
	}

	// Positive control: the assertion above must be capable of firing. The local
	// workspace really is one, so a broken type assertion would show up here.
	var w any = &fakeSvc{}
	if _, ok := w.(ctrlproto.WorkspaceService); !ok {
		t.Fatal("fakeSvc does not satisfy ctrlproto.WorkspaceService, so the " +
			"assertion above proves nothing")
	}
}

// TestAnUnknownOriginIsRefused covers the third routing outcome, so a typo in a
// federated id fails rather than silently selecting some member.
func TestAnUnknownOriginIsRefused(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	agg, _, _ := startAggregate(t, ctx)

	if _, _, err := agg.Route("nosuchbox/abc"); !errors.Is(err, ErrUnknownOrigin) {
		t.Errorf("Route on an unknown origin returned %v, want ErrUnknownOrigin", err)
	}
	if _, err := agg.Subscribe(ctx, "nosuchbox/abc"); !errors.Is(err, ErrUnknownOrigin) {
		t.Errorf("Subscribe on an unknown origin returned %v, want ErrUnknownOrigin", err)
	}
}
