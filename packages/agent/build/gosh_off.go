//go:build !terva_gosh

package build

import "terva.sh/terva/packages/agent/tools"

// bashRunner is the no-op twin of gosh_on.go: without terva_gosh the bash
// tool always runs the host shell.
func bashRunner(string, *tools.Sandbox) tools.ShellRunner { return nil }
