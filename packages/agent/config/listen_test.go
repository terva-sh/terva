package config

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"terva.sh/terva/packages/testsupport"
)

func TestEndpointForListenerSpellsWhatAClientCanDial(t *testing.T) {
	for _, tc := range []struct{ network, addr, want string }{
		{"unix", "/run/user/1000/terva.sock", "unix:/run/user/1000/terva.sock"},
		{"tcp", "127.0.0.1:8730", "ws://127.0.0.1:8730/ws"},
		// A wildcard bind is not an address anything connects TO, and the only
		// reader of this record is on the same machine as the file.
		{"tcp", "[::]:8730", "ws://127.0.0.1:8730/ws"},
		{"tcp", "0.0.0.0:8730", "ws://127.0.0.1:8730/ws"},
		{"tcp", "192.168.1.5:8730", "ws://192.168.1.5:8730/ws"},
	} {
		if got := EndpointForListener(tc.network, tc.addr); got != tc.want {
			t.Errorf("EndpointForListener(%q, %q) = %q; want %q", tc.network, tc.addr, got, tc.want)
		}
	}
}

func TestListenRecordRoundTripAndRemoval(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	if _, ok := ReadListenRecord(); ok {
		t.Fatal("no daemon has published anything yet")
	}

	stop, err := PublishListenRecord(ListenRecord{
		Endpoint: "unix:/run/terva.sock", Version: "0.0.0-test", Auth: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := ReadListenRecord()
	if !ok {
		t.Fatal("a just-published record should read back")
	}
	if rec.Endpoint != "unix:/run/terva.sock" || !rec.Auth || rec.PID != os.Getpid() {
		t.Errorf("record = %+v; want the endpoint, the auth flag, and this pid", rec)
	}
	if rec.Heartbeat == "" || rec.Started == "" {
		t.Errorf("record = %+v; want both stamps filled in", rec)
	}

	stop()
	if _, err := os.Stat(ListenRecordPath()); !os.IsNotExist(err) {
		t.Errorf("stop should remove the record, stat err = %v", err)
	}
	stop() // idempotent
}

// A crashed daemon leaves its file behind. Readers must see that as "no daemon",
// not as one that is simply not answering — the whole point of a heartbeat.
func TestStaleListenRecordReadsAsAbsent(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	old := ListenRecord{
		Endpoint:  "unix:/run/terva.sock",
		PID:       999999,
		Heartbeat: time.Now().Add(-10 * ListenBeatInterval).UTC().Format(time.RFC3339),
	}
	if err := writeListenRecord(ListenRecordPath(), old); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadListenRecord(); ok {
		t.Error("a record that stopped beating must read as absent")
	}
	// One missed tick is not a death.
	fresh := old
	fresh.Heartbeat = time.Now().Add(-ListenBeatInterval).UTC().Format(time.RFC3339)
	if err := writeListenRecord(ListenRecordPath(), fresh); err != nil {
		t.Fatal(err)
	}
	if _, ok := ReadListenRecord(); !ok {
		t.Error("one beat of slack must still read as live")
	}
	// A record with no heartbeat at all is not fresh: it predates the field, so
	// nothing was keeping it current either.
	none := old
	none.Heartbeat = ""
	if none.FreshAt(time.Now()) {
		t.Error("a record with no heartbeat must not read as fresh")
	}
}

// A restart re-execs and rebinds, overwriting the record with the new pid. The
// outgoing process must not delete its successor's record on the way out.
func TestStopLeavesASuccessorsRecordAlone(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	stop, err := PublishListenRecord(ListenRecord{Endpoint: "unix:/run/terva.sock"})
	if err != nil {
		t.Fatal(err)
	}
	successor := ListenRecord{
		Endpoint:  "unix:/run/terva.sock",
		PID:       os.Getpid() + 1,
		Heartbeat: time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeListenRecord(ListenRecordPath(), successor); err != nil {
		t.Fatal(err)
	}
	stop()
	b, err := os.ReadFile(ListenRecordPath())
	if err != nil {
		t.Fatalf("the successor's record was removed: %v", err)
	}
	var got ListenRecord
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.PID != successor.PID {
		t.Errorf("record pid = %d; want the successor's %d", got.PID, successor.PID)
	}
}

// The record carries whether a token is needed, never the token.
func TestListenRecordCarriesNoSecret(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	stop, err := PublishListenRecord(ListenRecord{Endpoint: "ws://127.0.0.1:8730/ws", Auth: true})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	b, err := os.ReadFile(ListenRecordPath())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"token", "secret", "password"} {
		if _, ok := raw[k]; ok {
			t.Errorf("the record must not carry %q: %s", k, b)
		}
	}
	if raw["auth"] != true {
		t.Errorf("auth flag = %v; want true", raw["auth"])
	}
}
