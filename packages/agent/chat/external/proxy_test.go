package external

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/chat"
	"terva.sh/terva/packages/agent/connsdk"
)

// TestHelperConnector is not a test: it is the connector executable
// the proxy tests spawn. The proxy's manifest points exec at the test
// binary with args ["-test.run=^TestHelperConnector$", "<mode>"]; the
// verb terva appends lands as the last positional arg, exactly like a
// real connector. In a normal `go test` run there are no positional
// args, so this skips immediately.
func TestHelperConnector(t *testing.T) {
	args := flag.Args()
	if len(args) < 2 {
		t.Skip("helper process for proxy tests")
	}
	mode := args[0]
	switch mode {
	case "rawproto":
		// Speaks a future protocol the host must refuse. Raw frames,
		// no SDK (the SDK can't be made to lie about its version).
		fmt.Println(`{"type":"hello","name":"fake","protocol_min":99,"protocol_max":99,"capabilities":{}}`)
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "no-hello":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	}
	connsdk.Main(connsdk.Config{
		Name:    "fake",
		Version: "9.9.9",
		Capabilities: connsdk.Capabilities{
			MaxTextLen:    1234,
			TypingRefresh: 250 * time.Millisecond,
			SendsImages:   true,
			SendsFiles:    true,
		},
		NewTransport: func(s connsdk.Session) (connsdk.Transport, error) {
			return &fakeTransport{mode: mode, dataDir: s.DataDir, echo: make(chan connsdk.Message, 8)}, nil
		},
		Status:     func() (string, error) { return "fake status line", nil },
		Reset:      func() error { fmt.Println("fake reset ran"); return nil },
		Configured: func() bool { return mode != "configured-no" },
	})
	os.Exit(0) // suppress the testing framework's PASS chatter on stdout
}

// fakeTransport scripts one behavior per mode.
type fakeTransport struct {
	mode    string
	dataDir string
	echo    chan connsdk.Message
}

func (ft *fakeTransport) Connect(ctx context.Context) (connsdk.Identity, error) {
	if ft.mode == "connect-error" {
		return connsdk.Identity{}, errors.New("bad token")
	}
	return connsdk.Identity{ID: "77", Username: "fakebot"}, nil
}

func (ft *fakeTransport) Receive(ctx context.Context, deliver func(connsdk.Message)) error {
	switch ft.mode {
	case "crash-loop":
		deliver(connsdk.Message{ChatID: "c1", UserID: "u1", Text: "pre-crash"})
		os.Exit(3)
	case "crash-once":
		marker := filepath.Join(ft.dataDir, "crashed-once")
		if _, err := os.Stat(marker); err != nil {
			_ = os.WriteFile(marker, []byte("x"), 0o644)
			deliver(connsdk.Message{ChatID: "c1", UserID: "u1", Text: "first-life"})
			os.Exit(3)
		}
		deliver(connsdk.Message{ChatID: "c1", UserID: "u1", Text: "second-life"})
	case "attachment":
		good := filepath.Join(ft.dataDir, "in.png")
		_ = os.WriteFile(good, []byte("PNGDATA"), 0o644)
		evil := filepath.Join(ft.dataDir, "..", "evil.png")
		_ = os.WriteFile(evil, []byte("EVIL"), 0o644)
		deliver(connsdk.Message{ChatID: "c1", UserID: "u1", Text: "with attachments",
			Attachments: []connsdk.Attachment{
				{MimeType: "image/png", Path: good},
				{MimeType: "image/png", Path: evil},
			}})
	case "happy":
		deliver(connsdk.Message{ChatID: "c1", UserID: "u1", Username: "drew", ReplyTo: "m1", Text: "hello from chat"})
		for {
			select {
			case m := <-ft.echo:
				deliver(m)
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	<-ctx.Done()
	return ctx.Err()
}

func (ft *fakeTransport) Send(ctx context.Context, out connsdk.Outgoing) error {
	if ft.mode == "slow-send" {
		time.Sleep(5 * time.Second)
		return nil
	}
	if strings.Contains(out.Text, "boom") {
		return errors.New("boom requested")
	}
	ft.echo <- connsdk.Message{ChatID: out.ChatID, UserID: "u1", Text: "echo:" + out.Text}
	return nil
}

func (ft *fakeTransport) SendImage(ctx context.Context, chatID, path, caption string) error {
	return nil
}
func (ft *fakeTransport) SendFile(ctx context.Context, chatID, path, caption string) error {
	return nil
}
func (ft *fakeTransport) Typing(ctx context.Context, chatID string) error { return nil }

// ---- host-side harness ----

// warnLog collects proxy warnings across goroutines.
type warnLog struct {
	mu    sync.Mutex
	lines []string
}

func (w *warnLog) add(s string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lines = append(w.lines, s)
}

func (w *warnLog) joined() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.lines, "\n")
}

func helperManifest(t *testing.T, mode string) Manifest {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Manifest{
		Name:    "fake",
		Version: "9.9.9",
		Exec:    exe,
		Args:    []string{"-test.run=^TestHelperConnector$", mode},
	}
}

func newTestProxy(t *testing.T, mode string) (*Proxy, string, *warnLog) {
	t.Helper()
	tervaHome := t.TempDir()
	warns := &warnLog{}
	p := NewProxy(helperManifest(t, mode), t.TempDir(), tervaHome, warns.add)
	p.restartDelay = 5 * time.Millisecond
	return p, tervaHome, warns
}

// receiver runs p.Receive on its own goroutine and exposes the stream.
type receiver struct {
	msgs chan chat.Message
	done chan error
}

func startReceive(ctx context.Context, p *Proxy) *receiver {
	r := &receiver{msgs: make(chan chat.Message, 16), done: make(chan error, 1)}
	go func() {
		r.done <- p.Receive(ctx, func(m chat.Message) { r.msgs <- m })
	}()
	return r
}

func (r *receiver) next(t *testing.T) chat.Message {
	t.Helper()
	select {
	case m := <-r.msgs:
		return m
	case err := <-r.done:
		t.Fatalf("receive ended early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for a message")
	}
	return chat.Message{}
}

func (r *receiver) end(t *testing.T) error {
	t.Helper()
	select {
	case err := <-r.done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for Receive to return")
		return nil
	}
}

func TestProxyHappyPath(t *testing.T) {
	p, _, _ := newTestProxy(t, "happy")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	id, err := p.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if id.ID != "77" || id.Username != "fakebot" {
		t.Errorf("identity = %+v", id)
	}
	caps := p.Capabilities()
	if caps.MaxTextLen != 1234 || caps.TypingRefresh != 250*time.Millisecond || !caps.SendsImages || !caps.SendsFiles {
		t.Errorf("capabilities = %+v", caps)
	}

	r := startReceive(ctx, p)
	m := r.next(t)
	if m.Text != "hello from chat" || m.ChatID != "c1" || m.UserID != "u1" || m.Username != "drew" || m.ReplyTo != "m1" {
		t.Errorf("inbound message = %+v", m)
	}

	if err := p.Send(ctx, chat.Outgoing{ChatID: "c1", Text: "ping"}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if m := r.next(t); m.Text != "echo:ping" {
		t.Errorf("echo = %+v", m)
	}

	if err := p.Send(ctx, chat.Outgoing{ChatID: "c1", Text: "boom"}); err == nil || !strings.Contains(err.Error(), "boom requested") {
		t.Errorf("Send(boom) err = %v, want transport error", err)
	}
	if err := p.SendImage(ctx, "c1", "/tmp/x.png", "cap"); err != nil {
		t.Errorf("SendImage: %v", err)
	}
	if err := p.Typing(ctx, "c1"); err != nil {
		t.Errorf("Typing: %v", err)
	}

	cancel()
	if err := r.end(t); !errors.Is(err, context.Canceled) {
		t.Errorf("Receive returned %v, want context.Canceled", err)
	}
}

func TestProxyConnectError(t *testing.T) {
	p, _, _ := newTestProxy(t, "connect-error")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := p.Connect(ctx); err == nil || !strings.Contains(err.Error(), "bad token") {
		t.Fatalf("Connect err = %v, want bad token", err)
	}
}

func TestProxyProtocolMismatch(t *testing.T) {
	p, _, _ := newTestProxy(t, "rawproto")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := p.Connect(ctx)
	if err == nil || !strings.Contains(err.Error(), "speaks protocol 99..99") {
		t.Fatalf("Connect err = %v, want protocol mismatch", err)
	}
}

func TestProxyNoHello(t *testing.T) {
	p, _, _ := newTestProxy(t, "no-hello")
	p.helloTimeout = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := p.Connect(ctx); err == nil || !strings.Contains(err.Error(), "no hello") {
		t.Fatalf("Connect err = %v, want hello timeout", err)
	}
}

func TestProxyCrashRestart(t *testing.T) {
	p, _, warns := newTestProxy(t, "crash-once")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := p.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	r := startReceive(ctx, p)
	if m := r.next(t); m.Text != "first-life" {
		t.Fatalf("first message = %+v", m)
	}
	// The child crashes after that message; the proxy must restart it
	// and keep the same Receive call going.
	if m := r.next(t); m.Text != "second-life" {
		t.Fatalf("post-restart message = %+v", m)
	}
	if w := warns.joined(); !strings.Contains(w, "restarting") {
		t.Errorf("restart was silent; warns:\n%s", w)
	}
	cancel()
	_ = r.end(t)
}

func TestProxyCrashBudget(t *testing.T) {
	p, _, warns := newTestProxy(t, "crash-loop")
	p.restartMax = 2
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := p.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	r := startReceive(ctx, p)
	err := r.end(t)
	if err == nil || !strings.Contains(err.Error(), "keeps crashing") {
		t.Fatalf("Receive err = %v, want crash budget exhausted", err)
	}
	if w := warns.joined(); !strings.Contains(w, "attempt 1/2") || !strings.Contains(w, "attempt 2/2") {
		t.Errorf("expected per-attempt warns, got:\n%s", w)
	}
}

func TestProxyAttachmentIngestion(t *testing.T) {
	p, tervaHome, warns := newTestProxy(t, "attachment")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := p.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	r := startReceive(ctx, p)
	m := r.next(t)
	if len(m.Images) != 1 {
		t.Fatalf("got %d images, want 1 (outside-data-dir file must be refused); warns:\n%s", len(m.Images), warns.joined())
	}
	if string(m.Images[0].Data) != "PNGDATA" || m.Images[0].MimeType != "image/png" {
		t.Errorf("image = mime %q data %q", m.Images[0].MimeType, m.Images[0].Data)
	}
	dataDir := filepath.Join(ConnectorsDir(tervaHome), "fake", "data")
	if _, err := os.Stat(filepath.Join(dataDir, "in.png")); !os.IsNotExist(err) {
		t.Errorf("ingested attachment was not deleted")
	}
	if _, err := os.Stat(filepath.Join(ConnectorsDir(tervaHome), "fake", "evil.png")); err != nil {
		t.Errorf("file outside data dir must be left alone: %v", err)
	}
	if w := warns.joined(); !strings.Contains(w, "outside data dir") {
		t.Errorf("expected a warn about the outside path, got:\n%s", w)
	}
	cancel()
	_ = r.end(t)
}

func TestProxySendTimeout(t *testing.T) {
	p, _, _ := newTestProxy(t, "slow-send")
	p.sendTimeout = 200 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if _, err := p.Connect(ctx); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	err := p.Send(ctx, chat.Outgoing{ChatID: "c1", Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "no result within") {
		t.Fatalf("Send err = %v, want timeout", err)
	}
}

func TestProxySendWithoutProcess(t *testing.T) {
	p, _, _ := newTestProxy(t, "happy")
	err := p.Send(context.Background(), chat.Outgoing{ChatID: "c1", Text: "hi"})
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("Send err = %v, want not running", err)
	}
}
