package extdriver

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// TestConfigDeliveryHandshakeAndUpdate proves the resolver's values ride
// hello_ack and that PushConfigUpdate reaches a subscribed extension as a
// config_update event. The shell extension echoes a marker when it sees
// the handshake token, and another when a config_update arrives.
func TestConfigDeliveryHandshakeAndUpdate(t *testing.T) {
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, "cfg")
	hello := `{"type":"hello","name":"cfg","version":"1.0","capabilities":["events"]}`
	body := `printf '%s\n' '{"type":"ready"}'
printf '%s\n' '{"type":"subscribe","events":["config_update"]}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"hello_ack"'*)
      case "$line" in *'tok-abc'*) printf '%s\n' '{"type":"notify","level":"info","message":"HELLO-CFG"}' ;; esac ;;
    *'"event":"config_update"'*) printf '%s\n' '{"type":"notify","level":"info","message":"CFG-UPDATE"}' ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
	writeShellExt(t, dir, hello, body)

	hooks := &recordingHooks{}
	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", hooks)
	d.SetConfigResolver(func(name string, schema []ConfigField) map[string]json.RawMessage {
		return map[string]json.RawMessage{"token": json.RawMessage(`"tok-abc"`)}
	})

	if err := d.Load(context.Background(), dir, Manifest{Name: "cfg", Exec: "./run.sh"}); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer d.Stop(2 * time.Second)

	// The handshake carried the config.
	deadline := time.Now().Add(3 * time.Second)
	for !hooks.sawNotify("HELLO-CFG") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !hooks.sawNotify("HELLO-CFG") {
		t.Fatal("extension never saw its config in hello_ack")
	}

	// PushConfigUpdate reaches the subscriber. Retry until the subscribe
	// frame has been processed (the no-op-if-unsubscribed guard), bounded.
	deadline = time.Now().Add(3 * time.Second)
	for !hooks.sawNotify("CFG-UPDATE") && time.Now().Before(deadline) {
		d.PushConfigUpdate("cfg", map[string]json.RawMessage{"token": json.RawMessage(`"tok-xyz"`)})
		time.Sleep(20 * time.Millisecond)
	}
	if !hooks.sawNotify("CFG-UPDATE") {
		t.Fatal("subscribed extension never received the config_update")
	}
}

// TestPushConfigUpdateUnknownExtensionNoop: pushing to an extension that
// isn't loaded must not panic.
func TestPushConfigUpdateUnknownExtensionNoop(t *testing.T) {
	d := New(testsupport.TempDir(t), "", "0.0.0-test", "anthropic", "opus", &recordingHooks{})
	d.PushConfigUpdate("nope", map[string]json.RawMessage{"a": json.RawMessage(`1`)})
}
