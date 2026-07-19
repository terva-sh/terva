//go:build terva_workflows

package agent

import (
	"strings"
	"testing"
)

func TestWorkflowCommandDispatch(t *testing.T) {
	if handled, _ := runWorkflowCommand([]string{"rpc"}, "test"); handled {
		t.Fatal("non-workflow argv must not be handled")
	}
	handled, err := runWorkflowCommand([]string{"workflow"}, "test")
	if !handled || err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("bare `terva workflow` should print usage, got handled=%v err=%v", handled, err)
	}
	handled, err = runWorkflowCommand([]string{"workflow", "run"}, "test")
	if !handled || err == nil || !strings.Contains(err.Error(), "exactly one script") {
		t.Fatalf("run without a script should error cleanly, got handled=%v err=%v", handled, err)
	}
	handled, err = runWorkflowCommand([]string{"workflow", "run", "/nonexistent/x.js"}, "test")
	if !handled || err == nil {
		t.Fatalf("missing script file should error, got handled=%v err=%v", handled, err)
	}
}
