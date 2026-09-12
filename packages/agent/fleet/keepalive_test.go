package fleet

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// wsPair stands up one real WebSocket and hands back both ends. The keepalive
// is control-frame machinery, so a pipe or a fake will not do: gorilla decides
// when a ping is answered and when a read deadline fires, and those are the
// behaviours under test.
func wsPair(t *testing.T) (server, client *websocket.Conn) {
	t.Helper()
	up := &websocket.Upgrader{}
	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		accepted <- c
	}))
	t.Cleanup(srv.Close)

	c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	select {
	case s := <-accepted:
		t.Cleanup(func() { _ = s.Close() })
		return s, c
	case <-time.After(5 * time.Second):
		t.Fatal("the server never accepted the websocket")
		return nil, nil
	}
}

// eventually polls until cond holds, and fails with why when it never does.
// The keepalive is a race against timers by nature, so a fixed sleep would
// either be flaky or slow. This is neither.
func eventually(t *testing.T, within time.Duration, why string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("after %v: %s", within, why)
}

// TestTheHubPingsAnIdleMemberSocket covers the first criterion.
//
// The interval is injected rather than waited out, which is the whole reason
// keepaliveEvery exists next to keepalive. A test that sat out the production
// 20 seconds would be a test nobody runs.
func TestTheHubPingsAnIdleMemberSocket(t *testing.T) {
	server, client := wsPair(t)

	pinged := make(chan struct{}, 8)
	client.SetPingHandler(func(string) error {
		select {
		case pinged <- struct{}{}:
		default:
		}
		return nil
	})
	// gorilla processes control frames inside a read call, so the far end has
	// to be reading or the ping is never seen. This is the same reason the
	// member's own deadline has to be pushed by an inbound ping.
	go func() {
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	w := &wsConn{c: server}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.keepaliveEvery(ctx, 5*time.Millisecond)

	select {
	case <-pinged:
	case <-time.After(5 * time.Second):
		t.Fatal("an idle socket was never pinged: a proxy reading silence as death would cut it, which is the incident web/conn.go records")
	}
}

// TestASilentMemberIsReapedByTheReadDeadline covers the second criterion.
//
// The far end here is the dangerous shape, not a closed socket: it is open,
// the process is alive, and it answers nothing. That is what a dead tunnel
// looks like from the hub, and it is precisely the case the reconnect loop
// cannot see, because ctrlclient.Run only re-dials when a connection errors.
func TestASilentMemberIsReapedByTheReadDeadline(t *testing.T) {
	server, client := wsPair(t)

	// The client never reads, so gorilla never auto-pongs for it, and never
	// writes. From the hub's side it is indistinguishable from a host that
	// lost power with the socket still open.
	_ = client

	w := &wsConn{c: server, pongWait: 60 * time.Millisecond}
	w.armReadDeadline()

	// ReadFrame is bounded here rather than called straight, and that is the
	// point of the test rather than caution. Without the deadline this read
	// never returns, so a plain call would hang until go test gives up at ten
	// minutes and prints a panic dump instead of the failure below. A test whose
	// regression costs ten minutes and says nothing is worse than no test.
	done := make(chan error, 1)
	go func() {
		_, err := w.ReadFrame(context.Background())
		done <- err
	}()

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("ReadFrame never returned: a silent peer is being held open. That is the unbounded wait this whole ticket exists to close, and with no read deadline the only bound left is a TCP keepalive, about two hours on a Linux default")
	}
	if err == nil {
		t.Fatal("ReadFrame returned a frame and no error from a peer that sent nothing")
	}
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("want the read deadline to fire, got %v", err)
	}
}

// TestAHealthyMemberIsNotReapedWhileItPongs is the other half of the previous
// test, and without it that one proves far less than it looks.
//
// A read deadline that reaps everything would pass the reap test perfectly.
// This pins that the deadline is pushed back by the pong the hub's own ping
// draws, so a member that is merely idle survives.
func TestAHealthyMemberIsNotReapedWhileItPongs(t *testing.T) {
	server, client := wsPair(t)

	// A live member: reading, and therefore ponging, but sending no frames.
	go func() {
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				return
			}
		}
	}()

	w := &wsConn{c: server, pongWait: 120 * time.Millisecond}
	w.armReadDeadline()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.keepaliveEvery(ctx, 15*time.Millisecond)

	// Well past the deadline, and past several ping intervals.
	done := make(chan error, 1)
	go func() {
		_, err := w.ReadFrame(context.Background())
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("an idle but answering member was reaped anyway: %v", err)
	case <-time.After(400 * time.Millisecond):
		// Survived. The pongs pushed the deadline out.
	}
}

// TestAReapedMemberRedialsAndRejoins covers the third criterion, which is the
// one that decides whether this feature helps or hurts.
//
// A reap that only removed a member would shrink the fleet on the first dead
// tunnel and never give it back. PongWait below pingInterval makes the hub reap
// even a healthy member, which is how a reap happens on demand here.
func TestAReapedMemberRedialsAndRejoins(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hub, url := startHub(t, ctx, HubOptions{PongWait: 60 * time.Millisecond})
	startMember(t, ctx, url, "neot", newFakeSvc(ctrlproto.SessionInfo{ID: "s1"}))

	// It arrives at least once.
	eventually(t, 10*time.Second, "the member never checked in at all", func() bool {
		c, ok := hub.Client("neot")
		return ok && c.Connected()
	})

	// And it keeps coming back. dialCount rises every time the hub's client
	// re-enters dial, so a climbing count is reap and rejoin, repeatedly.
	first := hub.dialCount("neot")
	eventually(t, 15*time.Second, "the member was reaped and never rejoined, so the reap shrank the fleet instead of healing it", func() bool {
		return hub.dialCount("neot") >= first+2
	})

	// Not merely re-dialling: actually connected again on the far side of a reap.
	eventually(t, 10*time.Second, "the member re-dialled but never completed another handshake", func() bool {
		c, ok := hub.Client("neot")
		return ok && c.Connected()
	})
}

// TestAMemberThatStaysAwayLeavesTheAggregateSources covers the fourth
// criterion: a machine that is gone must not read as live.
//
// Hub members stay listed once they have checked in, deliberately, because the
// hub parks a client waiting for each one to come back. Sources therefore has
// to ask whether a member is connected NOW, or a host that lost power keeps
// appearing in the fleet for as long as the hub runs.
func TestAMemberThatStaysAwayLeavesTheAggregateSources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hub, url := startHub(t, ctx, HubOptions{})

	// The member gets its own context, so it can be stopped for good rather
	// than reconnecting the way a live one would.
	memberCtx, stopMember := context.WithCancel(ctx)
	startMember(t, memberCtx, url, "neot", newFakeSvc(ctrlproto.SessionInfo{ID: "s1"}))

	agg, err := NewAggregate(hub, newFakeSvc(ctrlproto.SessionInfo{ID: "local1"}), LocalOrigin)
	if err != nil {
		t.Fatalf("NewAggregate: %v", err)
	}

	eventually(t, 10*time.Second, "the member never became a source", func() bool {
		return hasOrigin(agg.Sources(), "neot")
	})

	stopMember()

	eventually(t, 10*time.Second, "a member that went away is still listed as a source, so a dead machine reads as live", func() bool {
		return !hasOrigin(agg.Sources(), "neot")
	})

	// The local workspace is untouched by any of this. Without this check the
	// test would also pass if Sources had simply stopped returning anything.
	if !hasOrigin(agg.Sources(), LocalOrigin) {
		t.Fatal("the local workspace stopped being a source too, so Sources is not filtering, it is empty")
	}

	// The origin is still KNOWN, which is the distinction Route depends on.
	// Reporting an unknown origin for a member the hub is expecting back would
	// send a reader hunting a typo instead of a reconnect.
	if _, _, err := agg.Route("neot/s1"); err != nil {
		t.Fatalf("Route stopped recognising a parked member: %v", err)
	}
}

func hasOrigin(srcs []Source, origin string) bool {
	for _, s := range srcs {
		if s.Origin == origin {
			return true
		}
	}
	return false
}
