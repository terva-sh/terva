package external

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/chat"
)

func awaitChild(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(8 * time.Second):
		t.Fatalf("%s did not complete", what)
	}
}

func childResult(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("connector caller remained blocked")
		return nil
	}
}

func TestBlockedConnectorLifecycle(t *testing.T) {
	for _, action := range []string{"cancel", "timeout", "shutdown"} {
		t.Run(action, func(t *testing.T) {
			p, _, warns := newTestProxy(t, "blocked-stdin")
			stoppedReading := make(chan struct{})
			var once sync.Once
			p.Warn = func(s string) {
				warns.add(s)
				if strings.Contains(s, "stdin stopped") {
					once.Do(func() { close(stoppedReading) })
				}
			}
			if action == "timeout" {
				p.sendTimeout = time.Second
			}
			lifetime, end := context.WithCancel(context.Background())
			defer end()
			if _, err := p.Connect(lifetime); err != nil {
				t.Fatal(err)
			}
			p.mu.Lock()
			c := p.child
			p.mu.Unlock()
			t.Cleanup(func() {
				_ = c.cmd.Process.Kill()
				p.shutdownChild()
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			call := make(chan error, 1)
			go func() { call <- p.Send(ctx, chat.Outgoing{ChatID: "c", Text: strings.Repeat("x", 2<<20)}) }()
			awaitChild(t, stoppedReading, "child stopping stdin reads")
			later := make(chan error, 1)
			go func() { later <- p.Send(context.Background(), chat.Outgoing{ChatID: "c", Text: "later"}) }()
			switch action {
			case "cancel":
				cancel()
			case "shutdown":
				done := make(chan struct{})
				go func() { p.shutdownChild(); close(done) }()
				awaitChild(t, done, "shutdown with a blocked writer")
			}
			err := childResult(t, call)
			if err == nil {
				t.Fatal("blocked send succeeded")
			}
			if action == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel = %v", err)
			}
			if action == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout = %v", err)
			}
			if err := childResult(t, later); err == nil {
				t.Fatal("waiting send survived the broken stream")
			}
			awaitChild(t, c.waited, "process reap")
			if c.cmd.ProcessState == nil {
				t.Fatal("process has no Wait result")
			}
			if action != "shutdown" {
				select {
				case exitErr := <-p.childExit:
					p.manifest = helperManifest(t, "happy")
					if err := p.restart(lifetime, exitErr); err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("broken stream did not reach the restart loop")
				}
				if err := p.Send(context.Background(), chat.Outgoing{ChatID: "c", Text: "restarted"}); err != nil {
					t.Fatalf("restarted connector cannot send: %v", err)
				}
				p.mu.Lock()
				fresh := p.child
				p.mu.Unlock()
				p.shutdownChild()
				awaitChild(t, fresh.waited, "responsive child shutdown")
				if !fresh.cmd.ProcessState.Success() {
					t.Fatal("responsive connector did not exit gracefully")
				}
			}
		})
	}
}
