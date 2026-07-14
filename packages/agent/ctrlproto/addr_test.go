package ctrlproto

import (
	"context"
	"testing"
	"time"
)

// A client that speaks the workspace address gets workspace events ONCE, on that
// address — not stamped onto whichever sessions it happens to be watching.
func TestAWorkspaceEventArrivesOnTheWorkspaceAddress(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	go ServeConn(t.Context(), server, svc, ServerHello("terva-test", "0"))

	push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation}, Features: []string{FeatureWorkspaceEvents}}))
	if sh := pull(t, client); sh.Kind != KindHello {
		t.Fatalf("expected server hello, got %+v", sh)
	}
	// Subscribed to a session AND to the workspace: the interesting case, because
	// this is where a double-delivery bug would show up.
	push(t, client, mustCmd(t, 1, "s1", MethodSubscribe, nil))
	if r := pull(t, client); r.Kind != KindResp || r.Error != nil {
		t.Fatalf("session subscribe: %+v", r)
	}
	push(t, client, mustCmd(t, 2, AddrWorkspace, MethodSubscribe, nil))
	if r := pull(t, client); r.Kind != KindResp || r.Error != nil {
		t.Fatalf("workspace subscribe: %+v", r)
	}

	svc.broadcast(AddrWorkspace, SurfaceUpdatedEvent("tasks"))

	f := pullEvent(t, client, EventSurfaceUpdated)
	if f.Sess != AddrWorkspace {
		t.Errorf("workspace event addressed to %q, want %q", f.Sess, AddrWorkspace)
	}

	// And exactly once. A second copy stamped with the session id would mean the
	// compat relay was running for a client that had negotiated the feature.
	if extra, ok := tryPullEvent(client, EventSurfaceUpdated, 300*time.Millisecond); ok {
		t.Errorf("the workspace event arrived twice (second copy addressed to %q)", extra.Sess)
	}
}

// A client that does NOT speak the address keeps working exactly as it did
// before the address existed: the server stamps a copy with each session the
// client is subscribed to. This is the compatibility shim that lets the
// workspace publish once without a flag day.
func TestAnOldClientStillGetsWorkspaceEventsOnItsSessions(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	go ServeConn(t.Context(), server, svc, ServerHello("terva-test", "0"))

	// No workspace-events feature — an old bundle, or an older `terva attach`.
	push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation}}))
	if sh := pull(t, client); sh.Kind != KindHello {
		t.Fatalf("expected server hello, got %+v", sh)
	}
	push(t, client, mustCmd(t, 1, "s1", MethodSubscribe, nil))
	if r := pull(t, client); r.Kind != KindResp || r.Error != nil {
		t.Fatalf("subscribe: %+v", r)
	}

	svc.broadcast(AddrWorkspace, SurfaceUpdatedEvent("tasks"))

	f := pullEvent(t, client, EventSurfaceUpdated)
	if f.Sess != "s1" {
		t.Errorf("event addressed to %q, want the subscribed session %q — an old client "+
			"demuxes on the session id and would drop anything else", f.Sess, "s1")
	}
}

// ...and it may not subscribe to the address it has not agreed to understand,
// because the server is already relaying those events into its sessions. Letting
// it through would deliver every workspace event twice.
func TestSubscribingToTheWorkspaceRequiresNegotiatingIt(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	go ServeConn(t.Context(), server, svc, ServerHello("terva-test", "0"))

	push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation}}))
	pull(t, client)

	push(t, client, mustCmd(t, 1, AddrWorkspace, MethodSubscribe, nil))
	r := pull(t, client)
	if r.Kind != KindResp || r.Error == nil {
		t.Fatalf("subscribe to #workspace without the feature = %+v, want an error", r)
	}
	if r.Error.Code != CodeUnsupported {
		t.Errorf("error code %q, want %q", r.Error.Code, CodeUnsupported)
	}
}

// The reserved namespace is closed: #workspace is the only address in it, and an
// unknown one is refused rather than quietly treated as a session.
func TestAnUnknownReservedAddressIsRefused(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	go ServeConn(t.Context(), server, svc, ServerHello("terva-test", "0"))

	push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation}, Features: []string{FeatureWorkspaceEvents}}))
	pull(t, client)

	push(t, client, mustCmd(t, 1, "#fleet", MethodSubscribe, nil))
	r := pull(t, client)
	if r.Kind != KindResp || r.Error == nil {
		t.Fatalf("subscribe to an unknown reserved address = %+v, want an error", r)
	}
	if r.Error.Code != CodeNotFound {
		t.Errorf("error code %q, want %q", r.Error.Code, CodeNotFound)
	}
}

// auth.providers is session-INDEPENDENT: a credential belongs to the daemon, not
// to a conversation. It must answer with no session addressed at all, because a
// client asking "why is my model list short" has no reason to name one.
func TestAuthProvidersIsAnsweredWithoutASession(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	go ServeConn(t.Context(), server, svc, ServerHello("terva-test", "0"))

	push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupSession}}))
	pull(t, client)

	push(t, client, mustCmd(t, 1, "", MethodAuthProviders, nil))
	r := pull(t, client)
	if r.Kind != KindResp || r.Error != nil {
		t.Fatalf("auth.providers = %+v, want a result", r)
	}
	var view ProvidersView
	if err := r.BindResult(&view); err != nil {
		t.Fatalf("bind: %v", err)
	}
	if len(view.Providers) == 0 {
		t.Fatal("no providers reported")
	}
}

// It is in the SESSION group, not control: it grants no authority, and a client
// that can read your transcripts already learns your provider from the first
// usage event. The verbs that CHANGE a credential will be a separate group.
func TestAuthProvidersIsNotAControlVerb(t *testing.T) {
	if g := MethodAuthProviders.Group(); g != GroupSession {
		t.Errorf("auth.providers is in group %q, want %q", g, GroupSession)
	}
}

func TestIsReservedAddr(t *testing.T) {
	for _, s := range []string{AddrWorkspace, "#fleet", "#"} {
		if !IsReservedAddr(s) {
			t.Errorf("IsReservedAddr(%q) = false", s)
		}
	}
	// A session id is a file-name stem. None of these are addresses.
	for _, s := range []string{"", "s1", "2026-07-13T10-00-00", "a#b"} {
		if IsReservedAddr(s) {
			t.Errorf("IsReservedAddr(%q) = true — a session id was mistaken for an address", s)
		}
	}
}

// pullEvent reads frames until one carries the wanted event type.
func pullEvent(t *testing.T, c *memConn, typ string) Frame {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("event %q never arrived", typ)
		default:
		}
		f := pull(t, c)
		if f.Kind == KindEvent && f.Event != nil && f.Event.Type == typ {
			return f
		}
	}
}

// tryPullEvent reports whether another matching event shows up within d. Used to
// assert an ABSENCE — that a negotiating client is not also served the compat
// copy — so the window only has to be long enough for a duplicate that is
// emitted synchronously with the first.
func tryPullEvent(c *memConn, typ string, d time.Duration) (Frame, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	for {
		f, err := c.ReadFrame(ctx)
		if err != nil {
			return Frame{}, false
		}
		if f.Kind == KindEvent && f.Event != nil && f.Event.Type == typ {
			return f, true
		}
	}
}
