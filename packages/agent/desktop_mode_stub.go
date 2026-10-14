//go:build !terva_desktop || !terva_web

package agent

import (
	"context"
	"terva.sh/terva/packages/i18n"

	"terva.sh/terva/packages/agent/build"
)

func runDesktopMode(_ context.Context, _ build.Args, _ string) error {
	return i18n.Errorf("desktop mode not built in; install the full terva build or build with -tags terva_desktop,terva_web")
}
