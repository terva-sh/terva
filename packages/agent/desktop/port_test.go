package desktop

import (
	"net"
	"os"
	"path/filepath"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

func always(bool) func(int) bool { return func(int) bool { return true } }

// A second launch on the same home reuses the port the first one bound, so
// the webview opens the same origin and its saved localStorage.
func TestSecondLaunchOnTheSameHomeReusesThePort(t *testing.T) {
	home := testsupport.TempDir(t)
	path := filepath.Join(home, StateFile)

	if port, busy := PickPort(path, 0, always(true)); port != 0 || busy != 0 {
		t.Fatalf("first launch = (%d, %d); want a random port and nothing busy", port, busy)
	}
	if err := RememberPort(path, 41234); err != nil {
		t.Fatal(err)
	}
	if port, busy := PickPort(path, 0, always(true)); port != 41234 || busy != 0 {
		t.Fatalf("second launch = (%d, %d); want the remembered 41234", port, busy)
	}
}

// Another home has its own state file, so it never inherits a port, and with
// it the first home's preferences.
func TestALaunchOnAnotherHomeDoesNotInheritThePort(t *testing.T) {
	first := filepath.Join(testsupport.TempDir(t), StateFile)
	other := filepath.Join(testsupport.TempDir(t), StateFile)
	if err := RememberPort(first, 41234); err != nil {
		t.Fatal(err)
	}
	if port, busy := PickPort(other, 0, always(true)); port != 0 || busy != 0 {
		t.Fatalf("other home = (%d, %d); want a random port", port, busy)
	}
}

// A busy remembered port falls back to a random one and names the lost port,
// so the launch can say its preferences start fresh.
func TestABusyRememberedPortFallsBack(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), StateFile)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	held := l.Addr().(*net.TCPAddr).Port
	if err := RememberPort(path, held); err != nil {
		t.Fatal(err)
	}
	if PortFree(held) {
		t.Fatalf("PortFree(%d) = true while a listener holds it", held)
	}
	if port, busy := PickPort(path, 0, PortFree); port != 0 || busy != held {
		t.Fatalf("busy launch = (%d, %d); want (0, %d)", port, busy, held)
	}
}

// An explicit --desktop-port wins over the remembered one.
func TestAnExplicitPortWins(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), StateFile)
	if err := RememberPort(path, 41234); err != nil {
		t.Fatal(err)
	}
	if port, busy := PickPort(path, 9134, always(true)); port != 9134 || busy != 0 {
		t.Fatalf("explicit = (%d, %d); want 9134", port, busy)
	}
}

// A damaged state file reads as no memory, never as a refusal to start.
func TestADamagedStateFileIsIgnored(t *testing.T) {
	path := filepath.Join(testsupport.TempDir(t), StateFile)
	for _, body := range []string{"{", `{"port": 0}`, `{"port": 70000}`, `{"port": "x"}`} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if port, busy := PickPort(path, 0, always(true)); port != 0 || busy != 0 {
			t.Errorf("state %s = (%d, %d); want nothing remembered", body, port, busy)
		}
	}
}
