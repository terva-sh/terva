package build

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// Outside a swarm the actor is the persona, slugged. This is the behaviour
// every ticket write had before subagents could hold a claim.
func TestTicketActorUsesThePersonaOutsideASwarm(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_PERSONA_NAME", "Mieli")
	t.Setenv("TERVA_SWARM_AGENT_ID", "")

	id, name := ticketActor()
	if id != "agent:terva/mieli" {
		t.Errorf("actor id = %q, want agent:terva/mieli", id)
	}
	if name != "Mieli" {
		t.Errorf("actor name = %q, want Mieli", name)
	}
}

// A persona with capitals and spaces still slugs into one actor token, or the
// id stops being a usable namespace.
func TestTicketActorSlugsAPersonaWithSpaces(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_PERSONA_NAME", "Code Reviewer")
	t.Setenv("TERVA_SWARM_AGENT_ID", "")

	id, _ := ticketActor()
	if id != "agent:terva/code-reviewer" {
		t.Errorf("actor id = %q, want agent:terva/code-reviewer", id)
	}
}

// Inside a swarm child the id names the subagent, so the claim the supervisor
// made on its behalf and the notes the child writes name one actor. The display
// name stays the persona: the id says which actor holds the claim, the name says
// who this is.
func TestTicketActorUsesTheSubagentIDInsideASwarm(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_PERSONA_NAME", "Mieli")
	t.Setenv("TERVA_SWARM_AGENT_ID", "fix-the-parser-481522")

	id, name := ticketActor()
	if id != "agent:terva/fix-the-parser-481522" {
		t.Errorf("actor id = %q, want agent:terva/fix-the-parser-481522", id)
	}
	if name != "Mieli" {
		t.Errorf("actor name = %q, want the persona Mieli", name)
	}
}

// The registered write tools have to carry that actor, not just the helper.
// A subagent whose tools still signed as the persona would leave the ticket
// disagreeing with itself about who did the work.
func TestTicketToolsCarryTheSubagentActor(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_PERSONA_NAME", "Mieli")
	t.Setenv("TERVA_SWARM_AGENT_ID", "sweep-the-logs-900001")
	dir := ticketStoreDir(t)

	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, dir, nil, "", "", false, nil)
	ct, ok := reg["ticket_claim"].(*tools.TicketClaimTool)
	if !ok {
		t.Fatalf("ticket_claim is %T, not *tools.TicketClaimTool", reg["ticket_claim"])
	}
	if ct.ActorID != "agent:terva/sweep-the-logs-900001" {
		t.Errorf("ticket_claim actor = %q, want the subagent id", ct.ActorID)
	}
}

// ticket_init is the deliberate exception. It seeds a NEW store's first actor,
// which is a durable identity for the repository, and a subagent id is minted
// per spawn and gone by the next one. A store signed by a dead subagent would
// name an actor that can never write again.
func TestTicketInitKeepsThePersonaActorInASwarmChild(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_PERSONA_NAME", "Mieli")
	t.Setenv("TERVA_SWARM_AGENT_ID", "sweep-the-logs-900001")
	// No store here, which is the inverse gate ticket_init registers on.
	dir := testsupport.TempDir(t)

	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, dir, nil, "", "", false, nil)
	ti, ok := reg["ticket_init"].(*tools.TicketInitTool)
	if !ok {
		t.Fatalf("ticket_init is %T, not *tools.TicketInitTool", reg["ticket_init"])
	}
	if ti.AgentActorID != "agent:terva/mieli" {
		t.Errorf("ticket_init actor = %q, want the persona actor", ti.AgentActorID)
	}
	if strings.Contains(ti.AgentActorID, "sweep-the-logs") {
		t.Error("ticket_init would sign a new store with a per-spawn subagent id")
	}
}

// ticketExec runs one registered ticket tool and returns its decoded JSON. It
// fails the test on a refusal, because every call here is a step toward the
// assertion rather than the thing under test.
func ticketExec(t *testing.T, reg core.Registry, name string, args map[string]any) map[string]any {
	t.Helper()
	tool, ok := reg[name]
	if !ok {
		t.Fatalf("%s is not registered", name)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := tool.Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var text string
	for _, c := range res.Content {
		if tb, ok := c.(provider.TextBlock); ok {
			text += tb.Text
		}
	}
	if res.IsError {
		t.Fatalf("%s refused: %s", name, text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("%s returned no JSON: %v (body %q)", name, err, text)
	}
	return out
}

func ticketStr(t *testing.T, m map[string]any, key string) string {
	t.Helper()
	v, _ := m[key].(string)
	if v == "" {
		t.Fatalf("result carries no %s: %v", key, m)
	}
	return v
}

// The acceptance criterion for tier 1's identity half, tested end to end: a
// write through terva records agent:terva/<persona> and the model passes no
// actor anywhere.
//
// The registry-level tests above prove the field is SET on the tool. This one
// performs the write and reads the store back, because a field that is set and
// then dropped on the way to disk looks identical until you check the file.
func TestATicketWriteRecordsThePersonaActorWithoutTheModelPassingOne(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_PERSONA_NAME", "Mieli")
	t.Setenv("TERVA_SWARM_AGENT_ID", "")
	dir := ticketStoreDir(t)
	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, dir, nil, "", "", false, nil)

	// Not one of these calls names an actor. That absence is the criterion.
	made := ticketExec(t, reg, "ticket_create", map[string]any{"title": "Prove the actor lands"})
	id := ticketStr(t, made, "id")
	ready := ticketExec(t, reg, "ticket_transition", map[string]any{
		"ref": id, "if_revision": ticketStr(t, made, "revision"), "status": "ready",
	})
	ticketExec(t, reg, "ticket_claim", map[string]any{
		"ref": id, "if_revision": ticketStr(t, ready, "revision"),
	})

	s, err := ticket.Discover(dir)
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Claim == nil {
		t.Fatal("the claim did not land, so there is no actor to check")
	}
	if tk.Claim.Actor != "agent:terva/mieli" {
		t.Errorf("claim actor = %q, want agent:terva/mieli", tk.Claim.Actor)
	}
	// The trap .tickets/CONVENTIONS.md documents: a write that names no actor
	// signs as the first actor in config.yml, which is a person.
	if !strings.HasPrefix(tk.Claim.Actor, "agent:") {
		t.Errorf("the write signed as %q, which is not an agent identity", tk.Claim.Actor)
	}
}
