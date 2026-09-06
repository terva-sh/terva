package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type pipeSignal struct {
	once sync.Once
	ch   chan struct{}
}

func (s *pipeSignal) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.ch) })
	return len(p), nil
}

func awaitMCP(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(8 * time.Second):
		t.Fatalf("%s did not complete", what)
	}
}

func mcpCallResult(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("MCP caller remained blocked")
		return nil
	}
}

func TestStdioBlockedWriteLifecycle(t *testing.T) {
	for _, action := range []string{"cancel", "timeout", "stop"} {
		t.Run(action, func(t *testing.T) {
			signal := &pipeSignal{ch: make(chan struct{})}
			cl, err := Start(context.Background(), "blocked", ServerConfig{
				Command: buildStub(t), Args: []string{"--block-stdin"},
			}, "", signal)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(cl.Stop)
			if action == "timeout" {
				cl.timeout = time.Second
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			args := json.RawMessage(`{"payload":"` + strings.Repeat("x", 2<<20) + `"}`)
			call := make(chan error, 1)
			go func() { _, err := cl.CallTool(ctx, "add", args); call <- err }()
			awaitMCP(t, signal.ch, "child stopping stdin reads")
			later := make(chan error, 1)
			go func() { _, err := cl.CallTool(context.Background(), "add", nil); later <- err }()
			switch action {
			case "cancel":
				cancel()
			case "stop":
				stopped := make(chan struct{})
				go func() { cl.Stop(); close(stopped) }()
				awaitMCP(t, stopped, "Stop")
			}
			err = mcpCallResult(t, call)
			if err == nil {
				t.Fatal("blocked request succeeded")
			}
			if action == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel = %v", err)
			}
			if action == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout = %v", err)
			}
			if err := mcpCallResult(t, later); err == nil {
				t.Fatal("later call survived a partial frame")
			}
			tr := cl.transport.(*stdioTransport)
			awaitMCP(t, tr.waitDone, "process reap")
			awaitMCP(t, cl.Done(), "client disconnection")
			if tr.cmd.ProcessState == nil {
				t.Fatal("process has no Wait result")
			}
			cl.mu.Lock()
			pending := len(cl.pending)
			cl.mu.Unlock()
			if pending != 0 {
				t.Fatalf("%d pending calls remain", pending)
			}
		})
	}
}

func TestStdioResponseTimeoutPreservesTransport(t *testing.T) {
	signal := &pipeSignal{ch: make(chan struct{})}
	cl, err := Start(context.Background(), "silent", ServerConfig{
		Command: buildStub(t), Args: []string{"--no-reply"}, TimeoutMS: 200,
	}, "", signal)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Stop()
	call := make(chan error, 1)
	go func() { _, err := cl.CallTool(context.Background(), "add", nil); call <- err }()
	awaitMCP(t, signal.ch, "request read")
	if err := mcpCallResult(t, call); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("response timeout = %v", err)
	}
	if err := cl.listTools(context.Background()); err != nil {
		t.Fatalf("transport unusable after a response timeout: %v", err)
	}
	cl.Stop()
	tr := cl.transport.(*stdioTransport)
	awaitMCP(t, tr.waitDone, "graceful reap")
	if !tr.cmd.ProcessState.Success() {
		t.Fatal("responsive MCP child did not exit gracefully")
	}
}

func TestStdioEOFReapsLiveProcess(t *testing.T) {
	cl := startStub(t, "--close-stdout")
	awaitMCP(t, cl.Done(), "spontaneous EOF")
	tr := cl.transport.(*stdioTransport)
	// No Stop call: EOF must start cleanup even if the child stays alive.
	awaitMCP(t, tr.waitDone, "reap after spontaneous EOF")
	if tr.cmd.ProcessState == nil {
		t.Fatal("process has no Wait result")
	}
}
