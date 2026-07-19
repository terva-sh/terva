package mcp

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/procenv"
	"terva.sh/terva/packages/lineframe"
)

// stdioTransport speaks MCP over a subprocess's stdin/stdout as newline-delimited
// JSON — the original transport and still the default. It is a pure move of the
// process + read-loop mechanics out of Client; the correlation logic stayed in
// Client (see transport.go).
type stdioTransport struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	in    chan []byte

	writeMu   sync.Mutex
	closeOnce sync.Once
	closed    chan struct{}
}

// newStdioTransport spawns the server subprocess and starts funnelling its
// stdout frames into Incoming(). cwd (when non-empty) is the process working
// directory — the user's project — so relative-path MCP tools resolve there
// rather than against terva's launch directory. stderr (when non-nil) captures
// the child's diagnostics.
func newStdioTransport(cfg ServerConfig, cwd string, stderr io.Writer) (*stdioTransport, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	env := procenv.Inherited()
	for k, v := range cfg.Env {
		// Validated at config load; belt-and-braces here because this is the
		// actual trust boundary.
		if procenv.Disallowed(k) || k == "PATH" {
			continue
		}
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	if stderr != nil {
		cmd.Stderr = stderr
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn %s: %w", cfg.Command, err)
	}
	t := &stdioTransport{
		cmd:    cmd,
		stdin:  stdin,
		in:     make(chan []byte, 16),
		closed: make(chan struct{}),
	}
	go t.readLoop(stdout)
	return t, nil
}

func (t *stdioTransport) readLoop(stdout io.Reader) {
	// Closing `in` is how the Client learns the server is gone (stdout EOF).
	defer close(t.in)
	// MCP tool results can be large (a whole file in a text block); read through
	// lineframe at the shared 4 MiB ceiling so one oversized frame from a buggy
	// or hostile server drops just that frame instead of tearing the connection
	// down the way bufio.Scanner's ErrTooLong would. This minimal transport has
	// no logger seam, so the skip is silent.
	fr := lineframe.NewReader(stdout, lineframe.DefaultMaxBytes, nil)
	for {
		line, err := fr.Read()
		if err != nil {
			return // io.EOF or a read error: the server closed stdout
		}
		if len(line) == 0 {
			continue
		}
		// lineframe may reuse its buffer across reads, and the Client parses the
		// frame off-goroutine, so hand it a private copy.
		frame := make([]byte, len(line))
		copy(frame, line)
		select {
		case t.in <- frame:
		case <-t.closed:
			return
		}
	}
}

// Send writes one frame plus a newline under the write lock, so concurrent
// callers can't interleave their bytes on stdin. ctx is unused: a pipe write is
// not cancellable and returns promptly.
func (t *stdioTransport) Send(_ context.Context, frame []byte) error {
	t.writeMu.Lock()
	defer t.writeMu.Unlock()
	if _, err := t.stdin.Write(frame); err != nil {
		return err
	}
	_, err := t.stdin.Write([]byte("\n"))
	return err
}

func (t *stdioTransport) Incoming() <-chan []byte { return t.in }

// Close closes stdin (the polite MCP shutdown) and reaps the process, killing it
// after a short grace period. When the process has already died (stdout EOF), the
// Wait returns immediately and the grace timer never fires.
func (t *stdioTransport) Close() {
	t.closeOnce.Do(func() {
		close(t.closed)
		_ = t.stdin.Close()
		if t.cmd == nil || t.cmd.Process == nil {
			return
		}
		done := make(chan struct{})
		go func() {
			_, _ = t.cmd.Process.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = t.cmd.Process.Kill()
			<-done
		}
	})
}
