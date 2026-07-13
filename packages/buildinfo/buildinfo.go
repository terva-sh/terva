// Package buildinfo holds the running binary's build identity —
// version, commit, and build timestamp — as a process-global set once
// at startup.
//
// These three values are injected into package main via -ldflags (see
// cmd/terva/main.go), where they are otherwise folded into a single
// display string and lose their structure. Recording them here, before
// that collapse, keeps the structured triple reachable by code that
// needs to report the running process's build — notably terva_status,
// which surfaces it for version-sensitive debugging, bug reports, and
// verifying that a self-restart loaded the expected binary.
//
// It mirrors the set-once idiom already used for the combined version
// string (external/extconn/connlocal SetTervaVersion): written once
// during startup, read many times afterward. A binary that never calls
// Set (an SDK embedder, a test) reads back the zero Info, and consumers
// degrade gracefully.
package buildinfo

import (
	"sync"
	"time"
)

// Info is the immutable build identity of the running binary.
type Info struct {
	// Version is the semantic/build version (e.g. "0.120.1"), or the
	// "0.0.0" placeholder for local / untagged builds.
	Version string
	// Commit is the VCS revision the binary was built from. It may be a
	// full or abbreviated hash, or empty for a build with no VCS info.
	Commit string
	// Date is the build timestamp (RFC 3339), or empty when unstamped.
	Date string
}

// String folds the triple into the canonical display string — e.g.
// "0.120.1 (8f5bd80, 2026-07-12T06:30:32Z)" — the same shape `terva
// --version` prints (cmd/terva/main.go delegates here). The commit is
// abbreviated to 7 chars; commit and date are omitted when absent. A
// zero Info renders "" so callers can drop the line entirely.
func (i Info) String() string {
	if i.Version == "" {
		return ""
	}
	s := i.Version
	if i.Commit != "" {
		short := i.Commit
		if len(short) > 7 {
			short = short[:7]
		}
		s += " (" + short
		if i.Date != "" {
			s += ", " + i.Date
		}
		s += ")"
	} else if i.Date != "" {
		s += " (" + i.Date + ")"
	}
	return s
}

var (
	mu      sync.RWMutex
	current Info
)

// Set records the running binary's build identity. Called once at CLI
// startup; a binary that skips it (SDK embedder, test) leaves the zero
// Info, which consumers render as "unavailable" rather than failing.
func Set(i Info) {
	mu.Lock()
	current = i
	mu.Unlock()
}

// Get returns the recorded build identity, or the zero Info if Set was
// never called.
func Get() Info {
	mu.RLock()
	defer mu.RUnlock()
	return current
}

// started is captured at package init, which for the terva binary is
// process start to within milliseconds — good enough for the uptime the
// status surfaces report.
var started = time.Now()

// Started returns when this process started. Uptime (time.Since) lets an
// operator confirm a self-restart actually replaced the process: the
// build identity may be unchanged, but the uptime resets.
func Started() time.Time { return started }
