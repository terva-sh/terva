package extdriver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/testsupport"
)

// TestHostToolCallRoundTrip drives a host_tool_call frame from a shell
// extension through the injected dispatcher and proves the extension
// gets the correlated host_tool_result back. The extension echoes a
// notify once it reads the result, so the test can observe the full
// ext → host → ext round trip.
func TestHostToolCallRoundTrip(t *testing.T) {
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "caller")
	hello := `{"type":"hello","name":"caller","version":"1.0","capabilities":["events"]}`
	// Emit a host_tool_call, then watch stdin for the host_tool_result
	// and announce it via notify so the test can synchronise.
	body := `printf '%s\n' '{"type":"ready"}'
printf '%s\n' '{"type":"host_tool_call","id":"c1","name":"read","args":{"path":"x"},"silent":false}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"host_tool_result"'*) printf '%s\n' '{"type":"notify","level":"info","message":"GOT-RESULT"}' ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
	writeShellExt(t, dir, hello, body)

	var (
		mu       sync.Mutex
		gotExt   string
		gotTool  string
		gotArgs  string
		gotCount int
	)
	hooks := &refreshRecorder{} // reused recorder: captures the notify
	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", hooks)
	d.SetHostToolDispatcher(func(_ context.Context, extName, toolName string, args json.RawMessage, silent bool) ([]extproto.ContentBlock, bool) {
		mu.Lock()
		gotExt, gotTool, gotArgs, gotCount = extName, toolName, string(args), gotCount+1
		mu.Unlock()
		return []extproto.ContentBlock{{Type: "text", Text: "file contents"}}, false
	})

	if err := d.Load(context.Background(), dir, Manifest{Name: "caller", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer d.Stop(2 * time.Second)

	deadline := time.Now().Add(3 * time.Second)
	for !hooks.sawNotify("GOT-RESULT") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !hooks.sawNotify("GOT-RESULT") {
		t.Fatal("extension never received the host_tool_result")
	}

	mu.Lock()
	defer mu.Unlock()
	if gotCount != 1 {
		t.Errorf("dispatcher called %d times, want 1", gotCount)
	}
	if gotExt != "caller" || gotTool != "read" {
		t.Errorf("dispatch got ext=%q tool=%q, want caller/read", gotExt, gotTool)
	}
	if !strings.Contains(gotArgs, `"path":"x"`) {
		t.Errorf("dispatch args = %q, want the call's args", gotArgs)
	}
}

// TestHostToolCallNoDispatcher proves a host_tool_call with no dispatcher
// wired gets an error result rather than hanging the extension.
func TestHostToolCallNoDispatcher(t *testing.T) {
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "caller2")
	hello := `{"type":"hello","name":"caller2","version":"1.0","capabilities":["events"]}`
	body := `printf '%s\n' '{"type":"ready"}'
printf '%s\n' '{"type":"host_tool_call","id":"c1","name":"read","args":{},"silent":false}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"host_tool_result"'*) case "$line" in *'"is_error":true'*) printf '%s\n' '{"type":"notify","level":"info","message":"GOT-ERR"}';; esac ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
	writeShellExt(t, dir, hello, body)

	hooks := &refreshRecorder{}
	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", hooks)
	// No dispatcher set.
	if err := d.Load(context.Background(), dir, Manifest{Name: "caller2", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer d.Stop(2 * time.Second)

	deadline := time.Now().Add(3 * time.Second)
	for !hooks.sawNotify("GOT-ERR") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !hooks.sawNotify("GOT-ERR") {
		t.Fatal("a host_tool_call with no dispatcher should return an error result")
	}
}
