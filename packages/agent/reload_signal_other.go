//go:build !unix

package agent

import "context"

// installReloadHandler is a no-op where SIGHUP and exec(2) do not exist
// (Windows). Self-restart is already unsupported there (relaunch.Supported()
// reports false), so there is nothing for a reload signal to drive.
func installReloadHandler(context.Context) {}
