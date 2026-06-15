//go:build !terva_acp

package agent

import (
	"context"
	"fmt"
)

// runACPMode is the no-tag stub: the ACP mode is an opt-in build
// (-tags terva_acp), same discipline as the telegram connector. Without the
// tag the `terva acp` subcommand still routes here and exits with a clear
// note instead of a missing-symbol link error, so `case ModeACP` resolves in
// both builds.
func runACPMode(_ context.Context, _ Args, _ string) error {
	return fmt.Errorf("acp mode not built in: rebuild terva with -tags terva_acp to enable `terva acp`")
}
