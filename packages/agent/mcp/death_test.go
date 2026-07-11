package mcp

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestManagerWithdrawsToolsWhenServerDies: when a server subprocess dies
// unexpectedly, the Manager must stop reporting it connected, stop publishing
// its tools, record a warning, and fire onToolsChanged so the host can rebuild
// the live registry. Restarting it restores everything.
func TestManagerWithdrawsToolsWhenServerDies(t *testing.T) {
	stub := buildStub(t)
	m := StartAll(context.Background(), &Config{Servers: map[string]ServerConfig{"alpha": {Command: stub}}}, "", nil)
	defer m.StopAll()

	changed := make(chan struct{}, 1)
	m.SetOnToolsChanged(func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	})

	// Baseline: one connected server, its 4 tools published.
	if got := len(m.Tools()); got != 4 {
		t.Fatalf("baseline tools = %d, want 4", got)
	}
	if st := m.Status(); len(st) != 1 || !st[0].Connected {
		t.Fatalf("baseline status = %+v, want 1 connected", st)
	}

	// Kill the subprocess out from under the manager — a server crash.
	m.mu.Lock()
	proc := m.clients["alpha"].cmd.Process
	m.mu.Unlock()
	if err := proc.Kill(); err != nil {
		t.Fatalf("kill stub: %v", err)
	}

	// The watcher must observe the death and ask the host to refresh.
	select {
	case <-changed:
	case <-time.After(10 * time.Second):
		t.Fatal("onToolsChanged never fired after server death")
	}

	// Status and discovery are now truthful: nothing connected, no tools.
	if tools := m.Tools(); len(tools) != 0 {
		t.Errorf("Tools() still lists %d tools from a dead server: %v", len(tools), tools)
	}
	if st := m.Status(); len(st) != 0 {
		t.Errorf("Status() still reports %d server(s) after death: %+v", len(st), st)
	}
	var sawWarn bool
	for _, w := range m.Warnings() {
		if strings.Contains(w, "alpha") && strings.Contains(w, "unexpectedly") {
			sawWarn = true
		}
	}
	if !sawWarn {
		t.Errorf("no crash warning recorded; warnings: %v", m.Warnings())
	}

	// Restart restores the server, its tools, and a fresh death watcher.
	if err := m.StartOne(context.Background(), "alpha", ServerConfig{Command: stub}, nil); err != nil {
		t.Fatalf("StartOne after death: %v", err)
	}
	if got := len(m.Tools()); got != 4 {
		t.Errorf("tools not restored after restart: %d, want 4", got)
	}
	if st := m.Status(); len(st) != 1 || !st[0].Connected {
		t.Errorf("status not restored after restart: %+v", st)
	}
}

// TestManagerStopDoesNotWarnAsCrash: an intentional StopOne removes the client
// from the map before stopping it, so the death watcher must recognise the exit
// as intentional — no crash warning, no onToolsChanged.
func TestManagerStopDoesNotWarnAsCrash(t *testing.T) {
	stub := buildStub(t)
	m := StartAll(context.Background(), &Config{Servers: map[string]ServerConfig{"alpha": {Command: stub}}}, "", nil)
	defer m.StopAll()

	var fired int32
	m.SetOnToolsChanged(func() { atomic.AddInt32(&fired, 1) })

	m.StopOne("alpha") // closes stdin, waits for exit — Done fires after this

	// Give the watcher a generous window to (incorrectly) act.
	time.Sleep(500 * time.Millisecond)

	if n := atomic.LoadInt32(&fired); n != 0 {
		t.Errorf("onToolsChanged fired %d time(s) on an intentional stop", n)
	}
	for _, w := range m.Warnings() {
		if strings.Contains(w, "unexpectedly") {
			t.Errorf("intentional StopOne recorded a crash warning: %v", m.Warnings())
			break
		}
	}
	if st := m.Status(); len(st) != 0 {
		t.Errorf("stopped server still reported in status: %+v", st)
	}
}
