package build

// The harness-level ticket tests (TKT-01M1WDRS7K): confirm an agent in the
// terva harness works tickets end to end through the ticket_* tools. This
// exercises terva's wiring — the registry, the lazy group, the agent loop's
// dispatch, and the injected actor — and not git-ticket's semantics, which
// the library's own suite covers.

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	ticket "github.com/terva-sh/git-ticket/ticket"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/testsupport"
)

// ticketScriptClient is a scripted provider: each queued tool call rides out
// on the next request, and the request after that ends the turn. It records
// the advertised tools, the capability note, and the messages of every
// request, which is how a test sees what the model would see.
type ticketScriptClient struct {
	mu        sync.Mutex
	tools     [][]provider.Tool
	ephemeral []string
	messages  []string
	nextName  string
	nextArgs  string
	pending   bool
}

func (c *ticketScriptClient) Name() string { return "ticket-script" }

func (c *ticketScriptClient) queue(name, args string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextName, c.nextArgs, c.pending = name, args, true
}

func (c *ticketScriptClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.tools = append(c.tools, req.Tools)
	c.ephemeral = append(c.ephemeral, req.EphemeralContext)
	c.messages = append(c.messages, fmt.Sprintf("%+v", req.Messages))
	fire := c.pending
	name, args := c.nextName, c.nextArgs
	c.pending = false
	c.mu.Unlock()

	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "ticket-script", Model: req.Model}
		if fire {
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: "t1", Name: name, Arguments: json.RawMessage(args)}},
			}}
			return
		}
		out <- provider.EventDone{Stop: provider.StopEnd, Message: provider.Message{
			Role:    provider.RoleAssistant,
			Content: []provider.Content{provider.TextBlock{Text: "done"}},
		}}
	}()
	return out, nil
}

func (c *ticketScriptClient) lastTools() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := map[string]bool{}
	if len(c.tools) == 0 {
		return m
	}
	for _, spec := range c.tools[len(c.tools)-1] {
		m[spec.Name] = true
	}
	return m
}

func (c *ticketScriptClient) firstTools() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := map[string]bool{}
	if len(c.tools) == 0 {
		return m
	}
	for _, spec := range c.tools[0] {
		m[spec.Name] = true
	}
	return m
}

// The lazy group: a session in a store-bearing repository hides the ticket
// tools behind the `ticket` group, names them in the capability note, and
// activation brings all ten into the advertised set on the next turn. This
// is the path activate_tools drives through Agent.ActivateGroup.
func TestTicketToolsLazyGroupAdvertisesAndActivates(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, ticketStoreDir(t), nil, "", "", false, nil)
	client := &ticketScriptClient{}
	a := core.NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	adv := client.firstTools()
	if !adv["read"] {
		t.Error("core tools must stay advertised under lazy mode")
	}
	for _, name := range append(append([]string{}, ticketToolNames...), ticketWriteToolNames...) {
		if adv[name] {
			t.Errorf("%s advertised before activation", name)
		}
	}
	note := client.ephemeral[0]
	for _, want := range []string{"[inactive tool groups]", "ticket", "ticket_list"} {
		if !strings.Contains(note, want) {
			t.Errorf("capability note missing %q; note = %q", want, note)
		}
	}

	a.ActivateGroup("ticket")
	if err := a.Prompt(context.Background(), "again", nil, nil); err != nil {
		t.Fatalf("Prompt after activation: %v", err)
	}
	adv = client.lastTools()
	for _, name := range append(append([]string{}, ticketToolNames...), ticketWriteToolNames...) {
		if !adv[name] {
			t.Errorf("%s not advertised after activation", name)
		}
	}
}

// Plan mode through the loop: the session advertises the read five and none
// of the write five, which is the same split the registry test asserts, now
// proven at the surface the model actually sees.
func TestTicketPlanSessionAdvertisesReadsOnly(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	reg := BuildToolRegistry(Args{}, core.ApprovalPlan, ticketStoreDir(t), nil, "", "", false, nil)
	client := &ticketScriptClient{}
	a := core.NewAgent(client, "m", "sys", reg)

	if err := a.Prompt(context.Background(), "plan", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	adv := client.firstTools()
	for _, name := range ticketToolNames {
		if !adv[name] {
			t.Errorf("%s missing from a plan session", name)
		}
	}
	for _, name := range ticketWriteToolNames {
		if adv[name] {
			t.Errorf("%s advertised in a plan session", name)
		}
	}
}

// The lifecycle through the agent loop: list, get, claim, transition,
// comment, release, each dispatched by the loop from a scripted tool call,
// with the effects asserted on the store and the actor recorded as
// agent:terva/<persona>. The test injects fresh revisions between steps,
// because revision chaining is model behavior and the tools' own tests
// already cover the precondition.
func TestTicketLifecycleThroughAgentLoop(t *testing.T) {
	t.Setenv("TERVA_HOME", testsupport.TempDir(t))
	t.Setenv("TERVA_PERSONA_NAME", "Mieli")
	dir := ticketStoreDir(t)

	// Seed one claimable ticket. The seed writes carry an explicit actor
	// (a store refuses writes with none), and the ticket moves to ready,
	// because a draft cannot be claimed.
	ctx := context.Background()
	seedActor := ticket.Actor{ID: "human:seed", Name: "Seed"}
	s, err := ticket.Open(filepath.Join(dir, ticket.StoreDirName))
	if err != nil {
		t.Fatal(err)
	}
	made, err := s.Create(ctx, ticket.CreateOptions{Title: "Harness lifecycle", Type: "task", Priority: "normal", Actor: seedActor})
	if err != nil {
		t.Fatal(err)
	}
	id := made.Ticket.ID
	if _, err := s.Apply(ctx, id, ticket.SetStatus{Status: "ready"}, ticket.ApplyOptions{Actor: seedActor}); err != nil {
		t.Fatal(err)
	}

	reg := BuildToolRegistry(Args{}, core.ApprovalWorkspace, dir, nil, "", "", false, nil)
	client := &ticketScriptClient{}
	a := core.NewAgent(client, "m", "sys", reg)

	step := func(name string, args map[string]any) {
		t.Helper()
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		client.queue(name, string(raw))
		if err := a.Prompt(ctx, "step "+name, nil, nil); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	fresh := func() *ticket.Ticket {
		t.Helper()
		st, err := ticket.Discover(dir)
		if err != nil {
			t.Fatal(err)
		}
		tk, err := st.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return tk
	}

	step("ticket_list", map[string]any{})
	if last := client.messages[len(client.messages)-1]; !strings.Contains(last, id) {
		t.Errorf("ticket_list result never reached the conversation; last request lacks %s", id)
	}
	step("ticket_get", map[string]any{"ref": id})
	if last := client.messages[len(client.messages)-1]; !strings.Contains(last, fresh().Revision) {
		t.Error("ticket_get result should carry the revision into the conversation")
	}

	step("ticket_claim", map[string]any{"ref": id, "if_revision": fresh().Revision, "branch": "feat/harness"})
	tk := fresh()
	if tk.Claim == nil || tk.Claim.Actor != "agent:terva/mieli" {
		t.Fatalf("claim actor = %+v, want agent:terva/mieli", tk.Claim)
	}

	step("ticket_transition", map[string]any{"ref": id, "if_revision": fresh().Revision, "status": "in-progress"})
	if got := fresh().Status; got != "in-progress" {
		t.Fatalf("status = %q after transition", got)
	}

	step("ticket_comment", map[string]any{"ref": id, "if_revision": fresh().Revision, "text": "harness work record", "kind": "note"})
	if notes := fresh().Body.Notes; !strings.Contains(notes, "harness work record") || !strings.Contains(notes, "agent:terva/mieli") {
		t.Errorf("note text or actor missing from Notes: %q", notes)
	}

	step("ticket_claim", map[string]any{"ref": id, "if_revision": fresh().Revision, "release": true})
	if tk := fresh(); tk.Claim != nil {
		t.Errorf("release left a claim: %+v", tk.Claim)
	}

	step("ticket_transition", map[string]any{"ref": id, "if_revision": fresh().Revision, "status": "done"})
	final := fresh()
	if final.Status != "done" {
		t.Fatalf("final status = %q", final.Status)
	}
	if final.UpdatedBy == nil || final.UpdatedBy.ID != "agent:terva/mieli" {
		t.Errorf("updated_by = %+v, want the persona actor", final.UpdatedBy)
	}
}
