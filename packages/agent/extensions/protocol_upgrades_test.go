package extensions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
)

// writeShellExt drops a shell-script extension with the given hello
// frame and post-hello script body, plus its manifest. Returns the
// extension root to hand to New.
func writeShellExt(t *testing.T, name, helloJSON, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script extension; skip on windows")
	}
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "extensions", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' '" + helloJSON + "'\n" + body
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	mfb, _ := json.Marshal(map[string]any{"name": name, "exec": "./run.sh"})
	if err := os.WriteFile(filepath.Join(dir, "extension.json"), mfb, 0o644); err != nil {
		t.Fatal(err)
	}
	return tmp
}

// An extension whose hello declares a min_protocol higher than the
// host speaks is refused — Discover reports the version mismatch and
// none of its commands/tools register.
func TestMinProtocolNegotiationRejectsTooNew(t *testing.T) {
	hello := `{"type":"hello","name":"future-ext","version":"1.0","min_protocol":99,"capabilities":["commands"]}`
	// It would register a command if it were admitted; it must not be.
	body := `printf '%s\n' '{"type":"register_command","name":"shouldnotappear"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do :; done
`
	tmp := writeShellExt(t, "future-ext", hello, body)
	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	errs := mgr.Discover(context.Background())
	defer mgr.Stop(2 * time.Second)

	var sawVersionErr bool
	for _, e := range errs {
		if strings.Contains(e.Error(), "protocol version") {
			sawVersionErr = true
		}
	}
	if !sawVersionErr {
		t.Fatalf("expected a protocol-version error from Discover, got %v", errs)
	}
	if mgr.HasCommand("shouldnotappear") {
		t.Error("a min-protocol-rejected extension registered its command")
	}
}

// An extension declaring a min_protocol the host satisfies loads
// normally.
func TestMinProtocolNegotiationAcceptsSatisfiable(t *testing.T) {
	hello := `{"type":"hello","name":"ok-ext","version":"1.0","min_protocol":1,"capabilities":["commands"]}`
	body := `printf '%s\n' '{"type":"register_command","name":"ok"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in *'"type":"shutdown"'*) exit 0;; esac
done
`
	tmp := writeShellExt(t, "ok-ext", hello, body)
	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover errors: %v", errs)
	}
	defer mgr.Stop(2 * time.Second)
	time.Sleep(150 * time.Millisecond)
	if !mgr.HasCommand("ok") {
		t.Error("min_protocol:1 extension should load against a protocol-1 host")
	}
}

// When an extension subprocess exits on its own (crash) rather than
// via host shutdown, the manager surfaces a Notify so the user learns
// its tools are gone instead of hitting silent "unknown tool" errors.
func TestCrashSurfacing(t *testing.T) {
	hello := `{"type":"hello","name":"crasher","version":"1.0","capabilities":["commands"]}`
	// Say hello + ready, consume the host's hello_ack (so the
	// handshake completes and the read loop starts), then exit — a
	// crash from the host's point of view (no shutdown was requested).
	body := `printf '%s\n' '{"type":"ready"}'
IFS= read -r _line
exit 1
`
	tmp := writeShellExt(t, "crasher", hello, body)
	hooks := &stubHooks{}
	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", hooks)
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover errors: %v", errs)
	}
	defer mgr.Stop(2 * time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		hooks.mu.Lock()
		n := len(hooks.notifies)
		hooks.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	var sawCrash bool
	for _, n := range hooks.notifies {
		if strings.Contains(n, "crasher") && strings.Contains(n, "exited unexpectedly") {
			sawCrash = true
		}
	}
	if !sawCrash {
		t.Fatalf("expected a crash notice for the dead extension, got %v", hooks.notifies)
	}
}

// A clean host shutdown must NOT be reported as a crash.
func TestCleanShutdownIsNotACrash(t *testing.T) {
	hello := `{"type":"hello","name":"tidy","version":"1.0","capabilities":["commands"]}`
	body := `printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in *'"type":"shutdown"'*) printf '%s\n' '{"type":"shutdown_ack"}'; exit 0;; esac
done
`
	tmp := writeShellExt(t, "tidy", hello, body)
	hooks := &stubHooks{}
	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", hooks)
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover errors: %v", errs)
	}
	time.Sleep(150 * time.Millisecond)
	mgr.Stop(2 * time.Second)
	time.Sleep(150 * time.Millisecond)

	hooks.mu.Lock()
	defer hooks.mu.Unlock()
	for _, n := range hooks.notifies {
		if strings.Contains(n, "exited unexpectedly") {
			t.Fatalf("clean shutdown was reported as a crash: %q", n)
		}
	}
}

// tool_result events fan out to a subscribed extension: it subscribes
// to "tool_result", and an EmitEvent carrying a result reaches it.
func TestToolResultFanout(t *testing.T) {
	hello := `{"type":"hello","name":"watcher","version":"1.0","capabilities":["events"]}`
	// Subscribe to tool_result, then echo a marker into a file each
	// time one arrives so the test can observe receipt.
	tmp := writeShellExt(t, "watcher", hello, `printf '%s\n' '{"type":"subscribe","events":["tool_result"]}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in
    *'"event":"tool_result"'*) printf '%s' "$line" >> "$TR_OUT";;
    *'"type":"shutdown"'*) exit 0;;
  esac
done
`)
	out := filepath.Join(t.TempDir(), "tr.jsonl")
	t.Setenv("TR_OUT", out)

	hooks := &stubHooks{}
	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", hooks)
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover errors: %v", errs)
	}
	defer mgr.Stop(2 * time.Second)
	time.Sleep(150 * time.Millisecond)

	mgr.EmitEvent(extproto.EventFromHost{
		Event: "tool_result", ToolID: "call-9", Text: "RESULT-TEXT", IsError: false,
	})

	deadline := time.Now().Add(2 * time.Second)
	var data []byte
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(out); err == nil && len(b) > 0 {
			data = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(data) == 0 {
		t.Fatal("subscriber never received the tool_result event")
	}
	if !strings.Contains(string(data), "RESULT-TEXT") || !strings.Contains(string(data), "call-9") {
		t.Fatalf("tool_result payload missing fields: %s", data)
	}
}

// The protocol-2 ordering guarantee: a session_start event emitted
// before a tool invocation must reach the extension first, even though
// the two travel different host code paths (EmitEvent vs InvokeTool).
// The extension records the order it observes them; session_start must
// precede the tool_call. (Under the old fire-and-forget EmitEvent this
// raced; the single ordered writer makes it deterministic.)
func TestSessionStartOrderedBeforeToolCall(t *testing.T) {
	hello := `{"type":"hello","name":"orderer","version":"1.0","capabilities":["events","tools"]}`
	// Subscribe to session_start + register a tool. On session_start,
	// record the session_id and an "S" marker; on tool_call, record a
	// "T" marker and reply with a tool_result so InvokeTool returns.
	body := `printf '%s\n' '{"type":"subscribe","events":["session_start"]}'
printf '%s\n' '{"type":"register_tool","name":"rec","schema":{"type":"object"}}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in
    *'"event":"session_start"'*)
      printf 'S\n' >> "$ORDER_OUT"
      printf '%s' "$line" >> "$SESS_OUT"
      ;;
    *'"type":"tool_call"'*)
      printf 'T\n' >> "$ORDER_OUT"
      id=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
      printf '%s\n' "{\"type\":\"tool_result\",\"id\":\"$id\",\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}"
      ;;
    *'"type":"shutdown"'*) exit 0;;
  esac
done
`
	tmp := writeShellExt(t, "orderer", hello, body)
	orderOut := filepath.Join(t.TempDir(), "order.txt")
	sessOut := filepath.Join(t.TempDir(), "sess.json")
	t.Setenv("ORDER_OUT", orderOut)
	t.Setenv("SESS_OUT", sessOut)

	mgr := New(tmp, "", "0.0.0-test", "anthropic", "claude-opus-4-7", &stubHooks{})
	if errs := mgr.Discover(context.Background()); len(errs) > 0 {
		t.Fatalf("discover errors: %v", errs)
	}
	defer mgr.Stop(2 * time.Second)
	time.Sleep(150 * time.Millisecond)

	// Emit session_start, then immediately invoke the tool. The writer
	// must deliver them in this order.
	mgr.EmitEvent(extproto.EventFromHost{
		Event: "session_start", SessionID: "sess-xyz", SessionPath: "/x.tervasession",
	})
	if _, err := mgr.InvokeTool(context.Background(), "rec", json.RawMessage(`{}`), 2*time.Second); err != nil {
		t.Fatalf("InvokeTool: %v", err)
	}

	// By the time InvokeTool returned, the extension had processed both
	// frames in wire order. Assert S precedes T.
	deadline := time.Now().Add(2 * time.Second)
	var order string
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(orderOut); err == nil && strings.Contains(string(b), "T") {
			order = string(b)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	si := strings.Index(order, "S")
	ti := strings.Index(order, "T")
	if si < 0 || ti < 0 {
		t.Fatalf("missing markers; order log = %q", order)
	}
	if si > ti {
		t.Fatalf("session_start arrived AFTER tool_call (order = %q) — ordering guarantee broken", order)
	}
	if b, err := os.ReadFile(sessOut); err != nil || !strings.Contains(string(b), "sess-xyz") {
		t.Errorf("session_start did not carry the session id: %s (err=%v)", b, err)
	}
}
