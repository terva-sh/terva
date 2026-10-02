//go:build terva_gosh

package build

// The terva_gosh seam: this file and its twin gosh_off.go are the only
// place the tag is read in this package. It is a spike (TKT-01M3V1C73R),
// so an environment variable selects the runner and no configuration key
// exists yet.

import (
	"os"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/agent/tools/gosh"
)

// bashRunner returns the bash tool's runner. TERVA_SHELL=inprocess selects
// the in-process shell over the workspace. Anything else keeps the host
// shell (nil).
//
// The in-process shell narrows what bash reaches, it never widens it: the
// host shell sees every file the user can, and this one sees the workspace
// and a private /tmp. So a variable is an acceptable switch for a spike.
func bashRunner(cwd string, sb *tools.Sandbox) tools.ShellRunner {
	if os.Getenv("TERVA_SHELL") != "inprocess" {
		return nil
	}
	root := cwd
	if sb != nil && sb.Root != "" {
		root = sb.Root
	}
	return gosh.New(root, sb)
}
