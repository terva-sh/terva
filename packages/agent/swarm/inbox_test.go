package swarm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

// shortSocketDir returns a tempdir short enough that we can append
// "in.sock" and still fit under the unix-socket path cap. The Go
// runner's t.TempDir() lives under a long /var/folders/... path on
// macOS which blows the 104-byte limit by itself, so the inbox tests
// can't use it.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("swarm inbox transport uses Unix-domain sockets; Windows support will use a named-pipe backend")
	}
	dir, err := os.MkdirTemp("/tmp", "terva-in-")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// TestInboxRoundTrip is the happy path: child listens, supervisor
// dials and sends three lines, child receives them in order, and
// closing both sides is clean. Locks in the line-framed protocol.
func TestInboxRoundTrip(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "in.sock")
	ln, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	got := collectFor(t, ln.Lines(), 3, time.Second)
	inbox := NewInbox(path)
	defer inbox.Close()

	want := []string{"user hello", "user world", "cancel"}
	for _, msg := range want {
		if err := inbox.SendInput(msg); err != nil {
			t.Fatalf("SendInput(%q): %v", msg, err)
		}
	}

	gotLines := <-got
	if len(gotLines) != len(want) {
		t.Fatalf("got %d lines, want %d: %v", len(gotLines), len(want), gotLines)
	}
	for i, w := range want {
		if gotLines[i] != w {
			t.Errorf("line %d = %q; want %q", i, gotLines[i], w)
		}
	}
}

// TestInboxNotReady regression-tests the explicit "child hasn't
// opened the socket yet" case. The TUI needs an error it can show
// as "agent not listening yet" rather than a generic dial failure.
func TestInboxNotReady(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "in.sock")
	inbox := NewInbox(path)
	defer inbox.Close()
	err := inbox.SendInput("user hi")
	if err == nil {
		t.Fatal("SendInput on missing socket: want error")
	}
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("SendInput err = %v; want ErrNotReady", err)
	}
}

// TestIsNoListenerErrMatchesECONNREFUSED documents the contract
// that the user's screenshot regression depends on: any dial error
// whose root cause is "the kernel refused the connect because nobody
// is listening" must fold into ErrNotReady at the SendInput layer,
// so the TUI shows "agent not accepting input (press R to resume)"
// instead of leaking the unix-socket path. We pattern-match via
// errors.Is on syscall.ECONNREFUSED rather than string-matching the
// error text so platform variants all hit the same branch.
func TestIsNoListenerErrMatchesECONNREFUSED(t *testing.T) {
	if !isNoListenerErr(syscall.ECONNREFUSED) {
		t.Error("isNoListenerErr(ECONNREFUSED) = false; want true")
	}
	// Wrapped via fmt.Errorf — common form of the dial error.
	if !isNoListenerErr(fmt.Errorf("dial unix /tmp/x: %w", syscall.ECONNREFUSED)) {
		t.Error("isNoListenerErr(wrapped) = false; want true")
	}
	if isNoListenerErr(syscall.ECONNRESET) {
		t.Error("isNoListenerErr(ECONNRESET) = true; want false (unrelated errno)")
	}
	if isNoListenerErr(nil) {
		t.Error("isNoListenerErr(nil) = true; want false")
	}
}

// TestInboxNewSupervisorPreemptsOld documents that only one
// supervisor connection at a time is honoured. The swarm design
// assumes a single parent terva owns each agent; if a second
// parent dials, the listener boots the first one. The first
// supervisor's already-delivered messages still land in the
// channel — what we're guarding against is silent dual ownership,
// not message loss.
func TestInboxNewSupervisorPreemptsOld(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "in.sock")
	ln, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	first := NewInbox(path)
	if err := first.SendInput("user one"); err != nil {
		t.Fatalf("first send: %v", err)
	}
	// Wait for the listener to absorb "user one" before dialing the
	// second supervisor; otherwise we race against the connection
	// preemption and "user one" can be dropped (the first conn is
	// closed by Accept before its read loop processes the byte).
	waitN(t, ln.Lines(), 1, time.Second)

	second := NewInbox(path)
	defer second.Close()
	if err := second.SendInput("user two"); err != nil {
		t.Fatalf("second send: %v", err)
	}

	secondLines := waitN(t, ln.Lines(), 1, time.Second)
	if len(secondLines) != 1 || secondLines[0] != "user two" {
		t.Fatalf("second supervisor not received: %v", secondLines)
	}

	// The original supervisor's connection was preempted by the
	// listener accepting the second dial. The next SendInput on the
	// first handle sees a broken pipe and drops the cached connection;
	// the call after that redials successfully. Both behaviours
	// matter: the broken-pipe error must surface (so a real
	// supervisor knows it lost ownership) AND the next call must
	// recover so a flaky reconnect doesn't permanently wedge the
	// inbox.
	if err := first.SendInput("user three"); err == nil {
		t.Fatal("expected broken-pipe error on stale connection")
	}
	if err := first.SendInput("user four"); err != nil {
		t.Fatalf("first redial after eviction: %v", err)
	}
}

func waitN(t *testing.T, ch <-chan string, n int, timeout time.Duration) []string {
	t.Helper()
	var got []string
	deadline := time.After(timeout)
	for len(got) < n {
		select {
		case msg := <-ch:
			got = append(got, msg)
		case <-deadline:
			return got
		}
	}
	return got
}

// collectFor consumes up to n items from ch within timeout and
// pushes the slice onto the returned channel. Used by the tests
// above so they don't race against the listener's accept goroutine.
func collectFor(t *testing.T, ch <-chan string, n int, timeout time.Duration) <-chan []string {
	t.Helper()
	out := make(chan []string, 1)
	go func() {
		var got []string
		deadline := time.After(timeout)
		for len(got) < n {
			select {
			case msg, ok := <-ch:
				if !ok {
					out <- got
					return
				}
				got = append(got, msg)
			case <-deadline:
				out <- got
				return
			}
		}
		out <- got
	}()
	return out
}

// TestInboxConcurrentSends serialises the SendInput call so two
// goroutines writing at once still produce two whole lines, never
// a tearing of bytes between them.
func TestInboxConcurrentSends(t *testing.T) {
	path := filepath.Join(shortSocketDir(t), "in.sock")
	ln, err := Listen(path)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()
	inbox := NewInbox(path)
	defer inbox.Close()

	var wg sync.WaitGroup
	const n = 50
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			if err := inbox.SendInput("user msg"); err != nil {
				t.Errorf("send %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	collected := collectFor(t, ln.Lines(), n, 2*time.Second)
	got := <-collected
	if len(got) != n {
		t.Fatalf("got %d lines, want %d", len(got), n)
	}
	for i, g := range got {
		if g != "user msg" {
			t.Fatalf("line %d garbled: %q", i, g)
		}
	}
}
