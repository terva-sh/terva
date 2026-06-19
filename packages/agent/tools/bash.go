package tools

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

const (
	maxBashLines = 2000
	maxBashBytes = 50 * 1024
	// defaultBashTimeout bounds a command when the model omits an explicit
	// timeout. Without it a runaway command (a hung server, an infinite
	// loop, a command awaiting stdin) would block the agent forever.
	defaultBashTimeout = 120 * time.Second
)

// BashTool runs a shell command in the agent's cwd.
type BashTool struct {
	CWD     string
	Sandbox *Sandbox
}

type bashArgs struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

const bashSchema = `{"type":"object","properties":{"command":{"type":"string"},"timeout":{"type":"integer","description":"Maximum run time in seconds before the command is killed. Defaults to 120 if omitted."}},"required":["command"]}`

func (t *BashTool) Name() string { return "bash" }
func (t *BashTool) Description() string {
	return "Run a shell command (stdout+stderr merged). Prefer the dedicated tools over shell equivalents: read/write/edit for files (not cat, sed -i, echo >file), grep/glob for search (not grep, find, ls) — they are safer, reviewable, and cheaper. Commands run in the agent's cwd; avoid `cd` (pass paths instead). Git safety: never force-push, `reset --hard`, amend, skip hooks, or `git add -A` unless the user explicitly asks. Do not print or export secrets (.env, tokens, credentials). Slow commands should set an explicit timeout; the default kill is 120s."
}
func (t *BashTool) Schema() json.RawMessage { return json.RawMessage(bashSchema) }

func (t *BashTool) Execute(ctx context.Context, raw json.RawMessage, progress func(string)) (core.ToolResult, error) {
	var a bashArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{}, fmt.Errorf("invalid args: %w", err)
	}
	if strings.TrimSpace(a.Command) == "" {
		return core.ToolResult{}, fmt.Errorf("command is required")
	}
	if err := t.Sandbox.CheckCommand(a.Command); err != nil {
		return core.ToolResult{}, err
	}
	cwd := t.CWD
	if cwd == "" {
		cwd, _ = os.Getwd()
	}

	// Apply an explicit timeout, or a sane default when the model omits
	// one. timeoutDur is recorded so the result can tell the model how
	// long it waited before killing a hung command.
	timeoutDur := defaultBashTimeout
	if a.Timeout > 0 {
		timeoutDur = time.Duration(a.Timeout) * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeoutDur)
	defer cancel()

	start := time.Now()
	cmd := newShellCmd(runCtx, a.Command)
	cmd.Dir = cwd
	cmd.Env = os.Environ()
	setProcessGroup(cmd)

	// Capture merged stdout+stderr with line-by-line streaming.
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		return core.ToolResult{}, fmt.Errorf("start: %w", err)
	}

	// captured holds the in-memory result we show inline (capped at
	// maxBashBytes). spill tees the *complete* stream to a temp file so
	// the "full output" link is genuinely full, not a re-labeled copy of
	// the capped buffer. totalBytes tracks the real, un-capped size.
	captured := &bytes.Buffer{}
	spill := newSpillWriter()
	var totalBytes int64
	done := make(chan struct{})

	// Watch for context cancellation and kill the entire process
	// group immediately. exec.CommandContext only kills the direct
	// process, but child processes (e.g. grep spawned by the shell)
	// keep the output pipe open and block cmd.Wait() indefinitely.
	go func() {
		select {
		case <-runCtx.Done():
			killProcessGroup(cmd)
			// Close the write end so the reader goroutine unblocks.
			pw.Close()
		case <-done:
		}
	}()
	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, err := pr.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				totalBytes += int64(n)
				spill.Write(chunk)
				if captured.Len() < maxBashBytes {
					room := maxBashBytes - captured.Len()
					if n > room {
						captured.Write(chunk[:room])
					} else {
						captured.Write(chunk)
					}
				}
				if progress != nil {
					progress(string(chunk))
				}
			}
			if err != nil {
				return
			}
		}
	}()

	waitErr := cmd.Wait()
	pw.Close()
	<-done
	spill.Close()

	output := captured.String()
	truncBytes := totalBytes > int64(maxBashBytes)
	lines := strings.Split(output, "\n")
	truncLines := false
	if len(lines) > maxBashLines {
		lines = lines[:maxBashLines]
		truncLines = true
	}
	trimmed := strings.Join(lines, "\n")

	exitCode := 0
	if waitErr != nil {
		if ee, ok := waitErr.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			exitCode = -1
		}
	}

	elapsed := time.Since(start)

	// Distinguish a timeout from an external cancel. runCtx carries the
	// deadline; if it fired while the parent ctx is still live, we killed
	// the command for running too long. Surfacing this (rather than a bare
	// "[exit -1]") lets the model react — raise the timeout, background the
	// command, or split the work.
	timedOut := runCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil

	// Terminal-log style: echo the command on the first line with
	// a shell-prompt prefix, a blank line, the captured output, and
	// a footer showing exit code + elapsed time. Matches the look
	// a human would see if they ran the command themselves, which
	// makes the model's reasoning about it more natural too.
	var sb strings.Builder
	fmt.Fprintf(&sb, "$ %s\n", a.Command)
	if trimmed != "" {
		sb.WriteString("\n")
		sb.WriteString(trimmed)
		if !strings.HasSuffix(trimmed, "\n") {
			sb.WriteString("\n")
		}
	}
	if truncLines {
		fmt.Fprintf(&sb, "... [truncated at %d lines]\n", maxBashLines)
	}
	if truncBytes {
		fmt.Fprintf(&sb, "... [truncated at %d bytes]\n", maxBashBytes)
	}
	sb.WriteString("\n")
	if exitCode == 0 {
		fmt.Fprintf(&sb, "[exit 0]")
	} else {
		fmt.Fprintf(&sb, "[exit %d]", exitCode)
	}
	if timedOut {
		fmt.Fprintf(&sb, " (timed out after %s)", humanDuration(timeoutDur))
	}

	// Surface the genuinely-full output only when we actually dropped
	// bytes or lines from the inline view. The spill file was tee'd from
	// the complete stream, so it is the real full output, not a relabeled
	// copy of the capped buffer. When nothing was dropped, discard it.
	var fullPath string
	if truncBytes || truncLines {
		fullPath = spill.Path()
		if fullPath != "" {
			fmt.Fprintf(&sb, " (full output: %s)", fullPath)
		}
	} else {
		spill.Discard()
	}
	fmt.Fprintf(&sb, "  Took %s", humanDuration(elapsed))

	isErr := exitCode != 0 || ctx.Err() != nil || timedOut
	return core.ToolResult{
		Content: []provider.Content{provider.TextBlock{Text: sb.String()}},
		IsError: isErr,
		Details: map[string]any{
			"exit_code":        exitCode,
			"full_output_path": fullPath,
			"lines_truncated":  truncLines,
			"bytes_truncated":  truncBytes,
			"timed_out":        timedOut,
			"duration_ms":      elapsed.Milliseconds(),
		},
	}, nil
}

// humanDuration renders a duration in the "Took X.Ys" style used by
// the shell-log output: tenths of a second for sub-minute runs,
// whole seconds once we pass a minute. Trailing zeros dropped so
// "0.1s" instead of "0.10s".
func humanDuration(d time.Duration) string {
	switch {
	case d < time.Millisecond:
		return "0.0s"
	case d < time.Minute:
		s := d.Seconds()
		return fmt.Sprintf("%.1fs", s)
	case d < time.Hour:
		m := int(d.Minutes())
		s := int(d.Seconds()) - m*60
		return fmt.Sprintf("%dm%ds", m, s)
	default:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		return fmt.Sprintf("%dh%dm", h, m)
	}
}

// spillWriter tees the command's complete stdout+stderr stream to a temp
// file as it arrives. Unlike the old writeFullOutput, which wrote the
// already-capped in-memory buffer to a file labeled "full output" (a lie
// for byte-truncated runs), this captures the genuine, un-capped output
// without holding it all in memory. The file is created lazily on the
// first byte so commands with no output never touch the disk, and
// Discard removes it when the inline view was complete (nothing dropped).
type spillWriter struct {
	f    *os.File
	path string
	err  error
}

func newSpillWriter() *spillWriter { return &spillWriter{} }

// Write appends a chunk, creating the backing file on first use. Errors
// are latched and silently swallow further writes — a failed spill must
// not break command execution; the inline (capped) output still stands.
func (w *spillWriter) Write(p []byte) {
	if w.err != nil {
		return
	}
	if w.f == nil {
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		name := filepath.Join(os.TempDir(), "terva-bash-"+hex.EncodeToString(b)+".log")
		f, err := os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			w.err = err
			return
		}
		w.f, w.path = f, name
	}
	if _, err := w.f.Write(p); err != nil {
		w.err = err
	}
}

// Close flushes and closes the backing file (if any).
func (w *spillWriter) Close() {
	if w.f != nil {
		_ = w.f.Close()
	}
}

// Path returns the spill file path, or "" if nothing was written or the
// spill failed.
func (w *spillWriter) Path() string {
	if w.err != nil {
		return ""
	}
	return w.path
}

// Discard removes the spill file. Used when the inline output was
// complete, so there is no "full output" worth keeping.
func (w *spillWriter) Discard() {
	if w.path != "" {
		_ = os.Remove(w.path)
		w.path = ""
	}
}

func newShellCmd(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, "/bin/sh", "-c", command)
}
