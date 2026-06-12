package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
	"time"
)

// rpcSession drives the binary's --rpc mode over stdin/stdout: one
// JSON object per line in each direction.
type rpcSession struct {
	t     *testing.T
	stdin io.WriteCloser
	lines <-chan string
	cmd   *exec.Cmd
}

func startRPC(t *testing.T, baseURL, workspace string) *rpcSession {
	t.Helper()
	home := t.TempDir()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, tervaBin,
		"--rpc",
		"--provider", "openai-compatible",
		"--model", "test-model",
		"--base-url", baseURL,
		"--api-key", "e2e-test-key",
		"--cwd", workspace,
		"--no-session",
		"--no-ext",
	)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"TERVA_HOME=" + home,
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatalf("start rpc mode: %v", err)
	}

	lines := make(chan string, 64)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			lines <- sc.Text()
		}
	}()

	s := &rpcSession{t: t, stdin: stdin, lines: lines, cmd: cmd}
	t.Cleanup(func() {
		_ = stdin.Close() // EOF ends the rpc loop
		_ = cmd.Wait()
	})
	return s
}

// send writes one request line.
func (s *rpcSession) send(req map[string]any) {
	s.t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		s.t.Fatal(err)
	}
	if _, err := fmt.Fprintf(s.stdin, "%s\n", b); err != nil {
		s.t.Fatalf("write rpc request: %v", err)
	}
}

// next returns the next stdout frame, decoded. Fails the test after a
// timeout so a wedged server can't hang the suite.
func (s *rpcSession) next() map[string]any {
	s.t.Helper()
	select {
	case line, ok := <-s.lines:
		if !ok {
			s.t.Fatalf("rpc stdout closed unexpectedly")
		}
		var frame map[string]any
		if err := json.Unmarshal([]byte(line), &frame); err != nil {
			s.t.Fatalf("rpc frame is not valid JSON: %v\nline: %s", err, line)
		}
		return frame
	case <-time.After(30 * time.Second):
		s.t.Fatalf("timed out waiting for an rpc frame")
		return nil
	}
}

// collectUntil reads frames until pred returns true, returning every
// frame read (inclusive).
func (s *rpcSession) collectUntil(pred func(map[string]any) bool) []map[string]any {
	s.t.Helper()
	var out []map[string]any
	for {
		f := s.next()
		out = append(out, f)
		if pred(f) {
			return out
		}
	}
}

// TestRPCGoldenStream pins the advertised-stable RPC surface end to
// end: ping, state, a full prompt turn with streamed events, the
// transcript readback, and the unknown-command error shape. This is
// the first functional test the RPC server has ever had.
func TestRPCGoldenStream(t *testing.T) {
	fp := newFakeProvider(t, sseScript{
		chunks:   []map[string]any{textChunk("hello "), textChunk("rpc"), finishChunk("stop", 10, 2)},
		sendDone: true,
	})
	ws := t.TempDir()
	s := startRPC(t, fp.url(), ws)

	// ping
	s.send(map[string]any{"type": "ping", "id": "1"})
	pong := s.next()
	if pong["type"] != "response" || pong["command"] != "ping" || pong["success"] != true || pong["id"] != "1" {
		t.Fatalf("bad ping response: %v", pong)
	}

	// get_state before any turn
	s.send(map[string]any{"type": "get_state", "id": "2"})
	state := s.next()
	data, _ := state["data"].(map[string]any)
	if data["provider"] != "openai-compatible" || data["model"] != "test-model" {
		t.Fatalf("bad state: %v", state)
	}
	if data["busy"] != false {
		t.Fatalf("fresh rpc server reports busy: %v", state)
	}

	// prompt: ack, then event stream, then the done frame
	s.send(map[string]any{"type": "prompt", "id": "3", "message": "say hi"})
	ack := s.next()
	if ack["type"] != "response" || ack["command"] != "prompt" || ack["success"] != true {
		t.Fatalf("bad prompt ack: %v", ack)
	}
	frames := s.collectUntil(func(f map[string]any) bool { return f["type"] == "done" })

	var sawTurnEnd bool
	var text string
	for _, f := range frames {
		switch f["type"] {
		case "text_delta":
			d, _ := f["delta"].(string)
			text += d
		case "turn_end":
			sawTurnEnd = true
			if stop, _ := f["stop"].(string); stop != "end" {
				t.Errorf("turn_end stop = %q, want end", stop)
			}
		case "error":
			t.Errorf("unexpected error frame: %v", f)
		}
	}
	if !sawTurnEnd {
		t.Errorf("no turn_end before done; frames: %v", frames)
	}
	if text != "hello rpc" {
		t.Errorf("streamed text = %q, want %q", text, "hello rpc")
	}

	// transcript readback
	s.send(map[string]any{"type": "get_messages", "id": "4"})
	msgs := s.next()
	data, _ = msgs["data"].(map[string]any)
	list, _ := data["messages"].([]any)
	if len(list) != 2 {
		t.Fatalf("message count = %d, want 2 (user + assistant): %v", len(list), msgs)
	}

	// unknown command: an error response, not a dropped frame
	s.send(map[string]any{"type": "bogus", "id": "5"})
	bad := s.next()
	if bad["success"] != false || bad["error"] == "" || bad["id"] != "5" {
		t.Fatalf("bad unknown-command response: %v", bad)
	}
}
