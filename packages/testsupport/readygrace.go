package testsupport

import "time"

// ExtReadyGrace is the deadline tests should pass to Manager/Driver
// WaitForReady when they need a spawned extension (or connector) to have
// completed its ready handshake before asserting ready-dependent state.
//
// It is a *ceiling*, not a fixed wait: WaitForReady returns the instant every
// loaded extension signals ready, so on the happy path this costs nothing.
// The grace only bites when a subprocess is genuinely slow to come up — and a
// truly-broken extension that never becomes ready still fails the following
// assertion, just later. So a generous value cannot weaken a test; it only
// removes false failures.
//
// It is deliberately generous because the extensions are real subprocesses
// (a /bin/sh script or a compiled helper) whose fork+exec+hello handshake time
// varies widely under `go test -race` and CI contention — Forgejo runs the
// suite in the push and pull_request contexts concurrently, so a slow-but-
// healthy spawn can blow past a tight grace. A 3s grace on a shell-script
// connector was the proven source of a flaky reconnect test; this is the one
// knob that keeps every ext-ready wait comfortably above that observed spread.
const ExtReadyGrace = 15 * time.Second
