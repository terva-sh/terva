package ctrlproto

import (
	"encoding/json"
	"testing"
)

// A room event arrives on the room's own address, for a client that
// negotiated the talkoot group.
func TestATalkootEventArrivesOnTheRoomAddress(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	hello := ServerHello("terva-test", "0")
	hello.Groups = append(hello.Groups, GroupTalkoot)
	go ServeConn(t.Context(), server, svc, hello)

	push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation, GroupTalkoot}}))
	if sh := pull(t, client); sh.Kind != KindHello {
		t.Fatalf("expected server hello, got %+v", sh)
	}
	addr := TalkootAddr("crew")
	push(t, client, mustCmd(t, 1, addr, MethodSubscribe, nil))
	if r := pull(t, client); r.Kind != KindResp || r.Error != nil {
		t.Fatalf("subscribe to %s: %+v", addr, r)
	}

	svc.broadcast(addr, TalkootEnvelopeEvent("crew", TalkootLine{Type: "envelope",
		Envelope: &TalkootEnvelope{ID: "e1", From: "human:drew", To: []string{"lead"}, Kind: "message", Body: "go"}}))

	f := pullEvent(t, client, EventTalkootEnvelope)
	if f.Sess != addr {
		t.Errorf("event addressed to %q, want %q", f.Sess, addr)
	}
	if f.Event == nil || f.Event.Talkoot == nil || f.Event.Talkoot.Line == nil || f.Event.Talkoot.Line.Envelope == nil {
		t.Fatalf("the event carries no envelope: %+v", f.Event)
	}
	if got := f.Event.Talkoot.Line.Envelope.Body; got != "go" {
		t.Errorf("envelope body %q, want %q", got, "go")
	}
}

// A client that did not negotiate the group may not watch a room, the same
// rule that refuses it the group's verbs.
func TestSubscribingToARoomRequiresTheTalkootGroup(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	hello := ServerHello("terva-test", "0")
	hello.Groups = append(hello.Groups, GroupTalkoot)
	go ServeConn(t.Context(), server, svc, hello)

	push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation, GroupSession}, Features: []string{FeatureWorkspaceEvents}}))
	pull(t, client)

	push(t, client, mustCmd(t, 1, TalkootAddr("crew"), MethodSubscribe, nil))
	r := pull(t, client)
	if r.Kind != KindResp || r.Error == nil {
		t.Fatalf("subscribe to a room without the group = %+v, want an error", r)
	}
	if r.Error.Code != CodeUnsupported {
		t.Errorf("error code %q, want %q", r.Error.Code, CodeUnsupported)
	}
	svc.mu.Lock()
	n := len(svc.subs[TalkootAddr("crew")])
	svc.mu.Unlock()
	if n != 0 {
		t.Error("the refused subscription still reached the service")
	}
}

// "#talkoot:" names no talkoot, so it is an unknown reserved address.
func TestARoomAddressWithNoIDIsRefused(t *testing.T) {
	svc := newFakeSvc()
	client, server := newMemPair()
	hello := ServerHello("terva-test", "0")
	hello.Groups = append(hello.Groups, GroupTalkoot)
	go ServeConn(t.Context(), server, svc, hello)

	push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol,
		Groups: []Group{GroupConversation, GroupTalkoot}}))
	pull(t, client)

	push(t, client, mustCmd(t, 1, AddrTalkootPrefix, MethodSubscribe, nil))
	r := pull(t, client)
	if r.Kind != KindResp || r.Error == nil || r.Error.Code != CodeNotFound {
		t.Fatalf("subscribe to %q = %+v, want %s", AddrTalkootPrefix, r, CodeNotFound)
	}
}

func TestTalkootFromAddr(t *testing.T) {
	if id, ok := TalkootFromAddr(TalkootAddr("crew")); !ok || id != "crew" {
		t.Errorf("TalkootFromAddr(TalkootAddr(crew)) = %q, %v", id, ok)
	}
	for _, s := range []string{"", "crew", AddrWorkspace, AddrTalkootPrefix, "#talkoot", "x#talkoot:crew"} {
		if id, ok := TalkootFromAddr(s); ok {
			t.Errorf("TalkootFromAddr(%q) = %q, true", s, id)
		}
	}
	if !IsReservedAddr(TalkootAddr("crew")) {
		t.Error("a room address is not reserved, so a workspace would read it as a session id")
	}
}

// 🚨 The steer bit is the one thing between a caller that may prompt and a
// caller that may run a team. A mask of read, write, and spend is every
// capability a role held before the bit existed, and it must not reach a verb
// that changes or wakes a talkoot.
func TestTheSteerVerbsNeedTheSteerBit(t *testing.T) {
	steer := []Method{MethodTalkootCreate, MethodTalkootUpdate, MethodTalkootPost, MethodTalkootPause, MethodTalkootResume,
		MethodTalkootPropose, MethodTalkootDecide, MethodTalkootRecruit}
	for _, m := range steer {
		if m.Capabilities()&CapSteer == 0 {
			t.Errorf("%s does not need CapSteer", m)
		}
		if m.Permits(CapRead | CapWrite | CapSpend) {
			t.Errorf("%s is permitted without CapSteer", m)
		}
		if !m.Permits(capAll) {
			t.Errorf("%s is refused to an unrestricted caller", m)
		}
	}
	for _, m := range []Method{MethodTalkootList, MethodTalkootGet, MethodTalkootRoom, MethodTalkootInbox, MethodTalkootProposals} {
		if !m.Permits(CapRead) {
			t.Errorf("%s is refused to a read-only caller, who may watch a room", m)
		}
	}
	// Posting wakes a member, so it spends as well as steers. So does an
	// approval, which releases what the old roster held. A proposal names a
	// person as its author, a claim only a caller that could update the
	// roster may make.
	for _, m := range []Method{MethodTalkootPost, MethodTalkootDecide, MethodTalkootPropose} {
		if m.Permits(CapRead | CapWrite | CapSteer) {
			t.Errorf("%s is permitted without CapSpend", m)
		}
	}
}

// Steer rides on a class. A verb that only reads cannot steer, because the
// read class promises the verb changes nothing.
func TestNoSteerVerbIsReadOnly(t *testing.T) {
	for m := range steerMethods {
		if readOnlyMethods[m] {
			t.Errorf("%s is classified read-only and steer", m)
		}
		if !writeOnlyMethods[m] && !spendingMethods[m] {
			t.Errorf("%s steers but has no write or spend class", m)
		}
	}
}

func TestUnknownMethodNeedsTheSteerBit(t *testing.T) {
	if Method("nobody.classified.this").Capabilities()&CapSteer == 0 {
		t.Error("an unclassified verb is reachable without CapSteer")
	}
}

// The workspace announces a new talkoot to every subscriber, and the edge
// keeps the announcement from a client that cannot answer it.
func TestTalkootsChangedReachesOnlyTheGroup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		groups []Group
		want   bool
	}{
		{"with the group", []Group{GroupConversation, GroupTalkoot}, true},
		{"without it", []Group{GroupConversation}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFakeSvc()
			client, server := newMemPair()
			hello := ServerHello("terva-test", "0")
			hello.Groups = append(hello.Groups, GroupTalkoot)
			go ServeConn(t.Context(), server, svc, hello)

			push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol,
				Groups: tc.groups, Features: []string{FeatureWorkspaceEvents}}))
			pull(t, client)
			push(t, client, mustCmd(t, 1, AddrWorkspace, MethodSubscribe, nil))
			if r := pull(t, client); r.Error != nil {
				t.Fatalf("workspace subscribe: %+v", r.Error)
			}

			svc.broadcast(AddrWorkspace, TalkootsChangedEvent())
			// A positive control: an event every client gets, sent after.
			svc.broadcast(AddrWorkspace, SurfaceUpdatedEvent("tasks"))
			f := pull(t, client)
			for f.Kind != KindEvent {
				f = pull(t, client)
			}
			got := f.Event != nil && f.Event.Type == EventTalkootsChanged
			if got != tc.want {
				t.Errorf("first workspace event = %+v, want talkoots_changed %v", f.Event, tc.want)
			}
			if !got {
				if f.Event == nil || f.Event.Type != EventSurfaceUpdated {
					t.Errorf("the control event did not arrive: %+v", f.Event)
				}
			}
		})
	}
}

// 🚨 A room streams the envelopes talkoot.room pages, bodies and all. A
// connection provisioned with an exact verb set must not read a room through
// subscribe when its set leaves talkoot.room out.
func TestARoomNeedsTalkootRoomOnAProvisionedConnection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		methods []Method
		want    string
	}{
		{"subscribe alone", []Method{MethodSubscribe}, CodeForbidden},
		{"subscribe and talkoot.room", []Method{MethodSubscribe, MethodTalkootRoom}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFakeSvc()
			client, server := newMemPair()
			hello := ServerHello("terva-test", "0")
			hello.Groups = append(hello.Groups, GroupTalkoot)
			go ServeConn(t.Context(), server, svc, hello, WithMethods(tc.methods...))

			push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol,
				Groups: []Group{GroupConversation, GroupTalkoot}}))
			pull(t, client)
			push(t, client, mustCmd(t, 1, TalkootAddr("crew"), MethodSubscribe, nil))
			r := pull(t, client)
			got := ""
			if r.Error != nil {
				got = r.Error.Code
			}
			if got != tc.want {
				t.Errorf("subscribe to a room answered %q (%+v), want %q", got, r.Error, tc.want)
			}
		})
	}
}

// 🚨 An inbox card carries a member's tool call and its preview, which is what
// talkoot.inbox reads. A connection provisioned to watch a room, and not to
// read the inbox, must not get the cards on the room's address.
func TestAnInboxCardNeedsTalkootInboxOnAProvisionedConnection(t *testing.T) {
	for _, tc := range []struct {
		name    string
		methods []Method
		want    bool
	}{
		{"the room alone", []Method{MethodSubscribe, MethodTalkootRoom}, false},
		{"the room and the inbox", []Method{MethodSubscribe, MethodTalkootRoom, MethodTalkootInbox}, true},
		{"every verb", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFakeSvc()
			client, server := newMemPair()
			hello := ServerHello("terva-test", "0")
			hello.Groups = append(hello.Groups, GroupTalkoot)
			var opts []ServeOption
			if tc.methods != nil {
				opts = append(opts, WithMethods(tc.methods...))
			}
			go ServeConn(t.Context(), server, svc, hello, opts...)

			push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol,
				Groups: []Group{GroupConversation, GroupTalkoot}}))
			pull(t, client)
			room := TalkootAddr("crew")
			push(t, client, mustCmd(t, 1, room, MethodSubscribe, nil))
			if r := pull(t, client); r.Error != nil {
				t.Fatalf("subscribe to the room: %+v", r.Error)
			}

			card := TalkootCard{Session: "s1", Member: "helm", Kind: TalkootCardPermission, ID: "c1",
				Permission: &PermissionRequest{CallID: "c1", Tool: "bash", Preview: "rm -rf build"}}
			svc.broadcast(room, TalkootInboxEvent("crew", card))
			// A positive control: a room event every watcher gets, sent after.
			svc.broadcast(room, TalkootStatusEvent("crew", nil))
			f := pull(t, client)
			for f.Kind != KindEvent {
				f = pull(t, client)
			}
			got := f.Event != nil && f.Event.Type == EventTalkootInbox
			if got != tc.want {
				t.Errorf("first room event = %+v, want talkoot_inbox %v", f.Event, tc.want)
			}
			if !got && (f.Event == nil || f.Event.Type != EventTalkootStatus) {
				t.Errorf("the control event did not arrive: %+v", f.Event)
			}
		})
	}
}

// A room is read authority. subscribe is classified read-only, and handle
// checks the caller's mask before it routes subscribe, so a caller without
// CapRead never reaches the room gate. This pins that order for the room.
func TestARoomNeedsReadAuthority(t *testing.T) {
	for _, tc := range []struct {
		name string
		mask Capability
		want string
	}{
		{"write without read", CapWrite | CapSpend | CapSteer, CodeForbidden},
		{"read alone", CapRead, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newFakeSvc()
			client, server := newMemPair()
			hello := ServerHello("terva-test", "0")
			hello.Groups = append(hello.Groups, GroupTalkoot)
			go ServeConn(t.Context(), server, svc, hello, WithAuthority(tc.mask))

			push(t, client, HelloFrame(Hello{Role: RoleClient, Protocol: Protocol,
				Groups: []Group{GroupConversation, GroupTalkoot}}))
			pull(t, client)
			push(t, client, mustCmd(t, 1, TalkootAddr("crew"), MethodSubscribe, nil))
			r := pull(t, client)
			got := ""
			if r.Error != nil {
				got = r.Error.Code
			}
			if got != tc.want {
				t.Errorf("subscribe to a room answered %q (%+v), want %q", got, r.Error, tc.want)
			}
		})
	}
}

// sessions.create needs only CapWrite, so it must not bind a recruiter, which
// files roster proposals. The field never crosses the wire, and
// talkoot.recruit, a steer verb, is the only door to one.
func TestSessionsCreateCannotBindARecruiter(t *testing.T) {
	var o CreateOpts
	if err := json.Unmarshal([]byte(`{"persona":"hautoja","recruit":"crew"}`), &o); err != nil {
		t.Fatal(err)
	}
	if o.Recruit != "" {
		t.Fatalf("sessions.create decoded recruit %q from the wire", o.Recruit)
	}
	if MethodSessionCreate.Capabilities()&CapSteer != 0 {
		t.Fatal("sessions.create needs CapSteer; the recruiter door can move back to it")
	}
	if MethodTalkootRecruit.Permits(CapRead | CapWrite | CapSpend) {
		t.Fatal("talkoot.recruit is open to a caller without CapSteer")
	}
}
