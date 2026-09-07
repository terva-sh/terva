package agent

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"terva.sh/terva/packages/testsupport"
)

// The delegation contract of slice 1 (docs/plans/git-ticket.md): terva
// runs git-ticket's embedded command surface and returns its documented
// exit statuses. The store rides --store here so the test does not
// depend on Git-root discovery from a temp directory.
func TestTicketDelegationExitStatuses(t *testing.T) {
	tmp := testsupport.TempDir(t)
	store := filepath.Join(tmp, ".tickets")
	var out, errB bytes.Buffer

	if code := runTicketIn(tmp, []string{"--store", store, "init"}, &out, &errB); code != 0 {
		t.Fatalf("init: exit %d, stderr %q", code, errB.String())
	}
	out.Reset()
	errB.Reset()

	if code := runTicketIn(tmp, []string{"--store", store, "list"}, &out, &errB); code != 0 {
		t.Fatalf("list: exit %d, stderr %q", code, errB.String())
	}
	out.Reset()
	errB.Reset()

	code := runTicketIn(tmp, []string{"--store", store, "show", "TKT-NOPE"}, &out, &errB)
	if code == 0 {
		t.Fatal("show of a missing ticket exited 0; git-ticket's nonzero status was lost")
	}
	if !strings.Contains(errB.String(), "ticket_not_found") {
		t.Errorf("stderr lacks git-ticket's own error code: %q", errB.String())
	}
}

// Only the exact `ticket` token dispatches. The plural was superseded by
// git-ticket's plan section 2, and an argv that starts with anything
// else must fall through to terva's normal parser.
func TestTicketCommandDispatch(t *testing.T) {
	if handled, _ := runTicketCommand(nil); handled {
		t.Fatal("empty argv dispatched")
	}
	if handled, _ := runTicketCommand([]string{"tickets"}); handled {
		t.Fatal("the superseded plural dispatched")
	}
	if handled, _ := runTicketCommand([]string{"--help"}); handled {
		t.Fatal("a flag dispatched")
	}
}

func TestExitCodeErrorMessage(t *testing.T) {
	if got := (ExitCodeError{Code: 3}).Error(); got != "exit status 3" {
		t.Fatalf("ExitCodeError message: %q", got)
	}
}
