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
	"sort"
	"strings"
	"sync"
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

	// Env carries extra environment variables exported into every command,
	// on top of the inherited process environment. Used for terva runtime
	// facts the model otherwise can't see from inside a shell (TERVA_HOME,
	// so `cp x $TERVA_HOME/extensions/` works without a prior tool call).
	// Keys here win over inherited duplicates (appended last). NEVER put
	// secrets here — bash is an unguarded egress channel.
	Env map[string]string
}

type bashArgs struct {
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

const bashSchema = `{"type":"object","properties":{"command":{"type":"string"},"timeout":{"type":"integer","description":"Maximum run time in seconds before the command is killed. Defaults to 120 if omitted."}},"required":["command"]}`

// effectiveCWD is the directory commands actually run in: the configured CWD,
// or the process working directory when the host left it empty. Both
// Description and the failure footer name it, so the model is told the one
// fact a relative path depends on — the value it otherwise has to guess.
func (t *BashTool) effectiveCWD() string {
	if t.CWD != "" {
		return t.CWD
	}
	cwd, _ := os.Getwd()
	return cwd
}

func (t *BashTool) Name() string { return "bash" }
func (t *BashTool) Description() string {
	// Name the working directory concretely rather than calling it "the
	// agent's cwd". A model that only knows the phrase writes `./bin/test`
	// against whatever directory it assumes the project root is, and a
	// session whose cwd is a parent of the repo it is working in fails that
	// way repeatedly — each failure reported only as "not found", with
	// nothing in the output naming the directory that made it wrong.
	where := "the agent's cwd"
	if cwd := t.effectiveCWD(); cwd != "" {
		where = cwd
	}
	// Name the interpreter. When bash is absent the commands run under
	// /bin/sh — which on Debian-family hosts is dash, where a model's
	// reflexive ${PIPESTATUS[0]} and <(...) are syntax errors rather than
	// features. Saying so is the difference between the model writing POSIX
	// and the model discovering the dialect one dead turn at a time.
	return "Run a shell command under " + shellName() + " (stdout+stderr merged). Prefer the dedicated tools over shell equivalents: read/write/edit for files (not cat, sed -i, echo >file), grep/glob for search (not grep, find, ls) — they are safer, reviewable, and cheaper. Commands run in " + where + "; a relative path resolves against THAT directory, not the file you last read or edited — pass absolute paths (or `git -C`) for anything outside it, and avoid `cd`. Git safety: never force-push, `reset --hard`, amend, skip hooks, or `git add -A` unless the user explicitly asks. Do not print or export secrets (.env, tokens, credentials). Slow commands should set an explicit timeout; the default kill is 120s. In exploratory multi-step scripts avoid `set -e` (one failing probe aborts the whole script and hides the rest); check exit codes explicitly instead. $TERVA_HOME is exported into the environment."
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
	cwd := t.effectiveCWD()

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
	// Inherit the process environment, then append terva facts so they win
	// over any inherited duplicate (Go uses the last value for a repeated key).
	cmd.Env = os.Environ()
	for _, k := range sortedKeys(t.Env) {
		cmd.Env = append(cmd.Env, k+"="+t.Env[k])
	}
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
	// A non-zero exit is exactly where the working directory starts to
	// matter, and the prompt line above echoes the command without it. Left
	// unsaid, `./bin/test` → "not found" and `git diff` → "not a git
	// repository" both read as a broken repo rather than the wrong
	// directory. Named only on failure, so successful results are unchanged.
	if exitCode != 0 && cwd != "" {
		fmt.Fprintf(&sb, " (cwd: %s)", cwd)
	}

	// Surface the genuinely-full output only when we actually dropped
	// bytes or lines from the inline view. The spill file was tee'd from
	// the complete stream, so it is the real full output, not a relabeled
	// copy of the capped buffer. When nothing was dropped, discard it.
	var fullPath string
	if truncBytes || truncLines {
		fullPath = spill.Path()
		if fullPath != "" {
			// Point the model at the spill explicitly: it's a readable file,
			// so `read` (with offset/limit to page) reaches the dropped tail
			// without re-running the command and re-truncating.
			fmt.Fprintf(&sb, " (full output: %s — read this file, paging with offset/limit, instead of re-running)", fullPath)
		}
	} else {
		spill.Discard()
	}
	fmt.Fprintf(&sb, "  Took %s", humanDuration(elapsed))
	if hint := matchExitHint(exitCode, a.Command); hint != "" {
		sb.WriteString("\n" + hint)
	}

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

// sortedKeys returns m's keys in deterministic order so the exported env
// is stable across runs (the values are appended last either way).
func sortedKeys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// matchExitCommands are commands whose exit status 1 means "found nothing" or
// "not equal" rather than "failed". A pipeline ending in one of these reports
// 1 on the healthy path, and the model reads a red result off a green build.
var matchExitCommands = map[string]bool{
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "ag": true,
	"diff": true, "cmp": true, "test": true, "[": true,
}

// matchExitHint explains an exit 1 that a match-style command produced by
// finding nothing. Verification pipelines of the shape
//
//	cargo test ... | grep -c FAILED
//
// exit 1 precisely when the tests all passed, and the result comes back marked
// as an error over a body reading "0". Observed 13 times in one session, each
// one re-running a full test suite to re-confirm the same green build: the
// harness cannot know intent, but it can name the convention.
//
// Deliberately narrow — only exit 1, only when the last stage is a match-style
// command. A broader hint would fire on genuine failures and teach the model to
// discount real exit codes.
func matchExitHint(exitCode int, command string) string {
	if exitCode != 1 {
		return ""
	}
	name := lastPipelineCommand(command)
	if !matchExitCommands[name] {
		return ""
	}
	return fmt.Sprintf("[hint] a pipeline's exit status is its LAST command's, and %s exits 1 when it finds no match — "+
		"if the work before the pipe succeeded, read the output above rather than the exit code. "+
		"To report the status you actually mean, end with an explicit check (e.g. `... ; echo \"failures: $(... | grep -c X)\"`).", name)
}

// lastPipelineCommand returns the command word of the final pipeline stage of
// the final statement in cmd — the process whose status the shell reports.
// Quote-aware so a `;` or `|` inside an argument doesn't split a statement;
// returns "" when it cannot tell.
func lastPipelineCommand(cmd string) string {
	var (
		seg    strings.Builder
		quote  rune
		escape bool
	)
	reset := func() { seg.Reset() }
	for _, r := range cmd {
		switch {
		case escape:
			escape = false
			seg.WriteRune(r)
		case r == '\\' && quote != '\'':
			escape = true
		case quote != 0:
			if r == quote {
				quote = 0
			}
			seg.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
			seg.WriteRune(r)
		case r == '|' || r == ';' || r == '\n' || r == '&':
			// Every one of these ends the stage whose status would be
			// reported, so the last segment standing is the one that matters.
			reset()
		default:
			seg.WriteRune(r)
		}
	}
	fields := strings.Fields(seg.String())
	for _, f := range fields {
		// Skip a leading env assignment (FOO=bar cmd ...); the command word is
		// the first field that isn't one.
		if strings.Contains(f, "=") && !strings.HasPrefix(f, "=") {
			continue
		}
		return filepath.Base(strings.Trim(f, `"'`))
	}
	return ""
}

// shellPath is the interpreter every command runs under, resolved once.
var shellPath = sync.OnceValue(resolveShell)

// resolveShell picks the interpreter for the `bash` tool. It prefers a real
// bash from PATH and falls back to /bin/sh.
//
// The tool is NAMED bash and no model writes POSIX for a tool called bash. On
// Debian and Ubuntu /bin/sh is dash, so `${PIPESTATUS[0]}` and `<(...)` — the
// two constructs a model reaches for when it wants a pipeline's real exit code
// or wants to diff two command outputs — die with "Bad substitution" and
// "Syntax error: \"(\" unexpected". The failure is also silently
// platform-dependent: macOS's /bin/sh is bash in POSIX mode and swallows most
// of it, so the same command is portable on the developer's laptop and broken
// on the Linux host. Resolving bash first makes the tool mean what its name
// says; the /bin/sh fallback keeps busybox images working.
//
// PATH (not a hardcoded /bin/bash) so a Homebrew bash 5 wins over macOS's
// bundled 3.2 — the newer shell is a superset, and nothing here depends on the
// older one's gaps.
func resolveShell() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	if p, err := exec.LookPath("bash"); err == nil {
		return p
	}
	return "/bin/sh"
}

// shellName is the resolved shell's base name, for the tool description. A
// model told which shell it has writes for that shell; a model told only "a
// shell command" writes bash and finds out the hard way.
func shellName() string {
	if runtime.GOOS == "windows" {
		return "cmd"
	}
	return filepath.Base(shellPath())
}

func newShellCmd(ctx context.Context, command string) *exec.Cmd {
	if runtime.GOOS == "windows" {
		return exec.CommandContext(ctx, "cmd", "/C", command)
	}
	return exec.CommandContext(ctx, shellPath(), "-c", command)
}
