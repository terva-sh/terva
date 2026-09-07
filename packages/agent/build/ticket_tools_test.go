package build

import (
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// ticketStoreDir makes a directory governed by a real .tickets store — the
// registration gate probes ticket.Discover for real, so the test does too.
func ticketStoreDir(t *testing.T) string {
	t.Helper()
	dir := testsupport.TempDir(t)
	// Init takes the repository root and creates root/.tickets itself.
	if _, err := ticket.Init(dir, ticket.InitOptions{}); err != nil {
		t.Fatal(err)
	}
	return dir
}

var ticketToolNames = []string{"ticket_list", "ticket_search", "ticket_get", "ticket_ready", "ticket_check"}
var ticketWriteToolNames = []string{"ticket_create", "ticket_update", "ticket_transition", "ticket_claim", "ticket_comment"}

// The ticket tools register exactly where they can answer: present where a
// .tickets store governs the cwd, absent in a plain directory. A repository
// with no store pays no schema cost (docs/plans/git-ticket.md, slice 2).
func TestTicketToolsRegisterOnlyWithStore(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, ticketStoreDir(t), nil, "", "", false, nil)
	for _, name := range append(append([]string{}, ticketToolNames...), ticketWriteToolNames...) {
		if _, ok := reg[name]; !ok {
			t.Errorf("%s missing from a session with a ticket store", name)
		}
	}

	plain := BuildToolRegistry(Args{}, core.ApprovalWorkspace, testsupport.TempDir(t), nil, "", "", false, nil)
	for _, name := range append(append([]string{}, ticketToolNames...), ticketWriteToolNames...) {
		if _, ok := plain[name]; ok {
			t.Errorf("%s registered without any ticket store", name)
		}
	}
}

// Plan mode promises read-only. The five read tools must survive the prune
// — a plan that cannot consult the work ledger would plan blind — and the
// five write tools must not even be visible, the same split the worktree
// family asserts. A ticket store lives in the user's repository and lands
// in their next commit, so a plan-mode ticket_create would be a plan-mode
// write.
func TestTicketToolsPlanModeKeepsOnlyReads(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	plan := BuildToolRegistry(Args{}, core.ApprovalPlan, ticketStoreDir(t), nil, "", "", false, nil)
	for _, name := range ticketToolNames {
		if _, ok := plan[name]; !ok {
			t.Errorf("%s should survive plan mode (read-only)", name)
		}
	}
	for _, name := range ticketWriteToolNames {
		if _, ok := plan[name]; ok {
			t.Errorf("%s must be pruned in plan mode", name)
		}
	}
}
