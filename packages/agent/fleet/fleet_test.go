package fleet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/core"
)

const testToken = "fleet-test-token"

// fakeSvc is the member's workspace. It embeds the interface rather than
// implementing all 49 methods: a test that reaches an unimplemented one panics
// with the method name, which is a better failure than a silent zero value.
type fakeSvc struct {
	ctrlproto.WorkspaceService

	mu       sync.Mutex
	sessions []ctrlproto.SessionInfo
	events   chan ctrlproto.Event

	subscribed chan struct{}
}

func newFakeSvc(sessions ...ctrlproto.SessionInfo) *fakeSvc {
	return &fakeSvc{sessions: sessions, subscribed: make(chan struct{}, 8)}
}

func (f *fakeSvc) Sessions(context.Context) ([]ctrlproto.SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ctrlproto.SessionInfo(nil), f.sessions...), nil
}

func (f *fakeSvc) Subscribe(_ context.Context, _ string) (<-chan ctrlproto.Event, error) {
	ch := make(chan ctrlproto.Event, 16)
	f.mu.Lock()
	f.events = ch
	f.mu.Unlock()
	select {
	case f.subscribed <- struct{}{}:
	default:
	}
	return ch, nil
}

// emit offers one event to the member's live subscription.
func (f *fakeSvc) emit(ev ctrlproto.Event) bool {
	f.mu.Lock()
	ch := f.events
	f.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- ev:
		return true
	default:
		return false
	}
}

// startHub runs a hub behind httptest and returns its ws:// base URL.
func startHub(t *testing.T, ctx context.Context, opts HubOptions) (*Hub, string) {
	t.Helper()
	if opts.Token == "" {
		opts.Token = testToken
	}
	if opts.Hello.Agent == "" {
		opts.Hello = ctrlproto.Hello{Agent: "terva-hub", Version: "test"}
	}
	if opts.Backoff == 0 {
		opts.Backoff = 20 * time.Millisecond
	}
	hub, err := NewHub(opts)
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	srv := httptest.NewServer(hub.Handler(ctx))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { _ = hub.Close() })
	return hub, "ws" + strings.TrimPrefix(srv.URL, "http")
}

func startMember(t *testing.T, ctx context.Context, hubURL, origin string, svc ctrlproto.WorkspaceService) {
	t.Helper()
	m, err := NewMember(MemberOptions{
		HubURL:  hubURL,
		Token:   testToken,
		Origin:  origin,
		Service: svc,
		Hello:   ctrlproto.ServerHello("terva-member", "test"),
		Backoff: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewMember: %v", err)
	}
	go func() { _ = m.Run(ctx) }()
}

// TestAMemberDialsInAndTheHubDrivesItOverThatSocket is the carrier itself:
// the member opens the socket, the hub drives it, and the wire is unchanged.
// Handshake, one command, and one event, all in the direction the ticket asks
// for.
func TestAMemberDialsInAndTheHubDrivesItOverThatSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up := make(chan ctrlproto.Hello, 4)
	hub, hubURL := startHub(t, ctx, HubOptions{
		OnMemberUp: func(_ string, server ctrlproto.Hello) { up <- server },
	})

	svc := newFakeSvc(ctrlproto.SessionInfo{ID: "abc123", Model: "m1"})
	startMember(t, ctx, hubURL, "neot", svc)

	var server ctrlproto.Hello
	select {
	case server = <-up:
	case <-time.After(10 * time.Second):
		t.Fatal("the member never completed the handshake. A hang here rather than " +
			"an error is the signature of both ends reading: check who writes hello first")
	}
	if server.Agent != "terva-member" {
		t.Errorf("server hello names %q, want terva-member", server.Agent)
	}

	client, ok := hub.Client("neot")
	if !ok {
		t.Fatal("the hub has no client for neot after a completed check-in")
	}

	var got ctrlproto.SessionsResult
	if err := client.Call(ctx, "", ctrlproto.MethodSessionsList, nil, &got); err != nil {
		t.Fatalf("sessions.list over the reverse-dialled socket: %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ID != "abc123" {
		t.Fatalf("sessions.list returned %+v, want the member's abc123", got.Sessions)
	}

	events, err := client.Subscribe(ctx, "abc123")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	select {
	case <-svc.subscribed:
	case <-time.After(10 * time.Second):
		t.Fatal("the subscribe never reached the member")
	}

	const marker = "fleet-test-event"
	if !svc.emit(ctrlproto.Event{WireEvent: core.WireEvent{Type: marker}}) {
		t.Fatal("the member had no live subscription to emit into")
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("the hub's subscription closed before the event arrived")
			}
			if ev.Type == marker {
				return
			}
		case <-deadline:
			t.Fatal("the event never crossed the reverse-dialled socket")
		}
	}
}

// TestTheHubParksRatherThanSpinningWhenAMemberIsAway is the reconnect
// decision, asserted rather than described.
//
// ctrlclient.Run dials again after every drop, forever. A hub cannot re-dial
// an inbound member, so a dial that returned an error would spin at the
// backoff cadence. The backoff here is 10ms, so a spinning loop racks up
// roughly fifty dials in the half second this test waits. A parked one sits at
// exactly two: the dial that connected, and the dial now waiting for the
// member to come back.
func TestTheHubParksRatherThanSpinningWhenAMemberIsAway(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up := make(chan ctrlproto.Hello, 4)
	down := make(chan error, 8)
	hub, hubURL := startHub(t, ctx, HubOptions{
		Backoff:      10 * time.Millisecond,
		OnMemberUp:   func(_ string, server ctrlproto.Hello) { up <- server },
		OnMemberDown: func(_ string, err error) { down <- err },
	})

	memberCtx, stopMember := context.WithCancel(ctx)
	startMember(t, memberCtx, hubURL, "neot", newFakeSvc())

	select {
	case <-up:
	case <-time.After(10 * time.Second):
		t.Fatal("the member never checked in")
	}
	if n := hub.dialCount("neot"); n != 1 {
		t.Fatalf("hub dialled %d times to establish one connection, want 1", n)
	}

	stopMember()
	select {
	case <-down:
	case <-time.After(10 * time.Second):
		t.Fatal("the hub never noticed the member go away")
	}

	// Fifty backoff intervals. A spinning loop cannot hide in this window.
	time.Sleep(500 * time.Millisecond)

	if n := hub.dialCount("neot"); n != 2 {
		t.Errorf("hub dialled %d times while the member was away, want exactly 2 "+
			"(the connect, then one parked dial). A larger number means the dial is "+
			"returning instead of blocking, and the loop is spinning against a member "+
			"that can never be dialled", n)
	}
}

// TestAMemberChecksInAgainAndTheHubResumes covers the round trip the parked
// dial exists for. The hub is never restarted and its client is never
// replaced: the same object serves the member before and after.
func TestAMemberChecksInAgainAndTheHubResumes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up := make(chan ctrlproto.Hello, 4)
	down := make(chan error, 8)
	hub, hubURL := startHub(t, ctx, HubOptions{
		Backoff:      10 * time.Millisecond,
		OnMemberUp:   func(_ string, server ctrlproto.Hello) { up <- server },
		OnMemberDown: func(_ string, err error) { down <- err },
	})

	memberCtx, stopMember := context.WithCancel(ctx)
	startMember(t, memberCtx, hubURL, "neot", newFakeSvc(ctrlproto.SessionInfo{ID: "first"}))

	select {
	case <-up:
	case <-time.After(10 * time.Second):
		t.Fatal("the member never checked in")
	}
	before, ok := hub.Client("neot")
	if !ok {
		t.Fatal("no client for neot after the first check-in")
	}

	stopMember()
	select {
	case <-down:
	case <-time.After(10 * time.Second):
		t.Fatal("the hub never noticed the member go away")
	}

	// A different process, the same origin: this is a member coming back,
	// not a second member.
	startMember(t, ctx, hubURL, "neot", newFakeSvc(ctrlproto.SessionInfo{ID: "second"}))
	select {
	case <-up:
	case <-time.After(10 * time.Second):
		t.Fatal("the hub never picked the member back up: the parked dial did not wake")
	}

	after, ok := hub.Client("neot")
	if !ok {
		t.Fatal("no client for neot after it checked in again")
	}
	if before != after {
		t.Error("the hub replaced the member's client instead of resuming it, " +
			"so every subscription and pending call was rebuilt rather than recovered")
	}

	var got ctrlproto.SessionsResult
	if err := after.Call(ctx, "", ctrlproto.MethodSessionsList, nil, &got); err != nil {
		t.Fatalf("sessions.list after the member returned: %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ID != "second" {
		t.Fatalf("sessions.list returned %+v, want the second member process's session", got.Sessions)
	}
}

// TestCheckMemberBindSafetyRefusesTheNetwork pins the compensating control
// that stands in for member identity until fleet-identity lands.
func TestCheckMemberBindSafetyRefusesTheNetwork(t *testing.T) {
	cases := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:8081", true},
		{"localhost:8081", true},
		{"[::1]:8081", true},
		{"unix:/tmp/terva-fleet.sock", true},
		{"0.0.0.0:8081", false},
		{"[::]:8081", false},
		{":8081", false},
		{"192.168.1.10:8081", false},
		{"100.73.27.67:8081", false},
		{"unix:", false},
	}
	for _, c := range cases {
		err := CheckMemberBindSafety(c.addr)
		if c.ok && err != nil {
			t.Errorf("CheckMemberBindSafety(%q) refused a confined address: %v", c.addr, err)
		}
		if !c.ok && err == nil {
			t.Errorf("CheckMemberBindSafety(%q) allowed a network-reachable bind "+
				"while member identity is a shared bearer token", c.addr)
		}
	}
}

// TestListenEnforcesBindSafety proves the refusal is in the bind path and not
// only in a function nobody calls. The loopback case is the positive control:
// without it, a Listen that refused everything would pass the first assertion.
func TestListenEnforcesBindSafety(t *testing.T) {
	if ln, err := Listen("0.0.0.0:0"); err == nil {
		_ = ln.Close()
		t.Error("Listen bound a wildcard address while member identity is a shared bearer token")
	}
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen refused a loopback bind: %v", err)
	}
	_ = ln.Close()
}

// TestTheMemberEndpointRefusesTheUnauthorized covers the only gate this
// milestone has. A bad bearer and an origin that would break a federated id
// are both turned away before any upgrade happens.
func TestTheMemberEndpointRefusesTheUnauthorized(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	up := make(chan ctrlproto.Hello, 4)
	hub, err := NewHub(HubOptions{
		Token:      testToken,
		Hello:      ctrlproto.Hello{Agent: "terva-hub", Version: "test"},
		Backoff:    20 * time.Millisecond,
		OnMemberUp: func(_ string, server ctrlproto.Hello) { up <- server },
	})
	if err != nil {
		t.Fatalf("NewHub: %v", err)
	}
	t.Cleanup(func() { _ = hub.Close() })
	srv := httptest.NewServer(hub.Handler(ctx))
	t.Cleanup(srv.Close)

	get := func(token, origin string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+MemberPath+"?"+OriginParam+"="+origin, nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("do: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		return resp.StatusCode
	}

	if code := get("wrong-token", "neot"); code != http.StatusUnauthorized {
		t.Errorf("a bad bearer got %d, want %d", code, http.StatusUnauthorized)
	}
	if code := get("", "neot"); code != http.StatusUnauthorized {
		t.Errorf("a missing bearer got %d, want %d", code, http.StatusUnauthorized)
	}
	if code := get(testToken, "neot%2Fextra"); code != http.StatusBadRequest {
		t.Errorf("an origin carrying the federated separator got %d, want %d", code, http.StatusBadRequest)
	}
	if code := get(testToken, ""); code != http.StatusBadRequest {
		t.Errorf("an empty origin got %d, want %d", code, http.StatusBadRequest)
	}

	// The positive control, and it has to be a real check-in rather than a
	// status code. A plain GET that clears both gates still gets 400 from the
	// upgrader itself, which is indistinguishable from the bad-origin refusal
	// above. So drive the actual carrier: if a valid member reaches the
	// handshake, the gates are letting the right callers through, and every
	// refusal above meant something.
	startMember(t, ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), "neot", newFakeSvc())
	select {
	case <-up:
	case <-time.After(10 * time.Second):
		t.Fatal("a valid member never got through: the gates are refusing everything, " +
			"so the refusals asserted above prove nothing")
	}
}
