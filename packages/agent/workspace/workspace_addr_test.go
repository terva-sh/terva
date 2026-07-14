package workspace

import (
	"context"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
)

func recv(t *testing.T, ch <-chan ctrlproto.Event) (ctrlproto.Event, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(2 * time.Second):
		return ctrlproto.Event{}, false
	}
}

// The hole the workspace address exists to close.
//
// BroadcastAll used to fan an event out to every LIVE SESSION's subscribers, so
// a client that held no session subscription heard nothing — and neither did a
// workspace whose last live session had just been deleted. That is precisely the
// state in which a workspace-scoped event matters most: "the session set
// changed" and "a provider just logged in" are both facts you need while you
// have no session at all.
func TestAWorkspaceEventArrivesWithNoSessionSubscribed(t *testing.T) {
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := w.Subscribe(ctx, ctrlproto.AddrWorkspace)
	if err != nil {
		t.Fatalf("subscribe to the workspace: %v", err)
	}

	// No sessions exist at all — w.sessions is empty.
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("tasks"))

	ev, ok := recv(t, ch)
	if !ok {
		t.Fatal("no event: a workspace with zero sessions cannot tell anyone anything")
	}
	if ev.Type != ctrlproto.EventSurfaceUpdated {
		t.Errorf("got %q, want %q", ev.Type, ctrlproto.EventSurfaceUpdated)
	}
}

// A session subscription must NOT also receive workspace events. If it did, a
// client that subscribed to both addresses would see every workspace event
// twice — which is the failure the compat pump in serveState exists to avoid,
// and it only works because the workspace publishes to exactly one place.
func TestAWorkspaceEventDoesNotAlsoRideTheSessionStream(t *testing.T) {
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}
	s := &wsSession{id: "s1", ws: w, hub: newWSHub()}
	w.sessions[s.id] = s

	sessCh := s.hub.add(nil, true)
	w.BroadcastAll(ctrlproto.SurfaceUpdatedEvent("tasks"))

	select {
	case ev := <-sessCh:
		t.Fatalf("a workspace event was also stamped onto the session stream (%q); "+
			"a client subscribed to both addresses would receive it twice", ev.Type)
	case <-time.After(200 * time.Millisecond):
	}
}

// The line that must not be forgotten.
//
// validSessionID is a PATH-SAFETY check — no separators, no traversal, a clean
// base name — and "#workspace" passes every one of those. Without an explicit
// refusal, resolving it as a session would materialize a session FILE named
// "#workspace", and a client could create a session that shadows the address.
func TestAReservedAddressIsNeverASession(t *testing.T) {
	if validSessionID(ctrlproto.AddrWorkspace) {
		t.Fatal("#workspace is accepted as a session id — the workspace would create a session file for it, " +
			"and a session could then shadow the address")
	}
	// The refusal is on the whole reserved namespace, not on one magic string.
	if validSessionID("#anything") {
		t.Error("a reserved-prefix id is accepted as a session id")
	}
	// And an ordinary id is still a session.
	if !validSessionID("2026-07-13T10-00-00") {
		t.Error("a normal session id was rejected")
	}
}

// Subscribing to a session that does not exist must not be how you reach the
// workspace, and vice versa: the two addresses are separate spaces.
func TestTheWorkspaceAddressIsNotResolvedAsASession(t *testing.T) {
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := w.Subscribe(ctx, ctrlproto.AddrWorkspace); err != nil {
		t.Fatalf("the workspace address must be subscribable: %v", err)
	}
	// It did not land in the session map on the way.
	if _, ok := w.sessions[ctrlproto.AddrWorkspace]; ok {
		t.Error("subscribing to the workspace materialized a session named #workspace")
	}
}

// Cancelling a workspace subscription releases it: no leak, and no send on a
// channel nobody is reading.
func TestCancellingAWorkspaceSubscriptionReleasesIt(t *testing.T) {
	w := &Workspace{ctx: context.Background(), diag: func(string) {}, sessions: map[string]*wsSession{}}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := w.Subscribe(ctx, ctrlproto.AddrWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	cancel()

	// The channel closes once the subscription is torn down.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // closed: released
			}
		case <-deadline:
			t.Fatal("the workspace subscription never closed after its context ended")
		}
	}
}
