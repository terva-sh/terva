package core

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"terva.sh/terva/packages/provider"
)

// extToolFake wraps a flagTool with an Extension() accessor, placing it in a
// capability group like a real extension / MCP tool.
type extToolFake struct {
	*flagTool
	group string
}

func (e *extToolFake) Extension() string { return e.group }

// groupActivatorTool is a core tool (no Extension()) that activates a group
// when called — a stand-in for the activate_tools tool, so a test can drive a
// mid-turn activation.
type groupActivatorTool struct{ group string }

func (g *groupActivatorTool) Name() string            { return "activate" }
func (g *groupActivatorTool) Description() string     { return "activates a group" }
func (g *groupActivatorTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (g *groupActivatorTool) Execute(ctx context.Context, _ json.RawMessage, _ func(string)) (ToolResult, error) {
	if a := AgentFromContext(ctx); a != nil {
		a.ActivateGroup(g.group)
	}
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "activated"}}}, nil
}

// reqCaptureClient records req.Tools and req.EphemeralContext for every call. If
// toolThen is set, its first call (every odd call with toolEveryOdd) drives one
// tool call before ending. onCall, when set, runs at the top of request n — a
// test hook for acting mid-"reply" (e.g. queueing a message while the model
// speaks).
type reqCaptureClient struct {
	mu           sync.Mutex
	tools        [][]provider.Tool
	ephemeral    []string
	toolThen     string
	toolEveryOdd bool
	onCall       func(n int)
	calls        int
}

func (c *reqCaptureClient) Name() string { return "reqcap" }

func (c *reqCaptureClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.tools = append(c.tools, req.Tools)
	c.ephemeral = append(c.ephemeral, req.EphemeralContext)
	hook := c.onCall
	c.mu.Unlock()
	if hook != nil {
		hook(n)
	}

	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "reqcap", Model: req.Model}
		if c.toolThen != "" && (n == 1 || (c.toolEveryOdd && n%2 == 1)) {
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: "t1", Name: c.toolThen, Arguments: json.RawMessage(`{}`)}},
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

func extTool(name, group string) *extToolFake {
	return &extToolFake{flagTool: &flagTool{name: name}, group: group}
}

// With lazy tools on and no always-active groups, only the core group is
// advertised; the inactive groups' tools are hidden and summarized in the
// cache-free capability note so the model can discover and activate them.
func TestLazyToolsAdvertisesCoreHidesInactiveWithNote(t *testing.T) {
	reg := Registry{
		"read":      &flagTool{name: "read"},
		"mail_send": extTool("mail_send", "mail"),
		"gh_pr":     extTool("gh_pr", "mcp:github"),
	}
	client := &reqCaptureClient{}
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	adv := specNames(client.tools[0])
	if !adv["read"] {
		t.Error("core tool must stay advertised under lazy mode")
	}
	if adv["mail_send"] || adv["gh_pr"] {
		t.Errorf("inactive groups must be hidden, advertised = %v", adv)
	}
	note := client.ephemeral[0]
	for _, want := range []string{"[inactive tool groups]", "mail", "mail_send", "mcp:github", "gh_pr", "activate_tools"} {
		if !strings.Contains(note, want) {
			t.Errorf("capability note missing %q; note = %q", want, note)
		}
	}
}

// ActivateGroup brings a group into the advertised set on the NEXT turn, and the
// capability note drops it.
func TestActivateGroupTakesEffectNextTurn(t *testing.T) {
	reg := Registry{
		"read":      &flagTool{name: "read"},
		"mail_send": extTool("mail_send", "mail"),
	}
	client := &reqCaptureClient{}
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt 1: %v", err)
	}
	if specNames(client.tools[0])["mail_send"] {
		t.Fatal("mail_send must be hidden before activation")
	}

	if !a.ActivateGroup("mail") {
		t.Error("ActivateGroup should report a change")
	}
	if a.ActivateGroup("mail") {
		t.Error("re-activating an active group should report no change")
	}

	if err := a.Prompt(context.Background(), "again", nil, nil); err != nil {
		t.Fatalf("Prompt 2: %v", err)
	}
	last := client.tools[len(client.tools)-1]
	if !specNames(last)["mail_send"] {
		t.Error("mail_send must be advertised after activation")
	}
	if strings.Contains(client.ephemeral[len(client.ephemeral)-1], "mail_send") {
		t.Error("capability note should no longer list an activated group")
	}
}

// The active-group set is pinned per segment: a mid-turn ActivateGroup (via a
// tool) does NOT change the current segment's advertised set — it lands on the
// next pin. This mirrors the Tools/System pin, so activation is one deliberate
// cache write at a boundary, never mid-turn churn. Activation continuation is
// switched off here to isolate the pin semantics; the default-on path (the
// continuation lands the tools within the same Prompt) has its own tests below.
func TestActivateGroupMidTurnLandsNextTurn(t *testing.T) {
	reg := Registry{
		"read":      &flagTool{name: "read"},
		"activate":  &groupActivatorTool{group: "mail"},
		"mail_send": extTool("mail_send", "mail"),
	}
	client := &reqCaptureClient{toolThen: "activate"}
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()
	a.SetActivationContinuation(false)

	// Turn 1 has two steps: step 1 calls "activate" (activating mail mid-turn),
	// step 2 continues. Both steps of turn 1 must still HIDE mail (pinned).
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt 1: %v", err)
	}
	if len(client.tools) < 2 {
		t.Fatalf("expected a 2-step turn, got %d requests", len(client.tools))
	}
	for i := range 2 {
		if specNames(client.tools[i])["mail_send"] {
			t.Errorf("turn-1 step %d advertised mail_send, but a mid-turn activation must not change the pinned set", i+1)
		}
	}

	// The next turn picks up the activation.
	if err := a.Prompt(context.Background(), "next", nil, nil); err != nil {
		t.Fatalf("Prompt 2: %v", err)
	}
	if !specNames(client.tools[len(client.tools)-1])["mail_send"] {
		t.Error("mail_send must be advertised on the turn after a mid-turn activation")
	}
}

// chainActivatorTool activates a fresh group (g1, g2, …) on every call — the
// pathological always-dirty driver the activation-gate cap test needs.
type chainActivatorTool struct{ n int }

func (c *chainActivatorTool) Name() string            { return "activate" }
func (c *chainActivatorTool) Description() string     { return "activates a fresh group each call" }
func (c *chainActivatorTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (c *chainActivatorTool) Execute(ctx context.Context, _ json.RawMessage, _ func(string)) (ToolResult, error) {
	c.n++
	if a := AgentFromContext(ctx); a != nil {
		a.ActivateGroup(fmt.Sprintf("g%d", c.n))
	}
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "activated"}}}, nil
}

// Activation continuation, default ON: a segment that activated a group ends
// naturally, the built-in gate re-prompts with a synthetic nudge naming the
// newly live tools, the pin refreshes, and the continuation segment advertises
// them — all within ONE Prompt (docs/proposals/activation-continuation.md,
// stage 2).
func TestActivationContinuationLandsSamePrompt(t *testing.T) {
	reg := Registry{
		"read":      &flagTool{name: "read"},
		"activate":  &groupActivatorTool{group: "mail"},
		"mail_send": extTool("mail_send", "mail"),
	}
	client := &reqCaptureClient{toolThen: "activate"}
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()

	var causes []string
	err := a.Prompt(context.Background(), "go", nil, func(ev AgentEvent) {
		if e, ok := ev.(EvContinuation); ok {
			causes = append(causes, e.Cause)
		}
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(client.tools) != 3 {
		t.Fatalf("want 3 requests (tool step, natural end, continuation), got %d", len(client.tools))
	}
	if specNames(client.tools[0])["mail_send"] || specNames(client.tools[1])["mail_send"] {
		t.Error("the activating segment must still hide mail_send (pinned)")
	}
	if !specNames(client.tools[2])["mail_send"] {
		t.Error("the continuation segment must advertise the newly activated tools")
	}
	if len(causes) != 1 || causes[0] != "activation" {
		t.Errorf("want one EvContinuation with cause activation, got %v", causes)
	}
	if strings.Contains(client.ephemeral[2], "mail_send") {
		t.Error("the continuation's capability note must no longer list the activated group")
	}
	var nudge string
	for _, m := range a.Messages() {
		if m.Role == provider.RoleUser && m.Meta[MetaSynthetic] == "true" {
			nudge = extractText(m)
		}
	}
	if !strings.Contains(nudge, "mail_send") {
		t.Errorf("the synthetic nudge should name the newly live tools, got %q", nudge)
	}
}

// With activation continuation switched off, the boundary contract reverts to
// the stage-0 pin reuse: a host gate's continuation still sees the old tool
// set, and a mid-segment activation lands only on the next Prompt.
func TestActivationContinuationOffReusesPinnedTools(t *testing.T) {
	reg := Registry{
		"read":      &flagTool{name: "read"},
		"activate":  &groupActivatorTool{group: "mail"},
		"mail_send": extTool("mail_send", "mail"),
	}
	client := &reqCaptureClient{toolThen: "activate"}
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()
	a.SetActivationContinuation(false)
	a.AddContinuationGate(ContinuationGate{Cause: "test", Fire: func(provider.StopReason) (string, bool) {
		return "keep going", true
	}})

	// One Prompt, three requests: step 1 activates mail mid-segment, step 2
	// ends naturally, the host gate re-prompts, step 3 ends. Every request
	// must still hide mail_send.
	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt 1: %v", err)
	}
	if len(client.tools) != 3 {
		t.Fatalf("expected 3 requests (tool step, natural end, gate re-prompt), got %d", len(client.tools))
	}
	for i, tools := range client.tools {
		if specNames(tools)["mail_send"] {
			t.Errorf("request %d advertised mail_send; with the feature off a continuation must reuse the pin", i+1)
		}
	}

	// The next Prompt re-pins and picks the activation up.
	if err := a.Prompt(context.Background(), "next", nil, nil); err != nil {
		t.Fatalf("Prompt 2: %v", err)
	}
	if !specNames(client.tools[len(client.tools)-1])["mail_send"] {
		t.Error("mail_send must be advertised on the Prompt after the gated one")
	}
}

// Real queued input outranks the synthetic nudge: when a message is queued by
// the time the activating segment ends, no nudge is injected — the queued
// message continues the Prompt — but the pin still refreshes, so the reply to
// that message already has the tools live.
func TestActivationContinuationQueuedInputWins(t *testing.T) {
	reg := Registry{
		"read":      &flagTool{name: "read"},
		"activate":  &groupActivatorTool{group: "mail"},
		"mail_send": extTool("mail_send", "mail"),
	}
	client := &reqCaptureClient{toolThen: "activate"}
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()
	client.onCall = func(n int) {
		if n == 2 { // queued while the model "speaks" its final message
			a.SetQueuedMessages([]string{"real follow-up"})
		}
	}

	var causes []string
	err := a.Prompt(context.Background(), "go", nil, func(ev AgentEvent) {
		if e, ok := ev.(EvContinuation); ok {
			causes = append(causes, e.Cause)
		}
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(client.tools) != 3 {
		t.Fatalf("want 3 requests (tool step, natural end, queued follow-up), got %d", len(client.tools))
	}
	if !specNames(client.tools[2])["mail_send"] {
		t.Error("the queued follow-up's segment must run with the newly activated tools live")
	}
	if len(causes) != 0 {
		t.Errorf("real input must continue the Prompt without a gate, got causes %v", causes)
	}
	for _, m := range a.Messages() {
		if m.Role == provider.RoleUser && m.Meta[MetaSynthetic] == "true" {
			t.Errorf("no synthetic nudge should be injected when real input waits, got %q", extractText(m))
		}
	}
}

// The activation gate is capped (defense in depth — activation is monotonic,
// so real chains end on their own): a pathological run that activates another
// group every segment stops being continued after activationContinuationCap
// fires.
func TestActivationContinuationCap(t *testing.T) {
	reg := Registry{
		"read":     &flagTool{name: "read"},
		"activate": &chainActivatorTool{},
	}
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("t%d", i)
		reg[name] = extTool(name, fmt.Sprintf("g%d", i))
	}
	client := &reqCaptureClient{toolThen: "activate", toolEveryOdd: true}
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()

	var causes []string
	err := a.Prompt(context.Background(), "go", nil, func(ev AgentEvent) {
		if e, ok := ev.(EvContinuation); ok {
			causes = append(causes, e.Cause)
		}
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(causes) != activationContinuationCap {
		t.Errorf("want exactly %d activation continuations, got %d", activationContinuationCap, len(causes))
	}
	// Each continuation adds one 2-step segment: initial segment (2 calls) +
	// cap × 2, then the capped boundary ends the Prompt.
	if want := 2 + 2*activationContinuationCap; len(client.tools) != want {
		t.Errorf("want %d requests, got %d", want, len(client.tools))
	}
}

// ActivateGroupsForTools (skill-driven activation, step 5) resolves tool NAMES
// to their groups and activates them — but strictly on the visibility axis: the
// revealed tool is advertised yet STILL gated when called. This is the §Security
// acceptance gate — activating via a skill grants no authority. It also skips
// names absent from the registry (an untrusted workspace never loaded them) and
// the always-on core group.
func TestActivateGroupsForToolsVisibilityOnly(t *testing.T) {
	danger := extTool("danger_tool", "danger")
	reg := Registry{
		"read":        &flagTool{name: "read"},
		"danger_tool": danger,
	}
	client := &reqCaptureClient{toolThen: "danger_tool"}
	a := NewAgent(client, "m", "s", reg)
	a.EnableLazyTools()

	gateFired := false
	a.BeforeToolExecute = func(call provider.ToolCallBlock) (bool, string, json.RawMessage) {
		if call.Name == "danger_tool" {
			gateFired = true
		}
		return true, "", nil
	}

	// read is core (skipped), "nope" is absent (skipped), danger_tool -> "danger".
	got := a.ActivateGroupsForTools([]string{"danger_tool", "read", "nope"})
	if len(got) != 1 || got[0] != "danger" {
		t.Fatalf("ActivateGroupsForTools = %v, want [danger]", got)
	}

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !specNames(client.tools[0])["danger_tool"] {
		t.Error("a skill-activated tool must be advertised")
	}
	if !danger.executed {
		t.Error("a skill-activated tool must still dispatch")
	}
	if !gateFired {
		t.Error("the permission gate must still fire for a skill-activated tool (visibility != authority)")
	}
}

// Off lazy mode, skill-driven activation is a no-op (everything is already
// advertised — there is nothing to reveal).
func TestActivateGroupsForToolsOffLazyNoop(t *testing.T) {
	reg := Registry{"danger_tool": extTool("danger_tool", "danger")}
	a := NewAgent(nil, "m", "s", reg)
	if got := a.ActivateGroupsForTools([]string{"danger_tool"}); got != nil {
		t.Errorf("off lazy mode activation must be a no-op, got %v", got)
	}
}

// ToolsInGroup lists a group's registered tools and is the activate_tools guard.
func TestToolsInGroup(t *testing.T) {
	reg := Registry{
		"read":      &flagTool{name: "read"},
		"mail_send": extTool("mail_send", "mail"),
		"mail_read": extTool("mail_read", "mail"),
	}
	a := NewAgent(nil, "m", "sys", reg)
	got := a.ToolsInGroup("mail")
	if len(got) != 2 || got[0] != "mail_read" || got[1] != "mail_send" {
		t.Errorf("ToolsInGroup(mail) = %v, want [mail_read mail_send]", got)
	}
	if len(a.ToolsInGroup("nope")) != 0 {
		t.Error("an absent group must return no tools")
	}
}
