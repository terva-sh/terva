package modes

// The pump's reconnecting-carrier posture (terva attach): a subscribe failure
// against a down transport retries with backoff and surfaces the outage,
// instead of the in-process fail-stop; the successful resubscribe announces
// the resync.

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
)

// reconnCarrier is a fakeCarrier whose transport can be "down": subscribe
// fails while down, and Reconnecting reports it — the attachCarrier shape.
type reconnCarrier struct {
	*fakeCarrier
	mu   sync.Mutex
	down bool
	// upOnFail brings the transport back inside a failing subscribe, so the
	// reconnect lands after the attempt failed and before the pump can ask
	// Reconnecting(). Under load that ordering is a race; this forces it.
	upOnFail bool
	// subscribed, when non-nil, receives the address of every subscribe that
	// succeeds, the workspace included, which fakeCarrier does not record.
	subscribed chan string
}

func (r *reconnCarrier) setDown(d bool) {
	r.mu.Lock()
	r.down = d
	r.mu.Unlock()
}

func (r *reconnCarrier) Reconnecting() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.down
}

func (r *reconnCarrier) SubscribeReliable(ctx context.Context, sess string) (<-chan ctrlproto.Event, error) {
	r.mu.Lock()
	down := r.down
	if down && r.upOnFail {
		r.down = false
	}
	r.mu.Unlock()
	if down {
		return nil, errors.New("ctrlclient: not connected")
	}
	ch, err := r.fakeCarrier.SubscribeReliable(ctx, sess)
	if err == nil && r.subscribed != nil {
		select {
		case r.subscribed <- sess:
		default:
		}
	}
	return ch, err
}

// startPump runs one of the carrier loops on its own goroutine and returns a
// channel that closes when the loop returns. Cleanup stops the loop and waits
// for it, so a test that overrides carrierResubscribeDelay before this call
// restores the value only after the loop has stopped reading it.
func startPump(t *testing.T, loop func(context.Context)) <-chan struct{} {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		loop(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return done
}

func statusErrOf(i *Interactive) string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.statusErr
}

func waitCond(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestPumpRetriesReconnectingCarrier(t *testing.T) {
	old := carrierResubscribeDelay
	carrierResubscribeDelay = 5 * time.Millisecond
	t.Cleanup(func() { carrierResubscribeDelay = old })

	i := newCtrlprotoTestInteractive()
	rc := &reconnCarrier{fakeCarrier: newFakeCarrier(), down: true}
	i.cfg.Carrier = rc
	i.cfg.CarrierSession = "s1"

	go i.runCarrierLoop(t.Context())

	// Down: the pump stays alive, shows the outage, and keeps retrying.
	waitCond(t, "outage status", func() bool {
		i.mu.Lock()
		defer i.mu.Unlock()
		return strings.Contains(i.statusErr, "reconnecting")
	})

	// Back up: the next retry subscribes and announces the resync.
	rc.setDown(false)
	if got := recv(t, rc.subs, "resubscribe after reconnect"); got != "s1" {
		t.Fatalf("resubscribed to %q, want s1", got)
	}
	waitCond(t, "resync status", func() bool {
		i.mu.Lock()
		defer i.mu.Unlock()
		return strings.Contains(i.statusOK, "resynced") && i.statusErr == ""
	})
}

// The transport can come back between a failed subscribe and the pump's
// Reconnecting() check. The carrier was down when the attempt failed, so the
// failure belongs to the outage and the pump must retry. A pump that asked only
// after the failure read "connected", took the in-process fail-stop, and never
// subscribed again. TestPumpRetriesReconnectingCarrier hit that window on loaded
// CI runners. This test hits it on every run.
func TestPumpRetriesWhenTheReconnectBeatsTheCheck(t *testing.T) {
	old := carrierResubscribeDelay
	carrierResubscribeDelay = 5 * time.Millisecond
	t.Cleanup(func() { carrierResubscribeDelay = old })

	i := newCtrlprotoTestInteractive()
	rc := &reconnCarrier{fakeCarrier: newFakeCarrier(), down: true, upOnFail: true}
	i.cfg.Carrier = rc
	i.cfg.CarrierSession = "s1"

	done := startPump(t, i.runCarrierLoop)
	select {
	case got := <-rc.subs:
		if got != "s1" {
			t.Fatalf("resubscribed to %q, want s1", got)
		}
	case <-done:
		t.Fatalf("the pump stopped on a subscribe that failed while the carrier was down: statusErr=%q", statusErrOf(i))
	case <-time.After(recvTimeout):
		t.Fatalf("timeout waiting for resubscribe after reconnect: statusErr=%q", statusErrOf(i))
	}
	waitCond(t, "resync status", func() bool {
		i.mu.Lock()
		defer i.mu.Unlock()
		return strings.Contains(i.statusOK, "resynced") && i.statusErr == ""
	})
}

// The workspace pump has the same check, and losing that race costs it more
// quietly: it takes the failure to mean the carrier has no workspace stream and
// stops, so every workspace-scoped event is lost for the rest of the attach.
func TestWorkspacePumpRetriesWhenTheReconnectBeatsTheCheck(t *testing.T) {
	old := carrierResubscribeDelay
	carrierResubscribeDelay = 5 * time.Millisecond
	t.Cleanup(func() { carrierResubscribeDelay = old })

	i := newCtrlprotoTestInteractive()
	rc := &reconnCarrier{
		fakeCarrier: newFakeCarrier(),
		down:        true,
		upOnFail:    true,
		subscribed:  make(chan string, 4),
	}
	i.cfg.Carrier = rc

	done := startPump(t, i.runWorkspaceLoop)
	select {
	case got := <-rc.subscribed:
		if got != ctrlproto.AddrWorkspace {
			t.Fatalf("subscribed to %q, want the workspace", got)
		}
	case <-done:
		t.Fatal("the workspace pump stopped on a subscribe that failed while the carrier was down")
	case <-time.After(recvTimeout):
		t.Fatal("timeout waiting for the workspace resubscribe after reconnect")
	}
}

// The other side of that line: a reconnecting carrier whose transport is up
// before and after a failed subscribe got the failure from a live connection,
// for example a session the daemon does not know. That stays the fail-stop, so
// the user sees the error and not a "reconnecting" status that never ends.
func TestPumpFailStopWhenAReconnectingCarrierStaysUp(t *testing.T) {
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	fc.subErr = errors.New("unknown session")
	i.cfg.Carrier = &reconnCarrier{fakeCarrier: fc}
	i.cfg.CarrierSession = "s1"

	done := startPump(t, i.runCarrierLoop)
	select {
	case <-done:
	case <-time.After(recvTimeout):
		t.Fatalf("the pump kept retrying a failure from a live connection: statusErr=%q", statusErrOf(i))
	}
	if se := statusErrOf(i); !strings.Contains(se, "subscribe failed") {
		t.Fatalf("statusErr = %q, want subscribe-failed", se)
	}
}

func TestPumpFailStopForInProcessCarrier(t *testing.T) {
	// A plain carrier (no ReconnectingCarrier) keeps the fail-stop: one
	// subscribe error ends the pump with the failure on the status line.
	i := newCtrlprotoTestInteractive()
	fc := newFakeCarrier()
	fc.subErr = errors.New("boom")
	i.cfg.Carrier = fc
	i.cfg.CarrierSession = "s1"

	done := make(chan struct{})
	go func() { i.runCarrierLoop(t.Context()); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pump did not fail-stop on an in-process subscribe error")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if !strings.Contains(i.statusErr, "subscribe failed") {
		t.Fatalf("statusErr = %q, want subscribe-failed", i.statusErr)
	}
}
