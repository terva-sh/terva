//go:build !terva_web

package agent

import (
	"context"
	"fmt"

	"terva.sh/terva/packages/agent/build"
)

// runServeMode is the no-tag stub: the supervisor is part of the web surface,
// so it rides the same opt-in build tag. Without it the `terva serve`
// subcommand still routes here and exits with a clear note instead of a
// missing-symbol link error, so `case mode.Serve` resolves in both builds.
func runServeMode(_ context.Context, _ build.Args, _ string) error {
	return fmt.Errorf("serve mode not built in: rebuild terva with -tags terva_web to enable `terva serve`")
}
