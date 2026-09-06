package mcp

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/internal/pipeio"
	"terva.sh/terva/packages/agent/procenv"
	"terva.sh/terva/packages/envcompat"
	"terva.sh/terva/packages/lineframe"
)

// stdioTransport speaks MCP over a subprocess's stdin/stdout as newline-delimited
// JSON — the original transport and still the default. It is a pure move of the
// process + read-loop mechanics out of Client; the correlation logic stayed in
// Client (see transport.go).
type stdioTransport struct {
	cmd    *exec.Cmd
	stdin  *pipeio.Writer
	stdout io.ReadCloser
	in     chan []byte

	closeOnce sync.Once
	closed    chan struct{}
	readDone  chan struct{}
	waitDone  chan struct{}
}

// newStdioTransport spawns the server subprocess and starts funnelling its
// stdout frames into Incoming(). cwd (when non-empty) is the process working
// directory — the user's project — so relative-path MCP tools resolve there
// rather than against terva's launch directory. stderr (when non-nil) captures
// the child's diagnostics.
func newStdioTransport(cfg ServerConfig, cwd string, stderr io.Writer) (*stdioTransport, error) {
	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.WaitDelay = 2 * time.Second
	if cwd != "" {
		cmd.Dir = cwd
	}
	env := procenv.Inherited()
	// Name the data home explicitly rather than relying on the child to
	// re-derive it. Inherited() carries TERVA_HOME when terva's own environment
	// has it, but when terva resolved its home by DISCOVERY (no env var, an
	// install that predates the rename) the variable is absent and the child is
	// left to run the same four-step resolution and hope it agrees. terva-mcp-bridge
	// looked for the at-rest key under a home resolved that way, missed it, and
	// wrote OAuth tokens in the clear. Stated before cfg.Env so a server that
	// deliberately pins its own home still wins.
	env = append(env, "TERVA_HOME="+envcompat.Home())
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
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, fmt.Errorf("spawn %s: %w", cfg.Command, err)
	}
	t := &stdioTransport{
		cmd:      cmd,
		stdout:   stdout,
		in:       make(chan []byte, 16),
		closed:   make(chan struct{}),
		readDone: make(chan struct{}),
		waitDone: make(chan struct{}),
	}
	t.stdin = pipeio.New(stdin, func() { go t.Close() })
	go t.readLoop(stdout)
	go func() {
		<-t.readDone
		_ = cmd.Wait()
		close(t.waitDone)
	}()
	return t, nil
}

func (t *stdioTransport) readLoop(stdout io.Reader) {
	defer func() {
		close(t.readDone)
		go t.Close()
	}()
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

// Send serializes each frame and includes gate wait and pipe I/O in ctx.
func (t *stdioTransport) Send(ctx context.Context, frame []byte) error {
	return t.stdin.Write(ctx, append(frame, '\n'))
}

func (t *stdioTransport) Incoming() <-chan []byte { return t.in }

// Close closes stdin (the polite MCP shutdown) and reaps the process, killing it
// after a short grace period. When the process has already died (stdout EOF), the
// Wait returns immediately and the grace timer never fires.
func (t *stdioTransport) Close() {
	t.closeOnce.Do(func() {
		close(t.closed)
		t.stdin.Close()
		if t.cmd == nil || t.cmd.Process == nil {
			return
		}
		select {
		case <-t.waitDone:
		case <-time.After(2 * time.Second):
			_ = t.cmd.Process.Kill()
			// A descendant can retain stdout after the direct child dies.
			// Release the reader before the single cmd.Wait owner runs.
			_ = t.stdout.Close()
			<-t.waitDone
		}
	})
}
