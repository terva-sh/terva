package build

import (
	"context"
	"testing"

	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/testsupport"
)

// ticketAsker is a stand-in for the front end's question channel. The build
// package's other asker stub lives in the external test package, which this
// file cannot reach.
type ticketAsker struct{}

func (ticketAsker) Ask(context.Context, []core.UserQuestion) ([]core.UserAnswer, error) {
	return []core.UserAnswer{{Answer: "none"}}, nil
}

// ticket_init and the eleven are two halves of one gate, and no session holds
// both. A repository with a store has nothing to create, and a repository
// without one has nothing to work. Registering both would advertise a tool that
// answers "there is already a store" to every call it can ever receive.
func TestTicketInitRegistersOnlyWithoutStore(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))

	plain := BuildToolRegistry(Args{}, core.ApprovalWorkspace, testsupport.TempDir(t), nil, "", "", false, nil)
	if _, ok := plain["ticket_init"]; !ok {
		t.Error("ticket_init missing from a directory with no store, which is the only place it can act")
	}
	for _, name := range allTicketTools() {
		if _, ok := plain[name]; ok {
			t.Errorf("%s registered without a store, alongside ticket_init", name)
		}
	}

	stored := BuildToolRegistry(Args{}, core.ApprovalWorkspace, ticketStoreDir(t), nil, "", "", false, nil)
	if _, ok := stored["ticket_init"]; ok {
		t.Error("ticket_init registered where a store already governs the cwd")
	}
	if _, ok := stored["ticket_get"]; !ok {
		t.Fatal("no ticket tools in the store directory either: the gate is not what this test is measuring")
	}
}

// Creating a store writes .tickets/, .gitattributes and AGENTS.md into the
// user's repository, and all three land in their next commit. So it is pruned
// in plan mode with the six ticket write tools, and for the same reason.
func TestTicketInitPrunedInPlanMode(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	dir := testsupport.TempDir(t)

	if _, ok := BuildToolRegistry(Args{}, core.ApprovalWorkspace, dir, nil, "", "", false, nil)["ticket_init"]; !ok {
		t.Fatal("ticket_init absent in workspace mode: the prune is not what this test is measuring")
	}
	if _, ok := BuildToolRegistry(Args{}, core.ApprovalPlan, dir, nil, "", "", false, nil)["ticket_init"]; ok {
		t.Error("ticket_init survived plan mode, which promises the repository is not written to")
	}
}

// A user who turned the ticket tools off did not ask to be offered a ledger.
// Both opt-outs reach ticket_init, or the tool becomes the way around them.
func TestTicketInitRespectsTheOptOuts(t *testing.T) {
	t.Run("--no-ticket", func(t *testing.T) {
		t.Setenv("TERVA_HOME", testsupport.TempDir(t))
		reg := BuildToolRegistry(Args{NoTicket: true}, core.ApprovalWorkspace, testsupport.TempDir(t), nil, "", "", false, nil)
		if _, ok := reg["ticket_init"]; ok {
			t.Error("ticket_init registered under --no-ticket")
		}
	})
	t.Run("tickets:false on the user layer", func(t *testing.T) {
		home := testsupport.TempDir(t)
		t.Setenv("TERVA_HOME", home)
		writeUserConfig(t, home, config.Config{Tickets: ticketsOff()})
		reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, testsupport.TempDir(t), nil, "", "", false, nil)
		if _, ok := reg["ticket_init"]; ok {
			t.Error("ticket_init registered though the user turned the ticket tools off")
		}
	})
	t.Run("tickets:false on the project layer", func(t *testing.T) {
		t.Setenv("TERVA_HOME", testsupport.TempDir(t))
		dir := testsupport.TempDir(t)
		writeProjectConfig(t, dir, `{"tickets":false}`)
		reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, dir, nil, "", "", false, nil)
		if _, ok := reg["ticket_init"]; ok {
			t.Error("ticket_init registered though the project refused the ticket tools")
		}
	})
}

// The actor question needs the same channel ask_user_question uses, and it has
// the same hazard: a rebuild mints a fresh tool with a nil Asker. bindAsker is
// the single site both go through, so a rebuild path cannot bind one and miss
// the other.
func TestTicketInitTakesTheAskChannel(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("OPENAI_API_KEY", "test-key")

	r, err := Resolve(Args{Provider: "openai", Model: "gpt-5", CWD: testsupport.TempDir(t)}, false)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	r.SetAsker(ticketAsker{})
	ti, ok := r.ToolRegistry["ticket_init"].(*tools.TicketInitTool)
	if !ok {
		t.Fatal("ticket_init is not registered in a store-less workspace")
	}
	if ti.Asker == nil {
		t.Error("SetAsker left ticket_init with no channel, so its actor question would never reach the user")
	}
}
