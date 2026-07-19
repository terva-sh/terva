//go:build !terva_workflows

package agent

import "fmt"

// runWorkflowCommand is the no-workflows twin of workflowcmd_on.go: the
// subcommand is still RECOGNIZED, so a user typing `terva workflow run`
// on a lean build learns why it's absent instead of hitting an
// unknown-argument error.
func runWorkflowCommand(rawArgs []string, version string) (bool, error) {
	if len(rawArgs) == 0 || rawArgs[0] != "workflow" {
		return false, nil
	}
	return true, fmt.Errorf("this binary was built without workflows — rebuild with -tags terva_workflows (the full install/release builds carry it)")
}
