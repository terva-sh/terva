package extdriver

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

func TestBlockedExtensionHelper(t *testing.T) {
	args := flag.Args()
	if len(args) != 1 {
		return
	}
	fmt.Println(`{"type":"hello","name":"blocked","version":"1"}`)
	r := bufio.NewReader(os.Stdin)
	if _, err := r.ReadString('\n'); err != nil {
		os.Exit(2)
	}
	fmt.Println(`{"type":"ready"}`)
	if args[0] == "responsive" {
		for {
			line, err := r.ReadString('\n')
			if strings.Contains(line, `"shutdown"`) {
				os.Exit(0)
			}
			if err != nil {
				os.Exit(3)
			}
		}
	}
	if _, err := r.ReadByte(); err != nil {
		os.Exit(2)
	}
	fmt.Println(`{"type":"notify","level":"info","message":"stdin stopped"}`)
	time.Sleep(time.Hour)
	os.Exit(4)
}

type blockedHooks struct {
	stubHooks
	stopped chan struct{}
}

func (h *blockedHooks) Notify(_, _, message string) {
	if message == "stdin stopped" {
		close(h.stopped)
	}
}

func awaitExtension(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatalf("%s did not complete", what)
	}
}

func TestBlockedExtensionStopAndRestart(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := testsupport.TempDir(t)
	h := &blockedHooks{stopped: make(chan struct{})}
	d := New(dir, "", "test", "", "", h)
	mf := Manifest{Name: "blocked", Exec: exe, Args: []string{"-test.run=^TestBlockedExtensionHelper$", "blocked"}}
	if err := d.Load(context.Background(), dir, mf); err != nil {
		t.Fatal(err)
	}
	d.mu.RLock()
	ext := d.ext["blocked"]
	d.mu.RUnlock()
	t.Cleanup(func() {
		select {
		case <-ext.waitDone:
		default:
			killExtensionGroup(ext.cmd.Process)
		}
		d.Stop(time.Second)
	})
	awaitExtension(t, ext.readyCh, "startup")
	if err := ext.enqueueFrame([]byte(strings.Repeat("x", 2<<20)+"\n"), true); err != nil {
		t.Fatal(err)
	}
	awaitExtension(t, h.stopped, "child stopping stdin reads")
	// The active frame exceeds pipe capacity. Fill the host queue as well.
	for i := 0; i < outboxCapacity; i++ {
		if err := ext.enqueueFrame([]byte("{}\n"), false); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	call := make(chan error, 1)
	go func() {
		_, err := invokeTool(ctx, ext, "blocked", nil, time.Minute)
		call <- err
	}()
	cancel()
	select {
	case err := <-call:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled queue caller = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled tool call remained stuck in the full outbox")
	}
	timed := make(chan error, 1)
	go func() {
		_, err := invokeTool(context.Background(), ext, "blocked", nil, 100*time.Millisecond)
		timed <- err
	}()
	select {
	case err := <-timed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("full outbox timeout = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tool deadline did not include the full outbox")
	}
	ext.mu.Lock()
	pending := len(ext.pendingTool)
	ext.mu.Unlock()
	if pending != 0 {
		t.Fatalf("%d cancelled or expired calls remain registered", pending)
	}
	waiter := make(chan error, 1)
	go func() { waiter <- ext.writeFrame(map[string]string{"type": "pending"}) }()
	stopped := make(chan struct{})
	go func() { d.StopByName("blocked", 100*time.Millisecond); close(stopped) }()
	awaitExtension(t, stopped, "StopByName with a full pipe and outbox")
	awaitExtension(t, ext.writerDone, "writer exit")
	awaitExtension(t, ext.waitDone, "process reap")
	if ext.cmd.ProcessState == nil {
		t.Fatal("process has no Wait result")
	}
	select {
	case err := <-waiter:
		if !errors.Is(err, errExtStopped) {
			t.Fatalf("queued writer = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not release the queued writer")
	}
	// Reusing the name after teardown must create a healthy child.
	mf.Args[1] = "responsive"
	if err := d.Load(context.Background(), dir, mf); err != nil {
		t.Fatal(err)
	}
	d.mu.RLock()
	restarted := d.ext["blocked"]
	d.mu.RUnlock()
	awaitExtension(t, restarted.readyCh, "restart")
	d.Stop(3 * time.Second)
	awaitExtension(t, restarted.waitDone, "graceful reap")
	if restarted.waitErr != nil {
		t.Fatalf("responsive child did not receive shutdown: %v", restarted.waitErr)
	}
}
