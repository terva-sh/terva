package agent

import (
	"fmt"
	"io"
)

// reloadPolicy decides what a single reload request (a SIGHUP) does. It is split
// from the signal plumbing in reload_signal_unix.go so the decision can be
// tested without delivering a real signal or performing a real re-exec; enabled
// and trigger are injected (relaunch.Enabled / relaunch.Trigger in production).
//
// The contract is deliberate: a reload is the SAME Tier-1 self-restart the
// control plane and the terva_restart tool drive — it re-execs the installed
// binary in place, same PID, so there is a brief outage while the replacement
// boots. It therefore honors the --allow-restart master switch, and when that
// switch is off the request is LOGGED AND SWALLOWED — never allowed to fall
// through to SIGHUP's default disposition, which terminates the process.
func reloadPolicy(w io.Writer, enabled func() bool, trigger func(string) error) {
	if !enabled() {
		fmt.Fprintln(w, "terva: reload (SIGHUP) requested, but self-restart is off — pass --allow-restart to enable it; ignoring and continuing to serve")
		return
	}
	if err := trigger("reload (SIGHUP)"); err != nil {
		fmt.Fprintf(w, "terva: reload (SIGHUP) did not restart: %v — continuing to serve on the current image\n", err)
	}
}
