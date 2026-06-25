package extdriver

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/testsupport"
)

// stubSessionReader is a fixed in-memory SessionReader for driver tests.
type stubSessionReader struct {
	list []extproto.SessionInfo
	msgs map[string][]extproto.SessionMessage
}

func (s stubSessionReader) ListSessions(_, _ string) []extproto.SessionInfo { return s.list }
func (s stubSessionReader) ReadSession(_, id string) ([]extproto.SessionMessage, bool) {
	m, ok := s.msgs[id]
	return m, ok
}

// TestSessionReadRoundTrip drives list_sessions then read_session from a
// shell extension and proves the injected reader's data comes back
// correlated. The extension announces what it saw via notify.
func TestSessionReadRoundTrip(t *testing.T) {
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "search")
	hello := `{"type":"hello","name":"search","version":"1.0","capabilities":["events"]}`
	body := `printf '%s\n' '{"type":"ready"}'
printf '%s\n' '{"type":"list_sessions","id":"l1"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"session_list"'*)
      case "$line" in *'"session_id":"sess-1"'*) printf '%s\n' '{"type":"notify","level":"info","message":"LISTED"}';; esac
      printf '%s\n' '{"type":"read_session","id":"r1","session_id":"sess-1"}'
      ;;
    *'"type":"session_data"'*)
      case "$line" in *'hello world'*) printf '%s\n' '{"type":"notify","level":"info","message":"READ"}';; esac
      ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
	writeShellExt(t, dir, hello, body)

	reader := stubSessionReader{
		list: []extproto.SessionInfo{{SessionID: "sess-1", Title: "t", Messages: 2}},
		msgs: map[string][]extproto.SessionMessage{
			"sess-1": {{Role: "user", Text: "hello world"}},
		},
	}
	hooks := &refreshRecorder{}
	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", hooks)
	d.SetSessionReader(reader)
	if err := d.Load(context.Background(), dir, Manifest{Name: "search", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer d.Stop(2 * time.Second)

	deadline := time.Now().Add(3 * time.Second)
	for !(hooks.sawNotify("LISTED") && hooks.sawNotify("READ")) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !hooks.sawNotify("LISTED") {
		t.Error("extension never received the session_list with the expected session")
	}
	if !hooks.sawNotify("READ") {
		t.Error("extension never received the session_data transcript")
	}
}

// TestSessionReadNoReader proves read_session with no reader wired
// returns not_found instead of hanging.
func TestSessionReadNoReader(t *testing.T) {
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "search2")
	hello := `{"type":"hello","name":"search2","version":"1.0","capabilities":["events"]}`
	body := `printf '%s\n' '{"type":"ready"}'
printf '%s\n' '{"type":"read_session","id":"r1","session_id":"x"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"session_data"'*) case "$line" in *'"not_found":true'*) printf '%s\n' '{"type":"notify","level":"info","message":"NOTFOUND"}';; esac ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
	writeShellExt(t, dir, hello, body)

	hooks := &refreshRecorder{}
	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", hooks)
	if err := d.Load(context.Background(), dir, Manifest{Name: "search2", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer d.Stop(2 * time.Second)

	deadline := time.Now().Add(3 * time.Second)
	for !hooks.sawNotify("NOTFOUND") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !hooks.sawNotify("NOTFOUND") {
		t.Fatal("read_session with no reader should return not_found")
	}
}
