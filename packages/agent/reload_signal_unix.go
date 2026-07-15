//go:build unix

package agent

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"terva.sh/terva/packages/relaunch"
)

// installReloadHandler wires SIGHUP to the Tier-1 self-restart, so an operator
// can swap a running daemon for the freshly-installed binary in place — e.g. a
// systemd unit with `ExecReload=/bin/kill -HUP $MAINPID`, driven by
// `systemctl reload`. SIGHUP is OS-authenticated (only the unit's owner or root
// can send it), so it needs no bearer token; it still respects --allow-restart.
//
// The handler is installed UNCONDITIONALLY — even when restart is disabled — on
// purpose: an unhandled SIGHUP terminates the process, so a `systemctl reload`
// against a daemon started without --allow-restart would kill it. Handling it
// lets the reload be swallowed with a clear log instead (see reloadPolicy).
func installReloadHandler(ctx context.Context) {
	installReloadHandlerWith(ctx, os.Stderr, relaunch.Enabled, relaunch.Trigger)
}

// installReloadHandlerWith is the injectable core: the log sink and the
// enabled/trigger seam are parameters so a test can drive the real signal path
// deterministically (synchronize on the sink, assert on the trigger) without
// touching process-global relaunch state or the real stderr. signal.Notify runs
// before the goroutine, so SIGHUP's disposition is diverted the moment this
// returns. The goroutine stops when ctx is cancelled; on a successful restart it
// is torn down with the replaced image.
func installReloadHandlerWith(ctx context.Context, w io.Writer, enabled func() bool, trigger func(string) error) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGHUP)
	go func() {
		defer signal.Stop(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ch:
				reloadPolicy(w, enabled, trigger)
			}
		}
	}()
}
