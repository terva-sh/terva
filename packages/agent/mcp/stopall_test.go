package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestStopAllStopsSlowServersAtOnce: a server that ignores stdin EOF holds its
// Close for the stdio grace. StopAll must stop several together in about one
// grace, not one grace per server, and must not report the stop as a crash.
func TestStopAllStopsSlowServersAtOnce(t *testing.T) {
	stub := buildStub(t)
	const n = 4
	servers := map[string]ServerConfig{}
	for i := 0; i < n; i++ {
		servers[fmt.Sprintf("slow%d", i)] = ServerConfig{Command: stub, Args: []string{"--hold-on-eof"}}
	}
	m := StartAll(context.Background(), &Config{Servers: servers}, "", nil)
	if w := m.Warnings(); len(w) != 0 {
		m.StopAll()
		t.Fatalf("warnings: %v", w)
	}
	var fired atomic.Int32
	m.SetOnToolsChanged(func() { fired.Add(1) })
	m.mu.Lock()
	var transports []*stdioTransport
	for _, cl := range m.clients {
		transports = append(transports, cl.transport.(*stdioTransport))
	}
	m.mu.Unlock()
	if len(transports) != n {
		m.StopAll()
		t.Fatalf("started %d servers, want %d", len(transports), n)
	}

	start := time.Now()
	m.StopAll()
	elapsed := time.Since(start)

	// The servers must really hold out, or a fast StopAll proves nothing.
	if elapsed < stdioCloseGrace {
		t.Fatalf("StopAll took %s, under the %s grace: the servers did not hold out", elapsed, stdioCloseGrace)
	}
	if elapsed >= 2*stdioCloseGrace {
		t.Fatalf("StopAll took %s for %d slow servers; one at a time takes about %s", elapsed, n, n*stdioCloseGrace)
	}
	for _, tr := range transports {
		select {
		case <-tr.waitDone:
		default:
			t.Error("StopAll returned before a server was reaped")
		}
	}

	// Give the watchers time to (wrongly) act on the intentional stop.
	time.Sleep(200 * time.Millisecond)
	if got := fired.Load(); got != 0 {
		t.Errorf("onToolsChanged fired %d time(s) on an intentional StopAll", got)
	}
	for _, w := range m.Warnings() {
		if strings.Contains(w, "unexpectedly") {
			t.Errorf("StopAll recorded a crash warning: %v", m.Warnings())
			break
		}
	}
}

// hungTransport is a transport whose Close does not return until released.
type hungTransport struct {
	*fakeTransport
	release chan struct{}
}

func (h *hungTransport) Close() {
	<-h.release
	h.fakeTransport.Close()
}

// TestStopAllAbandonsAStragglerAtTheDeadline: one client that never finishes
// stopping must not hold the others or the caller past the deadline.
func TestStopAllAbandonsAStragglerAtTheDeadline(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	hung := newClient("hung", &hungTransport{newFakeTransport(), release}, time.Minute)
	quick := newClient("quick", newFakeTransport(), time.Minute)

	const deadline = 200 * time.Millisecond
	start := time.Now()
	if stopConcurrently([]*Client{hung, quick}, deadline) {
		t.Fatal("stopConcurrently reported every client stopped while one still hung")
	}
	if elapsed := time.Since(start); elapsed > deadline+time.Second {
		t.Fatalf("stopConcurrently returned after %s; the deadline was %s", elapsed, deadline)
	}
	select {
	case <-quick.Done():
	case <-time.After(time.Second):
		t.Fatal("the hung client kept the quick one from stopping")
	}
}
