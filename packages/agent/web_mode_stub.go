//go:build !terva_web

package agent

import (
	"context"
	"fmt"

	"terva.sh/terva/packages/agent/build"
)

// runWebMode is the no-tag stub: the web control panel is an opt-in build
// (-tags terva_web), same discipline as the ACP mode and the connectors.
// Without the tag the `terva web` subcommand still routes here and exits with a
// clear note instead of a missing-symbol link error, so `case ModeWeb` resolves
// in both builds.
func runWebMode(_ context.Context, _ build.Args, _ string) error {
	return fmt.Errorf("web mode not built in: rebuild terva with -tags terva_web to enable `terva web`")
}
