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
	group     string
	essential bool
}

func (e *extToolFake) Extension() string { return e.group }
func (e *extToolFake) Essential() bool   { return e.essential }

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

func essentialExtTool(name, group string) *extToolFake {
	return &extToolFake{flagTool: &flagTool{name: name}, group: group, essential: true}
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

// An essential (load-bearing) extension tool stays advertised even though its
// group is inactive, while its non-essential sibling in the same group stays
// hidden — and the capability note lists only the deferred sibling, never the
// already-visible essential tool. This is the "guidance names a tool the model
// must see" case: index_search rides along, index_rebuild waits for activation.
func TestLazyToolsAdvertisesEssentialToolFromInactiveGroup(t *testing.T) {
	reg := Registry{
		"read":          &flagTool{name: "read"},
		"index_search":  essentialExtTool("index_search", "index"),
		"index_rebuild": extTool("index_rebuild", "index"),
	}
	client := &reqCaptureClient{}
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}

	adv := specNames(client.tools[0])
	if !adv["index_search"] {
		t.Error("an essential tool must be advertised even from an inactive group")
	}
	if adv["index_rebuild"] {
		t.Error("a non-essential sibling in the same group must stay hidden")
	}
	note := client.ephemeral[0]
	if !strings.Contains(note, "index_rebuild") {
		t.Errorf("the capability note should list the deferred sibling; note = %q", note)
	}
	if strings.Contains(note, "index_search") {
		t.Errorf("the capability note must not list the already-advertised essential tool; note = %q", note)
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

// multiActivatorTool activates several groups in a single call, so one tool
// batch can dirty more than one group.
type multiActivatorTool struct{ groups []string }

func (m *multiActivatorTool) Name() string            { return "activate" }
func (m *multiActivatorTool) Description() string     { return "activates groups" }
func (m *multiActivatorTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (m *multiActivatorTool) Execute(ctx context.Context, _ json.RawMessage, _ func(string)) (ToolResult, error) {
	if a := AgentFromContext(ctx); a != nil {
		for _, g := range m.groups {
			a.ActivateGroup(g)
		}
	}
	return ToolResult{Content: []provider.Content{provider.TextBlock{Text: "activated"}}}, nil
}

// seqToolClient plays a fixed sequence of tool calls (one per step), ending
// naturally once the sequence is exhausted — so a test can drive "activate on
// step 1, then use the newly visible tool on step 2". Captures req.Tools.
type seqToolClient struct {
	mu    sync.Mutex
	tools [][]provider.Tool
	seq   []string // tool to call at each step; past the end, the step ends
	calls int
}

func (c *seqToolClient) Name() string { return "seq" }

func (c *seqToolClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	c.mu.Lock()
	n := c.calls
	c.calls++
	c.tools = append(c.tools, req.Tools)
	c.mu.Unlock()

	out := make(chan provider.Event, 4)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "seq", Model: req.Model}
		if n < len(c.seq) && c.seq[n] != "" {
			out <- provider.EventDone{Stop: provider.StopToolUse, Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.ToolCallBlock{ID: fmt.Sprintf("s%d", n), Name: c.seq[n], Arguments: json.RawMessage(`{}`)}},
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

// Immediate tool activation, default ON: a tool that activates a group makes
// that group's tools available on the very NEXT model step, within the same
// segment — no synthetic continuation, no natural-stop handoff. The completed
// activate_tools call is the synchronization boundary. This supersedes the old
// activation-continuation boundary (notes/immediate-tool-activation.md).
func TestImmediateActivationSameSegment(t *testing.T) {
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
	// Two requests: the activating tool step, then the next step with the group
	// already live. No third natural-stop continuation request.
	if len(client.tools) != 2 {
		t.Fatalf("want 2 requests (tool step, then live), got %d", len(client.tools))
	}
	if specNames(client.tools[0])["mail_send"] {
		t.Error("the activating step must still hide mail_send (the batch it belongs to is pinned)")
	}
	if !specNames(client.tools[1])["mail_send"] {
		t.Error("the next model step must advertise the newly activated tools")
	}
	if strings.Contains(client.ephemeral[1], "mail_send") {
		t.Error("the refreshed capability note must no longer list the activated group")
	}
	// No synthetic continuation, no synthetic nudge on the normal tool path.
	if len(causes) != 0 {
		t.Errorf("no EvContinuation should fire on the immediate tool path, got %v", causes)
	}
	for _, m := range a.Messages() {
		if m.Role == provider.RoleUser && m.Meta[MetaSynthetic] == "true" {
			t.Errorf("no synthetic activation nudge should be injected, got %q", extractText(m))
		}
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

// The natural-stop activation gate is retained as the FALLBACK for a group
// activated OFF the tool path (a host-side / asynchronous ActivateGroup while
// the model produces a non-tool reply). It stays capped — defense in depth,
// since activation is monotonic: a pathological run that activates a fresh group
// on every step stops being continued after activationContinuationCap fires.
func TestActivationFallbackGateCap(t *testing.T) {
	reg := Registry{"read": &flagTool{name: "read"}}
	for i := 1; i <= 6; i++ {
		name := fmt.Sprintf("t%d", i)
		reg[name] = extTool(name, fmt.Sprintf("g%d", i))
	}
	client := &reqCaptureClient{} // no tool calls: every step ends naturally
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()
	// Activate a fresh group at the top of every request — an async activation
	// with no post-tool boundary, so only the fallback gate can land it.
	client.onCall = func(n int) { a.ActivateGroup(fmt.Sprintf("g%d", n)) }

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
		t.Errorf("want exactly %d fallback continuations, got %d", activationContinuationCap, len(causes))
	}
	for _, c := range causes {
		if c != "activation" {
			t.Errorf("fallback continuations should carry the activation cause, got %q", c)
		}
	}
}

// A group activated OFF the tool path has no post-tool boundary to ride, so the
// natural-stop activation gate is what lands it: one continuation, and the next
// step advertises the group. This is the fallback the immediate path keeps.
func TestActivationFallbackGateFiresForAsyncActivation(t *testing.T) {
	reg := Registry{
		"read":      &flagTool{name: "read"},
		"mail_send": extTool("mail_send", "mail"),
	}
	client := &reqCaptureClient{}
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()
	client.onCall = func(n int) {
		if n == 1 {
			a.ActivateGroup("mail") // async: not via a tool call
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
	if len(client.tools) != 2 {
		t.Fatalf("want 2 requests (natural end, then fallback continuation), got %d", len(client.tools))
	}
	if specNames(client.tools[0])["mail_send"] {
		t.Error("the first step must hide mail_send (pinned before the async activation)")
	}
	if !specNames(client.tools[1])["mail_send"] {
		t.Error("the fallback continuation must advertise the async-activated group")
	}
	if len(causes) != 1 || causes[0] != "activation" {
		t.Errorf("want one activation continuation from the fallback gate, got %v", causes)
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
	a.BeforeToolExecute = func(_ context.Context, call provider.ToolCallBlock) (bool, string, json.RawMessage) {
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

// One tool batch that activates two groups refreshes the pin once and both
// groups are live on the next model step — no continuation, no double repin.
func TestImmediateActivationMultipleGroupsOneRepin(t *testing.T) {
	reg := Registry{
		"read":      &flagTool{name: "read"},
		"activate":  &multiActivatorTool{groups: []string{"mail", "cal"}},
		"mail_send": extTool("mail_send", "mail"),
		"cal_add":   extTool("cal_add", "cal"),
	}
	client := &reqCaptureClient{toolThen: "activate"}
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()

	var causes []string
	if err := a.Prompt(context.Background(), "go", nil, func(ev AgentEvent) {
		if e, ok := ev.(EvContinuation); ok {
			causes = append(causes, e.Cause)
		}
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(client.tools) != 2 {
		t.Fatalf("want 2 requests, got %d", len(client.tools))
	}
	adv := specNames(client.tools[1])
	if !adv["mail_send"] || !adv["cal_add"] {
		t.Errorf("both activated groups must be live on the next step, advertised = %v", adv)
	}
	if len(causes) != 0 {
		t.Errorf("one batch activating two groups needs no continuation, got %v", causes)
	}
}

// Re-activating an already-active (and already-advertised) group dirties
// nothing: no refresh, no continuation, and behavior is unchanged.
func TestImmediateActivationIdempotentAlreadyActive(t *testing.T) {
	reg := Registry{
		"read":      &flagTool{name: "read"},
		"activate":  &groupActivatorTool{group: "mail"},
		"mail_send": extTool("mail_send", "mail"),
	}
	client := &reqCaptureClient{toolThen: "activate"}
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()
	a.ActivateGroup("mail") // already active before the Prompt

	var causes []string
	if err := a.Prompt(context.Background(), "go", nil, func(ev AgentEvent) {
		if e, ok := ev.(EvContinuation); ok {
			causes = append(causes, e.Cause)
		}
	}); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	// mail was pinned from the first step (active before the pin), so the tool's
	// re-activation reveals nothing new.
	if !specNames(client.tools[0])["mail_send"] {
		t.Error("a group active before the Prompt is advertised from the first step")
	}
	if len(causes) != 0 {
		t.Errorf("re-activating an active group must not continue, got %v", causes)
	}
	for _, m := range a.Messages() {
		if m.Role == provider.RoleUser && m.Meta[MetaSynthetic] == "true" {
			t.Errorf("no synthetic nudge on an idempotent activation, got %q", extractText(m))
		}
	}
}

// The newly visible tool is actually usable on the next step: the model calls
// it, it dispatches through the full registry, and its permission gate still
// runs (visibility is not authority).
func TestImmediateActivationNewlyVisibleToolExecutes(t *testing.T) {
	mail := extTool("mail_send", "mail")
	reg := Registry{
		"read":      &flagTool{name: "read"},
		"activate":  &groupActivatorTool{group: "mail"},
		"mail_send": mail,
	}
	client := &seqToolClient{seq: []string{"activate", "mail_send"}} // activate, then use it
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()

	gateFired := false
	a.BeforeToolExecute = func(_ context.Context, call provider.ToolCallBlock) (bool, string, json.RawMessage) {
		if call.Name == "mail_send" {
			gateFired = true
		}
		return true, "", nil
	}

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if len(client.tools) < 2 || !specNames(client.tools[1])["mail_send"] {
		t.Fatal("mail_send must be advertised on the step after activation")
	}
	if !mail.executed {
		t.Error("the newly activated tool must dispatch through the full registry")
	}
	if !gateFired {
		t.Error("the newly activated tool must still face its permission gate (visibility != authority)")
	}
}

// An essential sibling stays visible before activation; the non-essential
// sibling appears immediately on the post-activation step, and the refreshed
// note lists only the (now gone) deferred sibling.
func TestImmediateActivationEssentialSiblingStaysVisible(t *testing.T) {
	reg := Registry{
		"read":          &flagTool{name: "read"},
		"activate":      &groupActivatorTool{group: "index"},
		"index_search":  essentialExtTool("index_search", "index"),
		"index_rebuild": extTool("index_rebuild", "index"),
	}
	client := &reqCaptureClient{toolThen: "activate"}
	a := NewAgent(client, "m", "sys", reg)
	a.EnableLazyTools()

	if err := a.Prompt(context.Background(), "go", nil, nil); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if !specNames(client.tools[0])["index_search"] {
		t.Error("the essential tool must be advertised before activation")
	}
	if specNames(client.tools[0])["index_rebuild"] {
		t.Error("the non-essential sibling must be hidden before activation")
	}
	if !specNames(client.tools[1])["index_rebuild"] {
		t.Error("the non-essential sibling must appear on the immediate post-activation step")
	}
	if strings.Contains(client.ephemeral[1], "index_rebuild") {
		t.Error("the refreshed note must not list the now-activated sibling")
	}
}
