package extdriver

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

// tunnelShellBody is a scriptable connector-extension at the TUNNEL
// level: it registers one tool and the connector role, then answers the
// envelope lifecycle — chat_open with one inner frame, and every later
// chat envelope with a verbatim echo. The driver never parses inner
// frames, so neither does this script; the inner protocol is exercised
// one layer up (chat/extconn against chat/connhost).
const tunnelShellBody = `printf '%s\n' '{"type":"register_tool","name":"noop","schema":{"type":"object"}}'
printf '%s\n' '{"type":"register_connector"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"chat_open"'*)
      sid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
      printf '{"type":"chat","id":"%s","frame":{"type":"hello","name":"shellconn"}}\n' "$sid"
      ;;
    *'"type":"chat_close"'*) : ;;
    *'"type":"chat"'*)
      printf '%s\n' "$line"
      ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`

// loadConnectorExt spawns one shell extension under a fresh driver with
// the given manifest connector consent.
func loadConnectorExt(t *testing.T, name, body string, consent bool) *Driver {
	t.Helper()
	tmp := testsupport.TempDir(t)
	dir := filepath.Join(tmp, name)
	writeShellExt(t, dir, `{"type":"hello","name":"`+name+`","version":"1.0"}`, body)
	d := New(tmp, "", "0.0.0-test", "anthropic", "opus", stubHooks{})
	if err := d.Load(context.Background(), dir, Manifest{Name: name, Exec: "./run.sh", Connector: consent}); err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Cleanup(func() { d.Stop(2 * time.Second) })
	d.WaitForReady(testsupport.ExtReadyGrace)
	return d
}

// readFrameTimeout guards a blocking ReadFrame with a test deadline.
func readFrameTimeout(t *testing.T, tun *ChatTunnel) ([]byte, error) {
	t.Helper()
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		b, err := tun.ReadFrame()
		ch <- res{b, err}
	}()
	select {
	case r := <-ch:
		return r.b, r.err
	case <-time.After(3 * time.Second):
		t.Fatal("ReadFrame never returned")
		return nil, nil
	}
}

// TestConnectorRoleGate: the manifest's Connector flag is the consent
// surface — the same register_connector frame is accepted with it and
// refused without it.
func TestConnectorRoleGate(t *testing.T) {
	consented := loadConnectorExt(t, "consented", tunnelShellBody, true)
	if _, ok := consented.ConnectorExtension("consented"); !ok {
		t.Error("consented extension should have the connector role")
	}

	denied := loadConnectorExt(t, "denied", tunnelShellBody, false)
	if _, ok := denied.ConnectorExtension("denied"); ok {
		t.Error("register_connector without the manifest flag must be refused")
	}
}

// TestChatTunnelRoundTrip drives the tunnel's whole carrier surface:
// OpenChat announces the session and the first inner frame arrives;
// WriteFrame wraps an inner frame that comes back verbatim (the script
// echoes); the driver stays byte-transparent in both directions.
func TestChatTunnelRoundTrip(t *testing.T) {
	d := loadConnectorExt(t, "shellconn", tunnelShellBody, true)
	ext, ok := d.ConnectorExtension("shellconn")
	if !ok {
		t.Fatal("connector role not registered")
	}

	tun, err := ext.OpenChat()
	if err != nil {
		t.Fatalf("OpenChat: %v", err)
	}
	defer tun.Close()

	first, err := readFrameTimeout(t, tun)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !strings.Contains(string(first), `"type":"hello"`) {
		t.Errorf("first inner frame = %s, want the script's hello", first)
	}

	// A second concurrent session must lose loudly.
	if _, err := ext.OpenChat(); err == nil || !strings.Contains(err.Error(), "already open") {
		t.Errorf("second OpenChat error = %v, want already-open refusal", err)
	}

	inner := `{"type":"ping","payload":"xyzzy"}`
	if err := tun.WriteFrame([]byte(inner)); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	echo, err := readFrameTimeout(t, tun)
	if err != nil {
		t.Fatalf("ReadFrame(echo): %v", err)
	}
	if string(echo) != inner {
		t.Errorf("echoed inner frame = %s, want %s (byte-transparent tunnel)", echo, inner)
	}
}

// TestChatTunnelReopenAfterClose: Close ends the session host-side and a
// fresh OpenChat starts a new one — the serial-session lifecycle.
func TestChatTunnelReopenAfterClose(t *testing.T) {
	d := loadConnectorExt(t, "reopen", tunnelShellBody, true)
	ext, ok := d.ConnectorExtension("reopen")
	if !ok {
		t.Fatal("connector role not registered")
	}

	tun, err := ext.OpenChat()
	if err != nil {
		t.Fatalf("OpenChat: %v", err)
	}
	tun.Close()
	if _, err := tun.ReadFrame(); !errors.Is(err, io.EOF) {
		t.Errorf("ReadFrame after Close = %v, want io.EOF", err)
	}
	if err := tun.WriteFrame([]byte(`{"type":"x"}`)); err == nil {
		t.Error("WriteFrame after Close should fail")
	}

	tun2, err := ext.OpenChat()
	if err != nil {
		t.Fatalf("OpenChat after Close: %v", err)
	}
	defer tun2.Close()
	if _, err := readFrameTimeout(t, tun2); err != nil {
		t.Fatalf("second session's first frame: %v", err)
	}
}

// TestChatDownClosesTunnel: a chat_down ends the consumer's stream with
// the extension's reason, while the process (and its tools) stays up.
func TestChatDownClosesTunnel(t *testing.T) {
	body := `printf '%s\n' '{"type":"register_connector"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"chat_open"'*)
      sid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
      printf '{"type":"chat_down","id":"%s","error":"auth revoked"}\n' "$sid"
      ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
	d := loadConnectorExt(t, "downer", body, true)
	ext, ok := d.ConnectorExtension("downer")
	if !ok {
		t.Fatal("connector role not registered")
	}
	tun, err := ext.OpenChat()
	if err != nil {
		t.Fatalf("OpenChat: %v", err)
	}
	_, err = readFrameTimeout(t, tun)
	if err == nil || !strings.Contains(err.Error(), "auth revoked") {
		t.Errorf("ReadFrame = %v, want the chat_down reason", err)
	}
	if got := tun.DownReason(); got != "auth revoked" {
		t.Errorf("DownReason = %q, want the extension's reason", got)
	}
}

// TestProcessExitClosesTunnel: the read loop's teardown ends a live
// tunnel so a consumer learns the process died (io.EOF, no reason).
func TestProcessExitClosesTunnel(t *testing.T) {
	body := `printf '%s\n' '{"type":"register_connector"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"chat_open"'*) exit 3 ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
	d := loadConnectorExt(t, "mortal", body, true)
	ext, ok := d.ConnectorExtension("mortal")
	if !ok {
		t.Fatal("connector role not registered")
	}
	tun, err := ext.OpenChat()
	if err != nil {
		t.Fatalf("OpenChat: %v", err)
	}
	if _, err := readFrameTimeout(t, tun); !errors.Is(err, io.EOF) {
		t.Errorf("ReadFrame = %v, want io.EOF (process exit, no chat_down reason)", err)
	}
}
