package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/testsupport"
)

// spawnFixture wires a spawn tool over a no-op runner and a real ticket store,
// so a test exercises the whole Execute path without starting a process.
func spawnFixture(t *testing.T) (*SwarmSpawnTool, *swarm.Swarm, *TicketCore) {
	t.Helper()
	root := testsupport.TempDir(t)
	sw := swarm.New(swarm.Config{
		Root:     root,
		RepoRoot: root,
		NewRunner: func(a *swarm.Agent) swarm.Runner {
			return swarm.RunnerFunc(func(ctx context.Context, sink swarm.Sink) error { return nil })
		},
	})
	t.Cleanup(sw.StopAll)
	tc := ticketCore(t)
	return &SwarmSpawnTool{Swarm: sw, Enabled: func() bool { return true }, Tickets: tc}, sw, tc
}

func spawnWith(t *testing.T, tool *SwarmSpawnTool, args map[string]any) (text string, isErr bool) {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := tool.Execute(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return ticketResultText(t, res), res.IsError
}

// draftTicket makes a ticket and leaves it in draft, which is where every new
// ticket lands.
func draftTicket(t *testing.T, tc *TicketCore, title string) string {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"title": title, "acceptance_criteria": []string{"do the thing"}})
	res, err := (&TicketCreateTool{TicketCore: tc}).Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	var made ticketWriteOut
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &made); err != nil {
		t.Fatal(err)
	}
	return made.ID
}

func readTicket(t *testing.T, tc *TicketCore, id string) *ticket.Ticket {
	t.Helper()
	s, err := tc.open()
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// A draft is not assignable, and the refusal has to say who can change that.
// Promotion is a person's decision in this repository, so the model must not go
// looking for a way around it.
func TestSpawnRefusesADraftTicket(t *testing.T) {
	tool, sw, tc := spawnFixture(t)
	id := draftTicket(t, tc, "Not promoted yet")

	text, isErr := spawnWith(t, tool, map[string]any{"task": "work it", "ticket": id})
	if !isErr {
		t.Fatalf("a draft ticket spawned anyway: %s", text)
	}
	if !strings.Contains(text, "still a draft") || !strings.Contains(text, "person promotes") {
		t.Errorf("the refusal does not name who can promote: %s", text)
	}
	if n := len(sw.List()); n != 0 {
		t.Errorf("%d sub-agent(s) started on work they cannot hold", n)
	}
}

// A ticket somebody else holds is not free to reassign, and the refusal names
// the holder so the dispatcher can decide whether to wait or take it.
func TestSpawnRefusesATicketAlreadyClaimed(t *testing.T) {
	tool, sw, tc := spawnFixture(t)
	id, rev := readyTicket(t, tc, []string{"do the thing"})
	if _, err := (&TicketClaimTool{TicketCore: tc}).Execute(context.Background(),
		mustJSONArgs(map[string]any{"ref": id, "if_revision": rev}), nil); err != nil {
		t.Fatal(err)
	}

	text, isErr := spawnWith(t, tool, map[string]any{"task": "work it", "ticket": id})
	if !isErr {
		t.Fatalf("a claimed ticket was reassigned: %s", text)
	}
	if !strings.Contains(text, "already claimed by") {
		t.Errorf("the refusal does not name the holder: %s", text)
	}
	if n := len(sw.List()); n != 0 {
		t.Errorf("%d sub-agent(s) started on a ticket another actor holds", n)
	}
}

// A ticket whose dependency is unfinished is not startable, and the refusal
// points at the work that actually comes first.
func TestSpawnRefusesOnAnUnmetDependency(t *testing.T) {
	tool, sw, tc := spawnFixture(t)
	blocker := draftTicket(t, tc, "Comes first")

	args, _ := json.Marshal(map[string]any{
		"title":               "Comes second",
		"acceptance_criteria": []string{"do the thing"},
		"dependencies":        []string{blocker},
	})
	res, err := (&TicketCreateTool{TicketCore: tc}).Execute(context.Background(), args, nil)
	if err != nil {
		t.Fatal(err)
	}
	var made ticketWriteOut
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &made); err != nil {
		t.Fatal(err)
	}
	if _, err := (&TicketTransitionTool{TicketCore: tc}).Execute(context.Background(),
		mustJSONArgs(map[string]any{"ref": made.ID, "if_revision": made.Revision, "status": "ready"}), nil); err != nil {
		t.Fatal(err)
	}

	text, isErr := spawnWith(t, tool, map[string]any{"task": "work it", "ticket": made.ID})
	if !isErr {
		t.Fatalf("a blocked ticket spawned anyway: %s", text)
	}
	if !strings.Contains(text, blocker) {
		t.Errorf("the refusal does not name the blocking ticket: %s", text)
	}
	if n := len(sw.List()); n != 0 {
		t.Errorf("%d sub-agent(s) started on blocked work", n)
	}
}

// A session with no ticket store cannot assign one. Refusing beats spawning an
// unassigned sub-agent that the dispatcher believes is holding a ticket.
func TestSpawnWithATicketButNoStoreRefuses(t *testing.T) {
	tool, sw, _ := spawnFixture(t)
	tool.Tickets = nil

	text, isErr := spawnWith(t, tool, map[string]any{"task": "work it", "ticket": "TKT-WHATEVER"})
	if !isErr {
		t.Fatalf("a ticket spawn succeeded with no store: %s", text)
	}
	if !strings.Contains(text, "no ticket store") {
		t.Errorf("the refusal does not name the cause: %s", text)
	}
	if n := len(sw.List()); n != 0 {
		t.Errorf("%d sub-agent(s) started", n)
	}
}

// The claim names the SUB-AGENT, not the host. That is the whole point: the
// child signs its own notes with the same id, because build.ticketActor reads
// it out of the child environment.
func TestSpawnClaimsTheTicketForTheSubagent(t *testing.T) {
	tool, sw, tc := spawnFixture(t)
	id, _ := readyTicket(t, tc, []string{"do the thing"})

	text, isErr := spawnWith(t, tool, map[string]any{"task": "work it", "ticket": id})
	if isErr {
		t.Fatalf("spawn refused: %s", text)
	}
	agents := sw.List()
	if len(agents) != 1 {
		t.Fatalf("want 1 sub-agent, got %d", len(agents))
	}
	agentID := agents[0].ID

	tk := readTicket(t, tc, id)
	if tk.Claim == nil {
		t.Fatal("the ticket carries no claim after the spawn")
	}
	if want := "agent:terva/" + agentID; tk.Claim.Actor != want {
		t.Errorf("claim actor = %q, want %q", tk.Claim.Actor, want)
	}
	if tk.Claim.Session == nil || *tk.Claim.Session != agentID {
		t.Errorf("claim session = %v, want the sub-agent id %q", tk.Claim.Session, agentID)
	}
	// A claim made on behalf of a process must lapse, or a dead sub-agent holds
	// the ticket forever.
	if tk.Claim.ExpiresAt == nil {
		t.Error("the claim has no expiry, so a dead sub-agent would hold it forever")
	}
	if !strings.Contains(text, id) {
		t.Errorf("the spawn output does not name the ticket it claimed: %s", text)
	}
}

// The ticket is the briefing. A sub-agent starts with no conversation context,
// so the criteria and the closure rule have to travel in the task text.
func TestSpawnRendersTheTicketIntoTheTask(t *testing.T) {
	tool, sw, tc := spawnFixture(t)
	id, _ := readyTicket(t, tc, []string{"first criterion", "second criterion"})

	if _, isErr := spawnWith(t, tool, map[string]any{"task": "work it", "ticket": id}); isErr {
		t.Fatal("spawn refused")
	}
	agents := sw.List()
	if len(agents) != 1 {
		t.Fatalf("want 1 sub-agent, got %d", len(agents))
	}
	task := agents[0].Task

	if !strings.Contains(task, "work it") {
		t.Error("the original task text was lost")
	}
	for _, want := range []string{id, "first criterion", "second criterion"} {
		if !strings.Contains(task, want) {
			t.Errorf("the brief is missing %q:\n%s", want, task)
		}
	}
	if !strings.Contains(task, "Do not close it") {
		t.Errorf("the brief does not forbid closure:\n%s", task)
	}
}

// A spawn with no ticket is the overwhelming default and must stay untouched.
func TestSpawnWithoutATicketClaimsNothing(t *testing.T) {
	tool, sw, tc := spawnFixture(t)
	id, _ := readyTicket(t, tc, []string{"do the thing"})

	if _, isErr := spawnWith(t, tool, map[string]any{"task": "unrelated work"}); isErr {
		t.Fatal("a plain spawn was refused")
	}
	if n := len(sw.List()); n != 1 {
		t.Fatalf("want 1 sub-agent, got %d", n)
	}
	if tk := readTicket(t, tc, id); tk.Claim != nil {
		t.Errorf("a spawn that named no ticket claimed %s anyway", id)
	}
}

func mustJSONArgs(m map[string]any) json.RawMessage {
	raw, _ := json.Marshal(m)
	return raw
}

// moveTicketStatus transitions a ticket and returns the revision the write
// produced, so a caller can chain another write behind it.
func moveTicketStatus(t *testing.T, tc *TicketCore, id, rev, status string, extra ...map[string]any) string {
	t.Helper()
	args := map[string]any{"ref": id, "if_revision": rev, "status": status}
	for _, m := range extra {
		for k, v := range m {
			args[k] = v
		}
	}
	res, err := (&TicketTransitionTool{TicketCore: tc}).Execute(context.Background(), mustJSONArgs(args), nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("transition to %s refused: %s", status, ticketResultText(t, res))
	}
	var out ticketWriteOut
	if err := json.Unmarshal([]byte(ticketResultText(t, res)), &out); err != nil {
		t.Fatal(err)
	}
	return out.Revision
}

// The acceptance criteria become the report contract, so the dispatcher reviews
// a structured answer for each criterion instead of reading prose.
func TestCriteriaBecomeTheReportContract(t *testing.T) {
	tc := ticketCore(t)
	id, _ := readyTicket(t, tc, []string{"first criterion", "second criterion"})

	raw := criteriaSchema(readTicket(t, tc, id))
	if len(raw) == 0 {
		t.Fatal("a ticket with criteria derived no contract")
	}
	var got struct {
		Type       string   `json:"type"`
		Required   []string `json:"required"`
		Properties struct {
			Criteria struct {
				Type        string `json:"type"`
				Description string `json:"description"`
				Items       struct {
					Required   []string       `json:"required"`
					Properties map[string]any `json:"properties"`
				} `json:"items"`
			} `json:"criteria"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the derived contract is not valid JSON: %v", err)
	}
	// A provider tool schema has to be an object at the top level, so the array
	// hangs off a single property rather than being the schema itself.
	if got.Type != "object" {
		t.Errorf("top-level type = %q, want object", got.Type)
	}
	if len(got.Required) != 1 || got.Required[0] != "criteria" {
		t.Errorf("required = %v, want [criteria]", got.Required)
	}
	if got.Properties.Criteria.Type != "array" {
		t.Errorf("criteria type = %q, want array", got.Properties.Criteria.Type)
	}
	// A criterion the child skipped has to come back missing rather than merely
	// unmentioned, so every field is required.
	for _, want := range []string{"criterion", "met", "evidence"} {
		if _, ok := got.Properties.Criteria.Items.Properties[want]; !ok {
			t.Errorf("a report entry cannot carry %q", want)
		}
		if !slicesContain(got.Properties.Criteria.Items.Required, want) {
			t.Errorf("%q is optional, so a child may omit it", want)
		}
	}
	// The criteria are numbered in the order they went out, so the answers come
	// back in the order the dispatcher reads them.
	desc := got.Properties.Criteria.Description
	for _, want := range []string{id, "1. first criterion", "2. second criterion"} {
		if !strings.Contains(desc, want) {
			t.Errorf("the contract description is missing %q:\n%s", want, desc)
		}
	}
}

// A caller who wrote a deliverable_schema was specific about the report it
// wants. The derivation is the convenience for the caller who wrote none.
func TestAnExplicitSchemaWinsOverTheCriteria(t *testing.T) {
	tc := ticketCore(t)
	id, _ := readyTicket(t, tc, []string{"first criterion"})

	explicit := json.RawMessage(`{"type":"object","properties":{"verdict":{"type":"string"}}}`)
	got := spawnSchema(explicit, readTicket(t, tc, id))
	if string(got) != string(explicit) {
		t.Errorf("the ticket derivation overrode an explicit schema:\n%s", got)
	}
}

// A ticket with no acceptance criteria has no contract to derive, and the
// sub-agent then reports in prose as an unassigned one does.
func TestATicketWithNoCriteriaDerivesNoContract(t *testing.T) {
	tc := ticketCore(t)
	id, _ := readyTicket(t, tc, nil)
	tk := readTicket(t, tc, id)

	if raw := criteriaSchema(tk); len(raw) != 0 {
		t.Errorf("a ticket with no criteria derived a contract: %s", raw)
	}
	if raw := spawnSchema(nil, tk); len(raw) != 0 {
		t.Errorf("spawnSchema invented a contract: %s", raw)
	}
}

// The review gate needs the recap to name the ticket the child was working. The
// recap prints a truncated task line, so the id has to lead the task text: a
// brief appended at the end falls outside that window.
func TestTheTaskTextLeadsWithTheTicketID(t *testing.T) {
	tool, sw, tc := spawnFixture(t)
	id, _ := readyTicket(t, tc, []string{"do the thing"})

	if _, isErr := spawnWith(t, tool, map[string]any{"task": "work it", "ticket": id}); isErr {
		t.Fatal("spawn refused")
	}
	agents := sw.List()
	if len(agents) != 1 {
		t.Fatalf("want 1 sub-agent, got %d", len(agents))
	}
	if want := "Ticket " + id + ". "; !strings.HasPrefix(agents[0].Task, want) {
		t.Errorf("the task does not lead with the ticket, so the recap cannot name it:\n%s", agents[0].Task)
	}
}

// A sub-agent works a ticket and never closes one. Whether the work is finished
// is a judgement about the report, and it belongs to the dispatcher that reads
// that report against the criteria. No actor approves its own output.
func TestASubagentCannotCloseATicket(t *testing.T) {
	for _, status := range []string{"done", "archived"} {
		t.Run(status, func(t *testing.T) {
			tc := ticketCore(t)
			id, rev := readyTicket(t, tc, []string{"do the thing"})
			rev = moveTicketStatus(t, tc, id, rev, "in-progress")

			tc.SubagentID = "swarm-child-1"
			args := mustJSONArgs(map[string]any{"ref": id, "if_revision": rev, "status": status})
			res, err := (&TicketTransitionTool{TicketCore: tc}).Execute(context.Background(), args, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !res.IsError {
				t.Fatalf("a sub-agent moved %s to %s", id, status)
			}
			text := ticketResultText(t, res)
			if !strings.Contains(text, "does not close a ticket") {
				t.Errorf("the refusal does not name the reason: %s", text)
			}
			// A refusal that names no next action leaves the child to retry or to
			// invent a way around the gate.
			if !strings.Contains(text, "ticket_comment") {
				t.Errorf("the refusal does not name the next action: %s", text)
			}
			// The guard runs before the mutation, so a refused close writes nothing.
			if got := readTicket(t, tc, id).Status; got != "in-progress" {
				t.Errorf("the refused %s move still wrote: status = %q", status, got)
			}
		})
	}
}

// The gate is closure, not every write. A sub-agent may park a ticket it cannot
// finish, and blocked is how it says so.
func TestASubagentMayBlockATicket(t *testing.T) {
	tc := ticketCore(t)
	id, rev := readyTicket(t, tc, []string{"do the thing"})
	rev = moveTicketStatus(t, tc, id, rev, "in-progress")

	tc.SubagentID = "swarm-child-1"
	moveTicketStatus(t, tc, id, rev, "blocked", map[string]any{"reason": "the upstream service is down"})
	if got := readTicket(t, tc, id).Status; got != "blocked" {
		t.Errorf("status = %q, want blocked", got)
	}
}

// The gate is scoped to a sub-agent. Refusing the child is only worth anything
// because the dispatcher still closes the ticket.
func TestTheDispatcherStillClosesATicket(t *testing.T) {
	tc := ticketCore(t)
	id, rev := readyTicket(t, tc, []string{"do the thing"})
	rev = moveTicketStatus(t, tc, id, rev, "in-progress")

	if tc.SubagentID != "" {
		t.Fatal("the dispatcher fixture is already a sub-agent")
	}
	moveTicketStatus(t, tc, id, rev, "done")
	if got := readTicket(t, tc, id).Status; got != "done" {
		t.Errorf("status = %q, want done", got)
	}
}

func slicesContain(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}
