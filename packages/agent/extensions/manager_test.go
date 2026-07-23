package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/testsupport"
)

// stubHooks records every callback so the test can assert on them.
type stubHooks struct {
	mu         sync.Mutex
	notifies   []string
	displays   []string
	clearNotes []string
	panels     []extproto.PanelSpec
	panelExts  []string
}

func (s *stubHooks) Notify(name, level, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notifies = append(s.notifies, name+":"+level+":"+message)
}
func (s *stubHooks) Submit(string)      {}
func (s *stubHooks) SubmitSlash(string) {}
func (s *stubHooks) Insert(string)      {}
func (s *stubHooks) Display(name, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.displays = append(s.displays, name+":"+text)
}
func (s *stubHooks) ClearNotes(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearNotes = append(s.clearNotes, name)
}
func (s *stubHooks) OpenPanel(extName string, spec extproto.PanelSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panelExts = append(s.panelExts, extName)
	s.panels = append(s.panels, spec)
}
func (s *stubHooks) UpdatePanel(string, string, string, []string, string, []extproto.Widget) {}
func (s *stubHooks) ClosePanel(string, string)                                               {}
func (s *stubHooks) RefreshStatus()                                                          {}
func (s *stubHooks) RefreshContext()                                                         {}
func (s *stubHooks) RefreshTools()                                                           {}

// writeMockExtension creates a minimal extension on disk that uses a
// shell script (or batch file on windows) to drive the protocol. The
// script reads commands from stdin and emits hard-coded responses,
// exercising the manager's spawn/handshake/dispatch path without
// needing the SDK.
func writeMockExtension(t *testing.T, root string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("mock extension uses /bin/sh; skip on windows")
	}

	dir := filepath.Join(root, "mock")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Shell script: emit hello, read frames, respond. Reads until
	// stdin closes; tail's -F keeps the pipe alive long enough for
	// the manager to send command_invoked.
	script := `#!/bin/sh
printf '%s\n' '{"type":"hello","name":"mock","version":"0.0.1","capabilities":["commands"]}'
printf '%s\n' '{"type":"register_command","name":"ping","description":"ping/pong"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"command_invoked"'*)
      id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
      printf '%s\n' "{\"type\":\"command_response\",\"id\":\"$id\",\"action\":\"display\",\"display\":\"pong\"}"
      ;;
    *'"type":"shutdown"'*)
      printf '%s\n' '{"type":"shutdown_ack"}'
      exit 0
      ;;
  esac
done
`
	scriptPath := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := map[string]any{
		"name":    "mock",
		"version": "0.0.1",
		"exec":    "./run.sh",
	}
	mfb, _ := json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverLoadsThemeOnlyExtension(t *testing.T) {
	tmp := testsupport.TempDir(t)
	extDir := filepath.Join(tmp, "extensions", "theme-only")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"theme-only","version":"1.0.0","description":"theme only"}`
	if err := os.WriteFile(filepath.Join(extDir, "extension.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	theme := `{"name":"Theme Only","description":"theme from extension","colors":{"dark":{"accent":204}}}`
	if err := os.WriteFile(filepath.Join(extDir, "theme.json"), []byte(theme), 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", nil)
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover errors: %v", errs)
	}
	defer mgr.Stop(10 * time.Millisecond)

	// A theme-only extension (no exec) still loads and is tracked, so the
	// tui-aware host can discover its bundled theme. The ThemeOption
	// conversion itself is asserted in the agent layer
	// (TestExtensionThemeOptions), where the tui dependency lives.
	var found bool
	for _, e := range mgr.All() {
		if e.Manifest.Name == "theme-only" {
			found = true
		}
	}
	if !found {
		t.Fatalf("theme-only extension was not loaded; All() = %v", mgr.All())
	}
}

func TestManagerSpawnAndInvoke(t *testing.T) {
	tmp := testsupport.TempDir(t)
	extRoot := filepath.Join(tmp, "extensions")
	if err := os.MkdirAll(extRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMockExtension(t, extRoot)

	hooks := &stubHooks{}
	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", hooks)

	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover errors: %v", errs)
	}
	defer mgr.Stop(2 * time.Second)

	// Give the extension a beat to send register_command frames after
	// the hello handshake.
	time.Sleep(150 * time.Millisecond)

	cmds := mgr.Commands()
	found := false
	for _, c := range cmds {
		if c.Name == "ping" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected command 'ping', got %#v", cmds)
	}
	if !mgr.HasCommand("ping") {
		t.Fatal("HasCommand(\"ping\") = false")
	}

	resp, err := mgr.Invoke(context.Background(), "ping", "", 2*time.Second)
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if resp.Action != "display" {
		t.Errorf("expected action=display, got %q", resp.Action)
	}
	if resp.Display != "pong" {
		t.Errorf("expected display=pong, got %q", resp.Display)
	}
}

// TestSpontaneousOpenPanel verifies that an extension sending an
// open_panel frame outside of any command response causes the manager
// to call hooks.OpenPanel with the correct PanelSpec fields.
func TestSpontaneousOpenPanel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mock extension uses /bin/sh; skip on windows")
	}

	tmp := testsupport.TempDir(t)
	extDir := filepath.Join(tmp, "extensions", "panel-mock")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Extension emits hello + ready, then immediately fires a
	// spontaneous open_panel, then waits for shutdown.
	script := `#!/bin/sh
printf '%s\n' '{"type":"hello","name":"panel-mock","version":"0.1","capabilities":["panels"]}'
printf '%s\n' '{"type":"ready"}'
printf '%s\n' '{"type":"open_panel","panel":{"id":"test-panel","title":"Hello Panel","lines":["line one","line two"],"footer":"esc close"}}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"shutdown"'*)
      printf '%s\n' '{"type":"shutdown_ack"}'
      exit 0
      ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(extDir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mfb, _ := json.Marshal(map[string]any{"name": "panel-mock", "exec": "./run.sh"})
	if err := os.WriteFile(filepath.Join(extDir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}

	hooks := &stubHooks{}
	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", hooks)
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover errors: %v", errs)
	}
	defer mgr.Stop(2 * time.Second)

	// Give the extension time to flush its open_panel frame.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hooks.mu.Lock()
		n := len(hooks.panels)
		hooks.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	hooks.mu.Lock()
	defer hooks.mu.Unlock()

	if len(hooks.panels) == 0 {
		t.Fatal("hooks.OpenPanel was never called")
	}
	spec := hooks.panels[0]
	if spec.ID != "test-panel" {
		t.Errorf("panel id: want %q, got %q", "test-panel", spec.ID)
	}
	if spec.Title != "Hello Panel" {
		t.Errorf("panel title: want %q, got %q", "Hello Panel", spec.Title)
	}
	if len(spec.Lines) != 2 || spec.Lines[0] != "line one" || spec.Lines[1] != "line two" {
		t.Errorf("panel lines: want [line one line two], got %v", spec.Lines)
	}
	if spec.Footer != "esc close" {
		t.Errorf("panel footer: want %q, got %q", "esc close", spec.Footer)
	}
	if hooks.panelExts[0] != "panel-mock" {
		t.Errorf("ext name: want %q, got %q", "panel-mock", hooks.panelExts[0])
	}
}

// TestHandshakeTimeoutSkipsExtension verifies that an extension binary
// that opens but never prints a hello frame (a daemon, a REPL, a
// typo'd path) does not hang terva startup. Discover must return inside
// extdriver.HelloTimeout (plus slack), report the failure as an error, and leave
// the manager usable with no extension registered.
func TestHandshakeTimeoutSkipsExtension(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub extension uses /bin/sh; skip on windows")
	}

	tmp := testsupport.TempDir(t)
	extDir := filepath.Join(tmp, "extensions", "silent")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Never prints hello. Blocks on stdin so the process stays alive
	// indefinitely until terva kills it on handshake timeout.
	script := "#!/bin/sh\ncat >/dev/null\n"
	if err := os.WriteFile(filepath.Join(extDir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mfb, _ := json.Marshal(map[string]any{"name": "silent", "exec": "./run.sh"})
	if err := os.WriteFile(filepath.Join(extDir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	defer mgr.Stop(time.Second)
	// Shorten the deadline rather than sit out the shipping default: what this
	// test asserts is that the timeout FIRES and bounds Discover, not what its
	// value is (that is TestNewExtensionManagerAppliesTheConfiguredHelloTimeout).
	const timeout = 2 * time.Second
	mgr.SetHelloTimeout(timeout)

	start := time.Now()
	errs := mgr.Discover(context.Background())
	elapsed := time.Since(start)

	// Must return promptly after the handshake timeout, not hang.
	if elapsed > timeout+3*time.Second {
		t.Fatalf("Discover took %s; expected to return near the hello timeout (%s)", elapsed, timeout)
	}
	// And it must actually have waited for the timeout, not bailed early
	// for some other reason.
	if elapsed < timeout {
		t.Fatalf("Discover returned in %s, before the hello timeout (%s)", elapsed, timeout)
	}
	// The failure must be reported.
	if len(errs) == 0 {
		t.Fatal("Discover reported no error for a never-handshaking extension")
	}
	var sawHandshakeErr bool
	for _, e := range errs {
		if strings.Contains(e.Error(), "hello") {
			sawHandshakeErr = true
		}
	}
	if !sawHandshakeErr {
		t.Fatalf("expected a hello/handshake error, got %v", errs)
	}
	// The extension must not be registered.
	if len(mgr.All()) != 0 {
		t.Fatalf("silent extension was registered despite failing handshake: %v", mgr.All())
	}
}

// TestConcurrentLargeFramesNoInterleave fires many concurrent
// InvokeTool calls, each carrying a payload well over PIPE_BUF (4KiB),
// at a single extension. The stub parses every tool_call line as JSON
// and echoes the payload's length back; if any two frames had
// interleaved on the shared stdin pipe, the stub's per-line JSON parse
// would fail or report the wrong length. Run under -race to also catch
// unsynchronised writes to the pipe.
func TestConcurrentLargeFramesNoInterleave(t *testing.T) {
	// The stub (testdata/cmd/echostub) registers `bigtool` and replies
	// to each tool_call with the byte length of args.payload. It's a
	// compiled Go binary rather than a shell+python stub on purpose:
	// python stdin read-ahead / per-spawn timing occasionally left a
	// call unanswered (a 60s timeout that looked like a host bug but
	// wasn't), so a missing reply here now means a genuine host-side
	// frame corruption — exactly what this test guards against.
	stub := buildEchoStub(t)

	tmp := testsupport.TempDir(t)
	extDir := filepath.Join(tmp, "extensions", "echo")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mfb, _ := json.Marshal(map[string]any{"name": "echo", "exec": stub})
	if err := os.WriteFile(filepath.Join(extDir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover errors: %v", errs)
	}
	defer mgr.Stop(2 * time.Second)
	mgr.WaitForReady(testsupport.ExtReadyGrace)

	if !mgr.HasTool("bigtool") {
		t.Fatal("bigtool not registered")
	}

	// Fire many concurrent calls, each with a distinct large payload so
	// a mixed-up reply (wrong length) is detectable. Payloads vary in
	// size and all exceed PIPE_BUF (4KiB) — interleaved writes would
	// corrupt a frame at n=2, so 16 concurrent writers is ample
	// detection headroom. The stub answers calls sequentially (one
	// python3 spawn per line), so keep n modest and the per-call
	// deadline generous: under a saturated `go test -race ./...` sweep
	// those spawns slow down, and a tight deadline would flake without
	// any frame actually corrupting.
	const n = 16
	ctx := context.Background()
	var wg sync.WaitGroup
	errsCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			size := 5000 + i*200 // 5000..8000 bytes, all > 4KiB
			payload := strings.Repeat("x", size)
			args, _ := json.Marshal(map[string]string{"payload": payload})
			res, err := mgr.InvokeTool(ctx, "bigtool", json.RawMessage(args), 60*time.Second)
			if err != nil {
				errsCh <- fmt.Errorf("call %d: %w", i, err)
				return
			}
			if res.IsError {
				errsCh <- fmt.Errorf("call %d: stub reported error: %s", i, toolText(res))
				return
			}
			got := strings.TrimSpace(toolText(res))
			want := strconv.Itoa(size)
			if got != want {
				errsCh <- fmt.Errorf("call %d: payload length %q, want %q (frame corruption)", i, got, want)
			}
		}(i)
	}
	wg.Wait()
	close(errsCh)
	for e := range errsCh {
		t.Error(e)
	}
}

// TestSpawnSanitizesChildEnv proves the procenv trust boundary at the
// process level: an LD_PRELOAD set in the host's environment must not
// reach a spawned extension, while benign vars pass through. Asserted
// through the live subprocess (envtool echoes os.Getenv) rather than
// by unit-testing the filter, so a regression in the spawn wiring —
// not just in procenv itself — fails the test.
func TestSpawnSanitizesChildEnv(t *testing.T) {
	t.Setenv("LD_PRELOAD", "/tmp/evil.so")
	t.Setenv("TERVA_ENVTEST_CANARY", "alive")

	stub := buildEchoStub(t)
	tmp := testsupport.TempDir(t)
	extDir := filepath.Join(tmp, "extensions", "echo")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mfb, _ := json.Marshal(map[string]any{"name": "echo", "exec": stub})
	if err := os.WriteFile(filepath.Join(extDir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}

	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover errors: %v", errs)
	}
	defer mgr.Stop(2 * time.Second)
	mgr.WaitForReady(testsupport.ExtReadyGrace)

	ask := func(key string) string {
		args, _ := json.Marshal(map[string]string{"key": key})
		res, err := mgr.InvokeTool(context.Background(), "envtool", json.RawMessage(args), 30*time.Second)
		if err != nil {
			t.Fatalf("envtool(%s): %v", key, err)
		}
		return toolText(res)
	}
	if got := ask("LD_PRELOAD"); got != "" {
		t.Errorf("child saw LD_PRELOAD=%q, want stripped", got)
	}
	if got := ask("TERVA_ENVTEST_CANARY"); got != "alive" {
		t.Errorf("child saw canary %q, want %q (sanitizer over-stripped)", got, "alive")
	}
}

// toolText concatenates the text blocks of a tool result.
func toolText(r extproto.ToolResultFromExt) string {
	var b strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// buildEchoStub compiles testdata/cmd/echostub into a temp binary and
// returns its path, mirroring swarm's buildStubChild. A compiled Go
// stub gives the frame-integrity test a deterministic extension with no
// interpreter/stdin-buffering variance.
func buildEchoStub(t *testing.T) string {
	t.Helper()
	out := filepath.Join(testsupport.TempDir(t), "echostub")
	if runtime.GOOS == "windows" {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./testdata/cmd/echostub")
	// Pass the test runner's env so `go build` finds HOME/PATH/GOCACHE;
	// disable CGO for a hermetic build across machines.
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build echostub: %v\n%s", err, b)
	}
	return out
}
