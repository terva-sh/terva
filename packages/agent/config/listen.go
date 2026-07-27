package config

// Where a running terva can be reached, published by the one process that can
// answer: the daemon serving the control plane.
//
// The problem this solves is that a client had no way to find a daemon it was
// not told about. `terva attach` defaulted to terva web's loopback port, and
// `terva ext config` — which must go through the running instance, because that
// instance owns config.json — could only be pointed at one by hand. On a
// deployment serving a filesystem socket there was no default that could work.
//
// The record lives INSIDE $TERVA_HOME, and that is the whole safety argument. A
// daemon found this way is by construction serving the same home the reader
// resolved, so a client cannot be routed at an unrelated instance holding
// someone else's config. An earlier attempt probed the default loopback port
// instead and immediately found a daemon serving a DIFFERENT home — the
// handshake does not say which home a daemon holds, so there was nothing to
// check the guess against. Reading a file out of the home under discussion has
// no such gap.
//
// One record per home, last writer wins. Two daemons serving one home is an
// odd shape — they would already be fighting over config.json — and naming the
// most recent one to bind is both the honest answer and the useful one. If that
// ever becomes a real deployment, this grows into a directory keyed by pid; it
// is deliberately not that yet.
//
// Liveness is a heartbeat, not a pid: a pid outlives the process that owned it,
// so probing one finds something very much alive that has nothing to do with
// this daemon (the same reasoning as workflow/runs, which restamps on the same
// cadence). A record that has stopped being restamped is ignored, so a crash
// leaves a file that reads as absent rather than as a daemon that isn't there.

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"terva.sh/terva/packages/privfs"
)

// ListenBeatInterval is how often a serving daemon restamps its record, and
// listenGrace is how long a reader waits before calling one stale. Three beats,
// so a single missed tick under load does not make a live daemon vanish.
const ListenBeatInterval = 10 * time.Second

const listenGrace = 3 * ListenBeatInterval

// ListenRecord names a reachable daemon. Nothing here is a secret: Auth reports
// only THAT a token is required, never what it is. A client that learns it needs
// one can say so instead of failing an opaque handshake, and the token itself
// keeps travelling the way it already does (TERVA_WEB_TOKEN, --token-file).
type ListenRecord struct {
	// Endpoint is spelled the way a client can use it verbatim — the same
	// forms `terva attach` accepts.
	Endpoint  string `json:"endpoint"`
	PID       int    `json:"pid,omitempty"`
	Version   string `json:"version,omitempty"`
	Auth      bool   `json:"auth,omitempty"`
	Started   string `json:"started,omitempty"`
	Heartbeat string `json:"heartbeat,omitempty"`
}

// ListenRecordPath is where the active home's record lives.
func ListenRecordPath() string { return filepath.Join(TervaHome(), "listen.json") }

// EndpointForListener spells a bound listener the way a client dials it.
//
// A wildcard bind is rewritten to loopback: "[::]:8730" is what a dual-stack
// listener reports and is not an address anything can connect TO, and this
// record is only ever read by something on the same machine as the file.
func EndpointForListener(network, addr string) string {
	if network == "unix" {
		return "unix:" + addr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "ws://" + addr + "/ws"
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	return "ws://" + net.JoinHostPort(host, port) + "/ws"
}

// PublishListenRecord writes rec and keeps it fresh until the returned stop is
// called, which also removes it. A missing home is created 0700 by privfs.
//
// stop is idempotent and safe to defer. It is deliberately NOT called before a
// self-restart re-exec: the replacement process overwrites the record within a
// beat, and removing it first would open a window where a client looking for
// this daemon finds nothing at all.
func PublishListenRecord(rec ListenRecord) (stop func(), err error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if rec.Started == "" {
		rec.Started = now
	}
	if rec.PID == 0 {
		rec.PID = os.Getpid()
	}
	rec.Heartbeat = now
	path := ListenRecordPath()
	if err := writeListenRecord(path, rec); err != nil {
		return func() {}, err
	}

	done := make(chan struct{})
	var once sync.Once
	go func() {
		t := time.NewTicker(ListenBeatInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				rec.Heartbeat = time.Now().UTC().Format(time.RFC3339)
				_ = writeListenRecord(path, rec)
			}
		}
	}()
	return func() {
		once.Do(func() {
			close(done)
			// Only remove a record still describing THIS process. A restart
			// that re-execs and rebinds has already overwritten it, and
			// deleting the successor's record on the way out would leave a
			// live daemon undiscoverable.
			if cur, err := readListenRecordAt(path); err == nil && cur.PID == rec.PID {
				_ = os.Remove(path)
			}
		})
	}, nil
}

func writeListenRecord(path string, rec ListenRecord) error {
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return privfs.WriteFile(path, b)
}

// ReadListenRecord returns the active home's daemon record, and false when
// there is none, it cannot be read, or it has gone stale. Stale is reported as
// absent on purpose: a caller that would treat "there is a record" as "there is
// a daemon" is exactly the caller a crashed daemon's leftovers would mislead.
func ReadListenRecord() (ListenRecord, bool) {
	rec, err := readListenRecordAt(ListenRecordPath())
	if err != nil || rec.Endpoint == "" {
		return ListenRecord{}, false
	}
	if !rec.FreshAt(time.Now()) {
		return ListenRecord{}, false
	}
	return rec, true
}

func readListenRecordAt(path string) (ListenRecord, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ListenRecord{}, err
	}
	var rec ListenRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return ListenRecord{}, err
	}
	return rec, nil
}

// FreshAt reports whether the record was restamped recently enough to believe.
// An unparsable or absent heartbeat is not fresh — a record that never learned
// to beat is from a version that could not have kept it current either.
func (r ListenRecord) FreshAt(now time.Time) bool {
	beat, err := time.Parse(time.RFC3339, r.Heartbeat)
	if err != nil {
		return false
	}
	return now.Sub(beat) <= listenGrace
}

// Describe names the record for a human: the endpoint, and the pid when it can
// help tell two daemons apart in a process list.
func (r ListenRecord) Describe() string {
	if r.PID == 0 {
		return r.Endpoint
	}
	return r.Endpoint + " (pid " + strconv.Itoa(r.PID) + ")"
}
