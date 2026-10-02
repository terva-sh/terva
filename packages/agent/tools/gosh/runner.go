//go:build terva_gosh

// Package gosh is an in-process shell for the bash tool. It interprets each
// script with mvdan.cc/sh, through go-bash's command set, and starts no
// process. It exists for hosts that cannot start one (iOS) and as a
// hermetic shell elsewhere. It is a spike behind the terva_gosh build tag
// (TKT-01M3V1C73R).
//
// The shell sees two places: the workspace, mounted at its real absolute
// path so that paths agree with what read and edit report, and a private
// in-memory tree for /tmp and $HOME. Nothing else on the host is visible.
package gosh

import (
	"bytes"
	"context"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	gobash "github.com/mark3labs/go-bash"
	gobashfs "github.com/mark3labs/go-bash/fs"
	"github.com/mark3labs/go-bash/fs/memfs"
	"github.com/mark3labs/go-bash/fs/mountfs"
	"github.com/mark3labs/go-bash/fs/rwfs"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/i18n"
)

// notesDesc is the English default for tool.bash.gosh.notes, the paragraph
// the in-process runner adds to the bash tool description.
const notesDesc = "This shell runs inside terva. It runs bash syntax, but it starts no program. The commands are built in, and nothing can be installed. git, python, node, go, make, and package managers are not available.\n\n" +
	"The shell sees the workspace at its real path, and a private /tmp. It does not see other parts of the computer. The network is off.\n\n" +
	"Use js for a script. It runs JavaScript, reads stdin as the global stdin, and has require('fs') for files. Example: cat data.json | js -e 'console.log(JSON.parse(stdin).items.length)'. jq also works for JSON."

// Runner is the in-process ShellRunner. Its /tmp and $HOME live as long as
// the Runner, so a file one call writes there is there for the next call,
// the way the host /tmp is.
type Runner struct {
	root string
	sb   *tools.Sandbox

	once sync.Once
	fs   gobashfs.FileSystem
	err  error
}

// New returns a Runner for the workspace at root, an absolute path.
//
// A nil Sandbox gets a locked one at root, so the guard never runs open:
// the host shell without a Sandbox is unjailed, and this runner is never.
func New(root string, sb *tools.Sandbox) *Runner {
	root = filepath.Clean(root)
	if sb == nil {
		sb = tools.NewSandbox(root)
		sb.Lock()
	}
	return &Runner{root: root, sb: sb}
}

func (r *Runner) ShellName() string { return "terva's in-process shell" }
func (r *Runner) Notes() string     { return i18n.D("tool.bash.gosh.notes", notesDesc) }

func (r *Runner) filesystem() (gobashfs.FileSystem, error) {
	r.once.Do(func() {
		base := memfs.New()
		for _, d := range []string{"/tmp", "/home/user", "/dev", filepath.ToSlash(filepath.Dir(r.root))} {
			if err := base.MkdirAll(d, 0o755); err != nil {
				r.err = err
				return
			}
		}
		ws, err := rwfs.New(rwfs.Options{Root: r.root})
		if err != nil {
			r.err = err
			return
		}
		r.fs, r.err = mountfs.New(mountfs.Options{
			Base:   &devFS{FileSystem: base},
			Mounts: []mountfs.Mount{{Path: filepath.ToSlash(r.root), FileSystem: &guardFS{FileSystem: ws, root: r.root, sb: r.sb}}},
		})
	})
	return r.fs, r.err
}

func big(n int) *int { return &n }

func (r *Runner) Run(ctx context.Context, req tools.ShellRequest) (int, error) {
	return r.run(ctx, req.Script, req.Dir, req.Env, req.Output, req.Output)
}

// run is Run with stdout and stderr apart. The bash tool merges them, and
// the comparison against GNU needs them separate.
func (r *Runner) run(ctx context.Context, script, reqDir string, reqEnv map[string]string, stdoutW, stderrW io.Writer) (int, error) {
	fsys, err := r.filesystem()
	if err != nil {
		return 0, err
	}
	dir := filepath.ToSlash(filepath.Clean(reqDir))
	if dir != r.root && !strings.HasPrefix(dir, filepath.ToSlash(r.root)+"/") {
		dir = filepath.ToSlash(r.root)
	}
	env := map[string]string{
		"HOME":   "/home/user",
		"USER":   "user",
		"TMPDIR": "/tmp",
		"PATH":   "/usr/bin:/bin",
		"SHELL":  "/bin/bash",
	}
	for k, v := range reqEnv {
		env[k] = v
	}
	b, err := gobash.New(gobash.BashOptions{
		FS:             fsys,
		Cwd:            dir,
		Env:            env,
		CustomCommands: customCommands(),
		// go-bash defaults to 10,000 loop iterations and 10,000 commands,
		// which a loop over a real tree passes. The bash tool's timeout is
		// the real bound, so these only stop runaway expansion.
		ExecutionLimits: &gobash.ExecutionLimits{
			MaxCommandCount:   big(1 << 30),
			MaxLoopIterations: big(1 << 30),
			MaxAwkIterations:  big(1 << 30),
			MaxSedIterations:  big(1 << 30),
			MaxJqIterations:   big(1 << 30),
		},
	})
	if err != nil {
		return 0, err
	}
	out := &cutoffWriter{w: stdoutW}
	errOut := &cutoffWriter{w: stderrW}
	stderr := &missingWatch{w: errOut}
	type result struct {
		res gobash.BashExecResult
		err error
	}
	done := make(chan result, 1)
	go func() {
		res, err := b.Exec(ctx, script, gobash.ExecOptions{
			Stdin:  strings.NewReader(""),
			Stdout: out,
			Stderr: stderr,
		})
		done <- result{res, err}
	}()

	var got result
	select {
	case got = <-done:
	case <-ctx.Done():
		select {
		case got = <-done:
		case <-time.After(abandonGrace):
			// 🚨 Nothing can kill an in-process command. Most of them watch
			// the context, but one that does not (go-bash's sort reading a
			// huge pipe ran 30 s past a 1 s timeout) would hold the turn.
			// The tool stops waiting and cuts the script's output off. The
			// work itself runs on until it ends, which the note says.
			out.cut()
			errOut.cut()
			io.WriteString(stderrW, "[the in-process shell did not stop at the timeout. It still runs in the background until it ends, and its output is dropped.]\n")
			return -1, nil
		}
	}
	// Exec has returned, and a background job it did not wait for must not
	// write after Run does: the bash tool closes its output next.
	defer out.cut()
	defer errOut.cut()
	if got.err != nil {
		io.WriteString(errOut, got.err.Error()+"\n")
		if got.res.ExitCode == 0 {
			return 2, nil
		}
	}
	if missing := stderr.names(); len(missing) > 0 {
		io.WriteString(errOut, missingHint(missing, b))
	}
	return got.res.ExitCode, nil
}

// abandonGrace is how long Run waits past the deadline for a script to
// notice it, before it gives up on it.
const abandonGrace = 2 * time.Second

// cutoffWriter drops every write after cut, so a script that Run abandoned
// cannot write into a result the bash tool has already returned.
type cutoffWriter struct {
	mu  sync.Mutex
	w   io.Writer
	off bool
}

func (c *cutoffWriter) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.off {
		return len(p), nil
	}
	return c.w.Write(p)
}

func (c *cutoffWriter) cut() {
	c.mu.Lock()
	c.off = true
	c.mu.Unlock()
}

// missingWatch passes stderr through and notes each "X: command not found",
// so the run can end with one hint rather than leave the model to guess why.
type missingWatch struct {
	w       io.Writer
	mu      sync.Mutex
	partial []byte
	seen    map[string]bool
}

func (m *missingWatch) Write(p []byte) (int, error) {
	m.mu.Lock()
	m.partial = append(m.partial, p...)
	for {
		i := bytes.IndexByte(m.partial, '\n')
		if i < 0 {
			break
		}
		line := string(m.partial[:i])
		m.partial = m.partial[i+1:]
		if name, ok := strings.CutSuffix(line, ": command not found"); ok {
			if m.seen == nil {
				m.seen = map[string]bool{}
			}
			m.seen[path.Base(name)] = true
		}
	}
	m.mu.Unlock()
	return m.w.Write(p)
}

func (m *missingWatch) names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.seen))
	for n := range m.seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func missingHint(missing []string, b *gobash.Bash) string {
	var names []string
	for _, n := range b.Registry().Names() {
		names = append(names, string(n))
	}
	sort.Strings(names)
	return "[hint] " + strings.Join(missing, ", ") + ": not available. This shell runs inside terva and starts no program, and nothing can be installed. " +
		"Use the read, write and edit tools for files, and js for a script. The built-in commands are: " + strings.Join(names, " ") + "\n"
}
