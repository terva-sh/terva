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
	r.mu.Unlock()
	if down {
		return nil, errors.New("ctrlclient: not connected")
	}
	return r.fakeCarrier.SubscribeReliable(ctx, sess)
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
