// Package e2e drives the COMPILED terva binary end to end: headless
// print/json modes against a fake openai-compatible provider served
// from httptest. This is the integration layer the June 2026 deep
// review found untested — args→Resolve→agent loop→provider wire→tool
// side effects — and where its four worst bug clusters lived (wrapper
// composition, abnormal stream termination, turn lifecycle, catalog
// refresh). Unit tests can't catch those; running the real binary can.
//
// Zero new dependencies: the fake provider is net/http/httptest, the
// binary is built once per `go test` run by TestMain below.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"terva.sh/terva/packages/testsupport"
	"testing"
	"time"
)

// tervaBin is the freshly-built binary under test, set by TestMain.
var tervaBin string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "terva-e2e-bin-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: temp dir: %v\n", err)
		os.Exit(1)
	}
	tervaBin = filepath.Join(tmp, "terva")
	if runtime.GOOS == "windows" {
		// exec on windows resolves executables by extension; a bare
		// "terva" builds fine but can never be spawned.
		tervaBin += ".exe"
	}

	// Plain build (no -race): the test binary itself runs under
	// whatever flags `go test` got; the subprocess just needs to be
	// the real product. Inherits the environment so the module/build
	// caches are reused.
	build := exec.Command("go", "build", "-o", tervaBin, "terva.sh/terva/cmd/terva")
	build.Dir = ".."
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build terva: %v\n%s", err, out)
		os.RemoveAll(tmp)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

// quitOnCancel makes a hung child say where it is stuck before it dies.
//
// exec.CommandContext kills with SIGKILL, which a process cannot observe: a
// terva that wedges mid-turn is erased, and every timeout reads the same way —
// "it stopped after the last frame it managed to print". Two Go 1.27 CI
// failures were diagnosed that far and no further, on two different tests,
// while the suite passed on every developer machine it was run on.
//
// SIGQUIT instead. The Go runtime answers it with a stack dump of every
// goroutine and then exits, so the stderr the failure already prints names the
// blocked call. terva installs no SIGQUIT handler, so nothing intercepts it.
//
// GOTRACEBACK is deliberately NOT set: SIGQUIT dumps all goroutines at the
// default level. Verified rather than assumed — a two-goroutine program parked
// in select{} reports both without it.
//
// WaitDelay bounds the courtesy. A process that ignores the signal, or dies
// while a pipe is still open, is killed after it rather than hanging the suite
// for the rest of the run. It is set on every platform, because it bounds the
// wait for the pipes whichever way the child dies.
//
// Windows gets no Cancel at all. os.Process.Signal there refuses everything but
// Kill — exec_windows.go returns EWINDOWS for any other signal — so a Cancel
// would dump nothing and merely add that error to what Wait reports. The
// default kill stays, which is what this platform had before.
//
// 🪤 The internal forge runs linux only and cannot see that. The PUBLIC GitHub
// lane runs this suite on windows-latest, so the first thing to notice would
// have been a release blocked at its own gate.
func quitOnCancel(cmd *exec.Cmd) {
	cmd.WaitDelay = 10 * time.Second
	if runtime.GOOS == "windows" {
		return
	}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGQUIT) }
}

// syncBuffer is an io.Writer safe to read while the child is still writing.
// os/exec copies a non-*os.File stderr on its own goroutine, so a test that
// reads the dump before Wait returns is racing that copy.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// tervaResult is one finished subprocess run.
type tervaResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// runTerva runs the built binary against the given fake-provider base
// URL with a fresh, isolated TERVA_HOME. The environment is constructed
// from scratch (not inherited) so the developer's real credentials,
// config, and provider env vars can never leak into a test, and runs
// are deterministic anywhere.
func runTerva(t *testing.T, baseURL, workspace string, args ...string) tervaResult {
	t.Helper()

	home := testsupport.TempDir(t)
	full := append([]string{
		"--provider", "openai-compatible",
		"--model", "test-model",
		"--base-url", baseURL,
		"--api-key", "e2e-test-key",
		"--cwd", workspace,
		"--no-session",
		"--no-ext",
	}, args...)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, tervaBin, full...)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"TERVA_HOME=" + home,
	}
	quitOnCancel(cmd)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := tervaResult{stdout: stdout.String(), stderr: stderr.String()}
	switch e := err.(type) {
	case nil:
		res.exitCode = 0
	case *exec.ExitError:
		res.exitCode = e.ExitCode()
	default:
		t.Fatalf("run terva: %v (ctx err: %v)\nstderr:\n%s", err, ctx.Err(), stderr.String())
	}
	if ctx.Err() != nil {
		t.Fatalf("terva timed out\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	return res
}

// The diagnostic has to work on the day it is needed, and nobody schedules that
// day. So it is exercised on every run: wedge the real binary, cancel it, and
// require the goroutine dump to come back.
//
// 🪤 Without this, quitOnCancel is a comment. If SIGQUIT ever stops reaching the
// child — a handler added upstream, a change in how os/exec cancels, a runtime
// that stops dumping at the default traceback level — every future timeout goes
// back to reporting only that terva stopped, and the regression is invisible
// until someone is already staring at a red CI run they cannot explain.
func TestAHungChildDumpsItsGoroutines(t *testing.T) {
	if runtime.GOOS == "windows" {
		// No SIGQUIT to send, so quitOnCancel deliberately leaves the default
		// kill in place and there is no dump to assert on.
		t.Skip("windows has no SIGQUIT; quitOnCancel is a no-op there")
	}
	home := testsupport.TempDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// --rpc parks reading stdin, and nothing is ever written to it. That is a
	// deliberate wedge in the same shape as the accidental ones: alive, healthy,
	// and producing nothing.
	cmd := exec.CommandContext(ctx, tervaBin,
		"--rpc",
		"--provider", "openai-compatible",
		"--model", "test-model",
		"--base-url", "http://127.0.0.1:1",
		"--api-key", "e2e-test-key",
		"--cwd", testsupport.TempDir(t),
		"--no-session",
		"--no-ext",
	)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"TERVA_HOME=" + home,
	}
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	quitOnCancel(cmd)

	if err := cmd.Start(); err != nil {
		t.Fatalf("start terva: %v", err)
	}
	time.Sleep(500 * time.Millisecond) // let the runtime come up
	cancel()
	_ = cmd.Wait()

	out := stderr.String()
	if !strings.Contains(out, "goroutine ") {
		t.Fatalf("a cancelled child produced no goroutine dump, so every future "+
			"timeout will report nothing but its own silence.\nstderr:\n%s", out)
	}
	// The dump is only useful if it carries terva's own frames, not just the
	// signal handler's.
	if !strings.Contains(out, "terva") {
		t.Errorf("the dump names no terva frame, so it cannot locate a hang:\n%s", out)
	}
}
