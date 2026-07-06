package extconn

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/extdriver"
	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/testsupport"
)

// noopHooks satisfies extdriver.HostHooks for adapter tests.
type noopHooks struct{}

func (noopHooks) Notify(extName, level, message string)                                 {}
func (noopHooks) Submit(text string)                                                    {}
func (noopHooks) SubmitSlash(text string)                                               {}
func (noopHooks) Insert(text string)                                                    {}
func (noopHooks) Display(extName, text string)                                          {}
func (noopHooks) ClearNotes(extName string)                                             {}
func (noopHooks) OpenPanel(extName string, spec extproto.PanelSpec)                     {}
func (noopHooks) UpdatePanel(a, b, c string, d []string, e string, f []extproto.Widget) {}
func (noopHooks) ClosePanel(extName, panelID string)                                    {}
func (noopHooks) RefreshStatus()                                                        {}
func (noopHooks) RefreshContext()                                                       {}
func (noopHooks) RefreshTools()                                                         {}

// writeShellExt mirrors extdriver's test helper (package-internal there):
// a /bin/sh extension printing helloJSON then running body.
func writeShellExt(t *testing.T, dir, helloJSON, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script extension; skip on windows")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nprintf '%s\\n' '" + helloJSON + "'\n" + body
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// bindDriver loads one shell connector-extension into a fresh driver and
// binds it as the adapter host. Restores the previous binding on cleanup
// so parallel-package state can't leak between tests.
func bindDriver(t *testing.T, tmp, name, body string) *extdriver.Driver {
	t.Helper()
	dir := filepath.Join(tmp, name)
	writeShellExt(t, dir, `{"type":"hello","name":"`+name+`","version":"1.0"}`, body)
	d := extdriver.New(tmp, "", "0.0.0-test", "anthropic", "opus", noopHooks{})
	if err := d.Load(context.Background(), dir, extdriver.Manifest{Name: name, Exec: "./run.sh", Connector: true}); err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Cleanup(func() { d.Stop(2 * time.Second) })
	d.WaitForReady(3 * time.Second)
	BindHost(d)
	t.Cleanup(func() { BindHost(nil) })
	return d
}

// e2eShellBody scripts a connector-extension at the INNER protocol
// level: it answers chat_open by starting a hand-rolled connproto
// session through the tunnel — hello, then connected + one message on
// connect, then a result per send. attachments is spliced into the
// message frame by the test.
func e2eShellBody(attachments string) string {
	return `printf '%s\n' '{"type":"register_connector"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"chat_open"'*)
      sid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
      printf '{"type":"chat","id":"%s","frame":{"type":"hello","name":"e2e","protocol_min":1,"protocol_max":1,"capabilities":{"max_text_len":777,"typing_refresh_ms":4000}}}\n' "$sid"
      ;;
    *'"frame":{"type":"connect"}'*)
      printf '{"type":"chat","id":"%s","frame":{"type":"connected","id":"bot-1","username":"e2e-bot"}}\n' "$sid"
      printf '{"type":"chat","id":"%s","frame":{"type":"message","chat_id":"c1","user_id":"u1","username":"drew","text":"hi"` + attachments + `}}\n' "$sid"
      ;;
    *'"frame":{"type":"send"'*)
      rid=$(printf '%s' "$line" | sed -n 's/.*"frame":{"type":"send","id":"\([^"]*\)".*/\1/p')
      printf '{"type":"chat","id":"%s","frame":{"type":"result","id":"%s"}}\n' "$sid" "$rid"
      ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
}

// TestConnEndToEnd drives the adapter's whole chat.Connector surface —
// Connect (tunnel open + inner hello/hello_ack + connect, identity and
// caps from the INNER protocol), Receive (inbound message with
// attachment ingestion + containment), Send (acked) — against a real
// driver and a scriptable shell extension.
func TestConnEndToEnd(t *testing.T) {
	tmp := testsupport.TempDir(t)

	// The extension's data dir is tervaHome/ext-data/<name> (driver
	// convention), announced through the inner hello_ack. Pre-place one
	// legitimate attachment there and one escape attempt outside it;
	// only the first may be ingested.
	dataDir := filepath.Join(tmp, "ext-data", "e2e")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(dataDir, "pic.png")
	if err := os.WriteFile(inside, []byte("PNGBYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(tmp, "secret.bin")
	if err := os.WriteFile(outside, []byte("SECRET"), 0o644); err != nil {
		t.Fatal(err)
	}

	atts := `,"attachments":[{"mime_type":"image/png","path":"` + inside + `"},{"mime_type":"application/octet-stream","path":"` + outside + `"}]`
	bindDriver(t, tmp, "e2e", e2eShellBody(atts))

	var warnMu sync.Mutex
	var warns []string
	conn := NewConn("e2e", func(s string) {
		warnMu.Lock()
		warns = append(warns, s)
		warnMu.Unlock()
	})

	id, err := conn.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if id.ID != "bot-1" || id.Username != "e2e-bot" {
		t.Errorf("identity = %+v", id)
	}
	caps := conn.Capabilities()
	if caps.MaxTextLen != 777 || caps.TypingRefresh != 4*time.Second {
		t.Errorf("caps = %+v", caps)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan chat.Message, 1)
	recvErr := make(chan error, 1)
	go func() {
		recvErr <- conn.Receive(ctx, func(m chat.Message) { got <- m })
	}()

	select {
	case m := <-got:
		if m.ChatID != "c1" || m.UserID != "u1" || m.Text != "hi" {
			t.Errorf("message = %+v", m)
		}
		// Containment: exactly the inside attachment, ingested and deleted.
		if len(m.Images) != 1 || string(m.Images[0].Data) != "PNGBYTES" {
			t.Fatalf("images = %+v, want the one legitimate attachment", m.Images)
		}
		if _, err := os.Stat(inside); !errors.Is(err, os.ErrNotExist) {
			t.Error("ingested attachment should be deleted")
		}
		if _, err := os.Stat(outside); err != nil {
			t.Error("the escape-attempt file must be left untouched")
		}
		warnMu.Lock()
		sawContainmentWarn := false
		for _, w := range warns {
			if strings.Contains(w, "outside data dir") {
				sawContainmentWarn = true
			}
		}
		warnMu.Unlock()
		if !sawContainmentWarn {
			t.Error("expected a containment warning for the outside path")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("inbound message never arrived")
	}

	if err := conn.Send(context.Background(), chat.Outgoing{ChatID: "c1", Text: "pong"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Consumer leaves: Receive returns ctx.Err, the process stays up.
	cancel()
	select {
	case err := <-recvErr:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Receive returned %v, want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Receive never returned after cancel")
	}
}

// TestConnStreamDown: the extension repeatedly ending the session with
// a reason (chat_down — a permanently broken transport) exhausts the
// reopen budget and surfaces as a permanent Receive error carrying the
// reason. The process was alive throughout, so no RestartHost is
// needed for the attempts.
func TestConnStreamDown(t *testing.T) {
	tmp := testsupport.TempDir(t)
	body := `printf '%s\n' '{"type":"register_connector"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"chat_open"'*)
      sid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
      printf '{"type":"chat","id":"%s","frame":{"type":"hello","name":"downer","protocol_min":1,"protocol_max":1,"capabilities":{}}}\n' "$sid"
      ;;
    *'"frame":{"type":"connect"}'*)
      printf '{"type":"chat","id":"%s","frame":{"type":"connected","id":"b","username":"b"}}\n' "$sid"
      printf '{"type":"chat_down","id":"%s","error":"auth revoked"}\n' "$sid"
      ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
	bindDriver(t, tmp, "downer", body)

	conn := NewConn("downer", func(string) {})
	conn.restartDelay = 5 * time.Millisecond
	if _, err := conn.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	err := conn.Receive(context.Background(), func(chat.Message) {})
	if err == nil || !strings.Contains(err.Error(), "keeps dying") || !strings.Contains(err.Error(), "auth revoked") {
		t.Errorf("Receive error = %v, want budget exhaustion carrying the chat_down reason", err)
	}
}

// TestConnReopenAfterChatDown: one chat_down is a redial, not the end —
// the session is reopened on the live process (the engine re-dials via
// a fresh transport) and messages keep flowing, including any buffered
// when the old session died.
func TestConnReopenAfterChatDown(t *testing.T) {
	tmp := testsupport.TempDir(t)
	body := `printf '%s\n' '{"type":"register_connector"}'
printf '%s\n' '{"type":"ready"}'
n=0
while IFS= read -r line; do
  case "$line" in
    *'"type":"chat_open"'*)
      sid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
      printf '{"type":"chat","id":"%s","frame":{"type":"hello","name":"blip","protocol_min":1,"protocol_max":1,"capabilities":{}}}\n' "$sid"
      ;;
    *'"frame":{"type":"connect"}'*)
      n=$((n+1))
      printf '{"type":"chat","id":"%s","frame":{"type":"connected","id":"b","username":"b"}}\n' "$sid"
      printf '{"type":"chat","id":"%s","frame":{"type":"message","chat_id":"c1","user_id":"u1","text":"hello-%s"}}\n' "$sid" "$n"
      if [ "$n" = "1" ]; then
        printf '{"type":"chat_down","id":"%s","error":"gateway lost"}\n' "$sid"
      fi
      ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
	bindDriver(t, tmp, "blip", body)

	conn := NewConn("blip", func(string) {})
	conn.restartDelay = 5 * time.Millisecond
	if _, err := conn.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan chat.Message, 4)
	recvErr := make(chan error, 1)
	go func() {
		recvErr <- conn.Receive(ctx, func(m chat.Message) { got <- m })
	}()

	want := map[string]bool{"hello-1": false, "hello-2": false}
	for i := 0; i < 2; i++ {
		select {
		case m := <-got:
			want[m.Text] = true
		case err := <-recvErr:
			t.Fatalf("Receive returned before both sessions delivered: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatalf("messages after %d deliveries never arrived (got %v)", i, want)
		}
	}
	if !want["hello-1"] || !want["hello-2"] {
		t.Errorf("want a message from each session, got %v", want)
	}
	cancel()
	if err := <-recvErr; !errors.Is(err, context.Canceled) {
		t.Errorf("Receive after cancel = %v, want context.Canceled", err)
	}
}

// fakeRestartHost upgrades the driver binding with the respawn
// primitive, the way extensions.Manager does for real: stop (reaping
// the crashed entry) + a fresh Load of the same manifest.
type fakeRestartHost struct {
	d    *extdriver.Driver
	dir  string
	name string

	mu       sync.Mutex
	restarts int
}

func (h *fakeRestartHost) ConnectorExtension(name string) (*extdriver.Extension, bool) {
	return h.d.ConnectorExtension(name)
}

func (h *fakeRestartHost) RestartExtension(ctx context.Context, name string) error {
	h.mu.Lock()
	h.restarts++
	h.mu.Unlock()
	h.d.StopByName(name, time.Second)
	if err := h.d.Load(ctx, h.dir, extdriver.Manifest{Name: name, Exec: "./run.sh", Connector: true}); err != nil {
		return err
	}
	h.d.WaitForReady(3 * time.Second)
	return nil
}

// mortalBody scripts a process that crashes right after its FIRST
// connect (leaving a `booted` marker in its own dir) and behaves on
// every boot after — the crash-once shape a respawn must recover from.
const mortalBody = `printf '%s\n' '{"type":"register_connector"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"chat_open"'*)
      sid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
      printf '{"type":"chat","id":"%s","frame":{"type":"hello","name":"mortal","protocol_min":1,"protocol_max":1,"capabilities":{}}}\n' "$sid"
      ;;
    *'"frame":{"type":"connect"}'*)
      printf '{"type":"chat","id":"%s","frame":{"type":"connected","id":"b","username":"b"}}\n' "$sid"
      if [ -f booted ]; then
        printf '{"type":"chat","id":"%s","frame":{"type":"message","chat_id":"c1","user_id":"u1","text":"after-respawn"}}\n' "$sid"
      else
        touch booted
        exit 3
      fi
      ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`

// TestConnRespawnAfterProcessExit: a process death asks the host to
// respawn the extension, then reopens the session — the bot loop rides
// through the crash, and the respawned process's messages flow.
func TestConnRespawnAfterProcessExit(t *testing.T) {
	tmp := testsupport.TempDir(t)
	d := bindDriver(t, tmp, "mortal", mortalBody)
	host := &fakeRestartHost{d: d, dir: filepath.Join(tmp, "mortal"), name: "mortal"}
	BindHost(host)

	conn := NewConn("mortal", func(string) {})
	conn.restartDelay = 5 * time.Millisecond
	if _, err := conn.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan chat.Message, 1)
	recvErr := make(chan error, 1)
	go func() {
		recvErr <- conn.Receive(ctx, func(m chat.Message) { got <- m })
	}()

	select {
	case m := <-got:
		if m.Text != "after-respawn" {
			t.Errorf("message = %+v, want the respawned process's", m)
		}
	case err := <-recvErr:
		t.Fatalf("Receive returned instead of respawning: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no message after the respawn")
	}
	host.mu.Lock()
	restarts := host.restarts
	host.mu.Unlock()
	if restarts != 1 {
		t.Errorf("RestartExtension called %d times, want 1", restarts)
	}
}

// TestConnRespawnBudgetExhausted: a process that crashes on every boot
// consumes the budget and Receive returns the permanent error.
func TestConnRespawnBudgetExhausted(t *testing.T) {
	tmp := testsupport.TempDir(t)
	body := `printf '%s\n' '{"type":"register_connector"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"chat_open"'*)
      sid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
      printf '{"type":"chat","id":"%s","frame":{"type":"hello","name":"crashy","protocol_min":1,"protocol_max":1,"capabilities":{}}}\n' "$sid"
      ;;
    *'"frame":{"type":"connect"}'*)
      printf '{"type":"chat","id":"%s","frame":{"type":"connected","id":"b","username":"b"}}\n' "$sid"
      exit 3
      ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
	d := bindDriver(t, tmp, "crashy", body)
	host := &fakeRestartHost{d: d, dir: filepath.Join(tmp, "crashy"), name: "crashy"}
	BindHost(host)

	conn := NewConn("crashy", func(string) {})
	conn.restartDelay = 5 * time.Millisecond
	conn.restartMax = 2
	if _, err := conn.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	err := conn.Receive(context.Background(), func(chat.Message) {})
	if err == nil || !strings.Contains(err.Error(), "keeps dying") {
		t.Errorf("Receive error = %v, want budget exhaustion", err)
	}
}

// TestConnProcessExitPermanentWithoutRestartHost: with a bare driver
// binding (no respawn primitive) a process death is immediately
// permanent, with an error saying why recovery didn't happen.
func TestConnProcessExitPermanentWithoutRestartHost(t *testing.T) {
	tmp := testsupport.TempDir(t)
	body := `printf '%s\n' '{"type":"register_connector"}'
printf '%s\n' '{"type":"ready"}'
while IFS= read -r line; do
  case "$line" in
    *'"type":"chat_open"'*)
      sid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
      printf '{"type":"chat","id":"%s","frame":{"type":"hello","name":"mortal","protocol_min":1,"protocol_max":1,"capabilities":{}}}\n' "$sid"
      ;;
    *'"frame":{"type":"connect"}'*)
      printf '{"type":"chat","id":"%s","frame":{"type":"connected","id":"b","username":"b"}}\n' "$sid"
      exit 3
      ;;
    *'"type":"shutdown"'*) exit 0 ;;
  esac
done
`
	bindDriver(t, tmp, "mortal", body)

	conn := NewConn("mortal", func(string) {})
	if _, err := conn.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	err := conn.Receive(context.Background(), func(chat.Message) {})
	if err == nil || !strings.Contains(err.Error(), "cannot respawn") {
		t.Errorf("Receive error = %v, want the cannot-respawn explanation", err)
	}
}

// TestConnectRefusedWithoutRole: an extension that never registered the
// role (or isn't loaded) fails Connect with a clear error.
func TestConnectRefusedWithoutRole(t *testing.T) {
	tmp := testsupport.TempDir(t)
	d := extdriver.New(tmp, "", "0.0.0-test", "anthropic", "opus", noopHooks{})
	BindHost(d)
	t.Cleanup(func() { BindHost(nil) })

	conn := NewConn("ghost", func(string) {})
	conn.roleWait = 200 * time.Millisecond // don't burn the full grace in a test
	_, err := conn.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did not register the connector role") {
		t.Errorf("Connect error = %v, want the role explanation", err)
	}
}

// TestConnectRefusedWithoutHost: no bound extension subsystem (a mode
// that never set one up) is a clear error, not a hang.
func TestConnectRefusedWithoutHost(t *testing.T) {
	BindHost(nil)
	conn := NewConn("anything", func(string) {})
	_, err := conn.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "extension subsystem not available") {
		t.Errorf("Connect error = %v, want the no-host explanation", err)
	}
}

// TestRegisterDiscovered scans a fake global extensions dir: only
// enabled, connector-flagged manifests become chat services.
func TestRegisterDiscovered(t *testing.T) {
	tmp := testsupport.TempDir(t)
	extsDir := filepath.Join(tmp, "extensions")
	write := func(name, manifest string) {
		dir := filepath.Join(extsDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "extension.json"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("xc-conn", `{"name":"xc-conn","exec":"./x","connector":true}`)
	write("xc-plain", `{"name":"xc-plain","exec":"./x"}`)
	write("xc-disabled", `{"name":"xc-disabled","exec":"./x","connector":true,"enabled":false}`)

	if errs := RegisterDiscovered(tmp, extsDir); len(errs) != 0 {
		t.Fatalf("errors: %v", errs)
	}

	if svc, ok := chat.Lookup("xc-conn"); !ok {
		t.Error("connector-flagged extension should be a chat service")
	} else if svc.Kind != "extension" {
		t.Errorf("service Kind = %q, want extension", svc.Kind)
	}
	if _, ok := chat.Lookup("xc-plain"); ok {
		t.Error("plain extension must NOT be a chat service")
	}
	if _, ok := chat.Lookup("xc-disabled"); ok {
		t.Error("disabled extension must NOT be a chat service")
	}

	// The service's pairing is host-side and survives a reset round trip.
	svc, _ := chat.Lookup("xc-conn")
	_, pairing, err := svc.NewConnector(tmp, nil)
	if err != nil {
		t.Fatalf("NewConnector: %v", err)
	}
	if pairing.AllowedUserID != "" {
		t.Errorf("fresh pairing = %q, want unpaired", pairing.AllowedUserID)
	}
	if err := pairing.Save("user-42"); err != nil {
		t.Fatalf("pairing save: %v", err)
	}
	if got := loadPairing(tmp, "xc-conn"); got != "user-42" {
		t.Errorf("persisted pairing = %q, want user-42", got)
	}
	if _, err := svc.Reset(tmp); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := loadPairing(tmp, "xc-conn"); got != "" {
		t.Errorf("pairing after reset = %q, want unpaired", got)
	}
}
