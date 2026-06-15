//go:build terva_acp

package acp

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// countingTextClient streams one text turn (like textTurnClient) but counts
// how many times Stream is called, so a test can assert the model was (or was
// NOT) invoked — the load-bearing check for native slash execution: a
// natively-handled /clear must never reach the provider.
type countingTextClient struct {
	calls int32
	reply string
}

func (c *countingTextClient) Name() string { return "fake-acp-counting" }

func (c *countingTextClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 8)
	reply := c.reply
	if reply == "" {
		reply = "assistant reply"
	}
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "fake-acp-counting", Model: req.Model}
		out <- provider.EventTextDelta{Delta: reply}
		out <- provider.EventDone{
			Stop:    provider.StopEnd,
			Message: provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: reply}}},
		}
	}()
	return out, nil
}

// commandSetup spins up Serve, runs initialize + session/new, and returns the
// harness, the sessionId, and a teardown. Mirrors permSetup but returns the
// session/new result's buffered updates are left on h.updates for the caller.
func commandSetup(t *testing.T, factory AgentFactory) (*harness, string, func()) {
	t.Helper()
	caR, caW := io.Pipe()
	acR, acW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(ctx, caR, acW, factory, AgentInfo{Name: "terva", Version: "test"}) }()

	h := newHarness(t, caW, acR)
	h.call(MethodInitialize, map[string]any{"protocolVersion": 1})
	newRes := h.call(MethodSessionNew, map[string]any{"cwd": t.TempDir()})
	sid, _ := newRes["sessionId"].(string)
	if sid == "" {
		t.Fatal("session/new returned empty sessionId")
	}
	teardown := func() {
		cancel()
		_ = caW.Close()
		_ = acW.Close()
		_ = caR.Close()
		_ = acR.Close()
		select {
		case <-serveErr:
		case <-time.After(2 * time.Second):
		}
	}
	return h, sid, teardown
}

// findAvailableCommands scans a batch of session/update params for the
// available_commands_update notification and returns the command-name set it
// carries (keyed by the bare name as sent on the wire).
func findAvailableCommands(updates []map[string]any) (names map[string]bool, found bool) {
	names = map[string]bool{}
	for _, u := range updates {
		upd, _ := u["update"].(map[string]any)
		if upd == nil || upd["sessionUpdate"] != UpdateAvailableCommands {
			continue
		}
		found = true
		cmds, _ := upd["availableCommands"].([]any)
		for _, ci := range cmds {
			cm, _ := ci.(map[string]any)
			if name, _ := cm["name"].(string); name != "" {
				names[name] = true
			}
		}
	}
	return names, found
}

// TestACPAvailableCommandsAdvertisedOnNew proves verification (a): session/new
// emits an available_commands_update carrying the curated catalog — the
// natively-executed commands are present (clear/compact) and a TUI-only picker
// (model) is absent (capability honesty).
func TestACPAvailableCommandsAdvertisedOnNew(t *testing.T) {
	factory := &fakeFactory{client: &countingTextClient{}, tools: core.Registry{}}
	h, _, teardown := commandSetup(t, factory)
	defer teardown()

	names, found := findAvailableCommands([]map[string]any{h.expectUpdate()})
	if !found {
		t.Fatal("session/new did not emit an available_commands_update")
	}
	// The curated, natively-executed commands MUST be advertised (bare names,
	// no leading slash — the editor prepends it).
	if !names["clear"] {
		t.Error("available_commands_update missing /clear (it is executed natively)")
	}
	if !names["compact"] {
		t.Error("available_commands_update missing /compact (it is executed natively)")
	}
	// Phase 4c expansion: /help, /permissions, /jail, /unjail, /skills are now
	// advertised AND executed natively. The verification spec calls out /help
	// and /permissions explicitly; assert the whole shipped set.
	for _, want := range []string{"help", "permissions", "jail", "unjail", "skills"} {
		if !names[want] {
			t.Errorf("available_commands_update missing /%s (it is executed natively)", want)
		}
	}
	// The two extMgr-reading built-ins are advertised + executed natively too,
	// even for a session with no extension manager (they degrade gracefully).
	for _, want := range []string{"context", "reload-ext"} {
		if !names[want] {
			t.Errorf("available_commands_update missing /%s (it is executed natively)", want)
		}
	}
	// A TUI-only picker the ACP frontend can't drive MUST NOT be advertised —
	// the editor already has the model config option (Phase 4b).
	if names["model"] {
		t.Error("available_commands_update advertised /model; a TUI-only picker must be excluded")
	}
	if names["sessions"] {
		t.Error("available_commands_update advertised /sessions; the editor has session/list instead")
	}
}

// drainChunkText concatenates the text of every agent_message_chunk in a batch
// of session/update params — the load-bearing observable effect for the
// text-emitting native commands (/help, /skills, /permissions).
func drainChunkText(updates []map[string]any) string {
	var b strings.Builder
	for _, u := range updates {
		upd, _ := u["update"].(map[string]any)
		if upd == nil || upd["sessionUpdate"] != UpdateAgentMessageChunk {
			continue
		}
		content, _ := upd["content"].(map[string]any)
		if text, _ := content["text"].(string); text != "" {
			b.WriteString(text)
		}
	}
	return b.String()
}

// TestACPSlashHelpExecutesNatively proves /help runs headlessly: no model call,
// resolves end_turn, and emits a non-empty agent_message_chunk that names the
// ACP-available commands (derived from availableCommands()).
func TestACPSlashHelpExecutesNatively(t *testing.T) {
	client := &countingTextClient{}
	factory := &fakeFactory{client: client, tools: core.Registry{}}
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()
	_ = h.drainUpdates() // discard the post-session/new catalog

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/help"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/help stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for /help; want 0 (native execution)", got)
	}
	text := drainChunkText(h.drainUpdates())
	if text == "" {
		t.Fatal("/help emitted no agent_message_chunk text")
	}
	// The overview should mention terva and list a curated command (derived
	// from availableCommands(), so it stays in sync).
	if !strings.Contains(text, "terva") {
		t.Errorf("/help text did not mention terva: %q", text)
	}
	if !strings.Contains(text, "/clear") {
		t.Errorf("/help text did not list the available commands: %q", text)
	}
}

// TestACPSlashSkillsExecutesNatively proves /skills runs headlessly: no model
// call, end_turn, and emits a chunk listing the seeded skills (name +
// description).
func TestACPSlashSkillsExecutesNatively(t *testing.T) {
	client := &countingTextClient{}
	factory := &fakeFactory{
		client:     client,
		tools:      core.Registry{},
		withSkills: true,
		skillList: []*skills.Skill{
			{Name: "deep-research", Description: "fan-out web search + synthesis"},
			{Name: "release", Description: "cut a terva release"},
		},
	}
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()
	_ = h.drainUpdates()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/skills"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/skills stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for /skills; want 0", got)
	}
	text := drainChunkText(h.drainUpdates())
	if !strings.Contains(text, "deep-research") || !strings.Contains(text, "release") {
		t.Errorf("/skills text missing seeded skill names: %q", text)
	}
	if !strings.Contains(text, "fan-out web search + synthesis") {
		t.Errorf("/skills text missing skill descriptions: %q", text)
	}
}

// TestACPSlashSkillsNoneDiscovered proves the empty path: with a skills source
// but no skills, /skills still degrades to a non-empty note (not silence).
func TestACPSlashSkillsNoneDiscovered(t *testing.T) {
	client := &countingTextClient{}
	factory := &fakeFactory{client: client, tools: core.Registry{}, withSkills: true, skillList: nil}
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()
	_ = h.drainUpdates()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/skills"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/skills (none) stopReason = %q; want end_turn", res["stopReason"])
	}
	if text := drainChunkText(h.drainUpdates()); text == "" {
		t.Error("/skills with no skills emitted no note (must degrade with a chunk)")
	}
}

// TestACPSlashPermissionsExecutesNatively proves /permissions runs headlessly:
// no model call, end_turn, and emits a chunk summarizing the approval mode and
// rules. The fake factory's gateMode seeds a live gate so the summary has a
// real mode to report.
func TestACPSlashPermissionsExecutesNatively(t *testing.T) {
	client := &countingTextClient{}
	factory := &fakeFactory{client: client, tools: core.Registry{}, gateMode: core.ApprovalAsk}
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()
	_ = h.drainUpdates()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/permissions"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/permissions stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for /permissions; want 0", got)
	}
	text := drainChunkText(h.drainUpdates())
	if !strings.Contains(text, "Approval mode") {
		t.Errorf("/permissions text missing approval mode summary: %q", text)
	}
	if !strings.Contains(text, "ask") {
		t.Errorf("/permissions text did not report the seeded mode 'ask': %q", text)
	}
}

// TestACPSlashJailUnjailFlipsSandbox proves /jail and /unjail run headlessly
// and actually toggle the shared sandbox's confinement state — the observable
// effect — without calling the model, each resolving end_turn.
func TestACPSlashJailUnjailFlipsSandbox(t *testing.T) {
	client := &countingTextClient{}
	sb := tools.NewSandbox(t.TempDir())
	factory := &fakeFactory{client: client, tools: core.Registry{}, sandbox: sb}
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()
	_ = h.drainUpdates()

	if sb.Locked() {
		t.Fatal("sandbox started locked; expected unlocked")
	}

	jailRes := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/jail"}},
	})
	if sr, _ := jailRes["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/jail stopReason = %q; want end_turn", jailRes["stopReason"])
	}
	if !sb.Locked() {
		t.Error("/jail did not lock the sandbox")
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for /jail; want 0", got)
	}
	if text := drainChunkText(h.drainUpdates()); text == "" {
		t.Error("/jail emitted no confirmation chunk")
	}

	unjailRes := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/unjail"}},
	})
	if sr, _ := unjailRes["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/unjail stopReason = %q; want end_turn", unjailRes["stopReason"])
	}
	if sb.Locked() {
		t.Error("/unjail did not unlock the sandbox")
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times after /jail+/unjail; want 0", got)
	}
}

// TestACPSlashJailNoSandboxDegrades proves /jail degrades gracefully with no
// sandbox in the build: a note, end_turn, no model call, no panic.
func TestACPSlashJailNoSandboxDegrades(t *testing.T) {
	client := &countingTextClient{}
	factory := &fakeFactory{client: client, tools: core.Registry{}} // sandbox nil
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()
	_ = h.drainUpdates()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/jail"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/jail (no sandbox) stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for /jail with no sandbox; want 0", got)
	}
	if text := drainChunkText(h.drainUpdates()); text == "" {
		t.Error("/jail with no sandbox emitted no note (must degrade with a chunk)")
	}
}

// TestACPAvailableCommandsAdvertisedOnLoad proves the catalog is re-advertised
// on session/load so a resumed session's command palette is repopulated.
func TestACPAvailableCommandsAdvertisedOnLoad(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()
	factory := &fakeFactory{client: &countingTextClient{reply: "answer"}, tools: core.Registry{}, root: root}

	caR, caW := io.Pipe()
	acR, acW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(ctx, caR, acW, factory, AgentInfo{Name: "terva", Version: "test"}) }()

	h := newHarness(t, caW, acR)
	h.call(MethodInitialize, map[string]any{"protocolVersion": 1})
	newRes := h.call(MethodSessionNew, map[string]any{"cwd": cwd})
	sid, _ := newRes["sessionId"].(string)
	// Prompt so the session has a transcript to reopen.
	h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "hi"}},
	})
	_ = h.drainUpdates() // clear the new-session catalog + the turn's chunks

	h.call(MethodSessionLoad, map[string]any{"sessionId": sid, "cwd": cwd, "mcpServers": []any{}})

	names, found := findAvailableCommands(h.drainUpdates())
	if !found {
		t.Fatal("session/load did not re-emit an available_commands_update")
	}
	if !names["clear"] || !names["compact"] {
		t.Errorf("resumed session catalog missing curated commands: %v", names)
	}

	cancel()
	_ = caW.Close()
	_ = acW.Close()
	_ = caR.Close()
	_ = acR.Close()
	select {
	case <-serveErr:
	case <-time.After(2 * time.Second):
	}
}

// TestACPSlashClearExecutesNatively proves verification (b): a session/prompt
// whose text is "/clear" is executed natively — it clears the agent's
// transcript WITHOUT calling the model, and resolves end_turn. We first run a
// real prompt to populate the transcript (and bump the model's call count),
// then send /clear and assert the count did NOT change and the messages are
// gone.
func TestACPSlashClearExecutesNatively(t *testing.T) {
	client := &countingTextClient{reply: "real answer"}
	factory := &fakeFactory{client: client, tools: core.Registry{}}
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()

	// A real turn so the transcript is non-empty and the model has been
	// called exactly once.
	promptRes := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "say something"}},
	})
	if sr, _ := promptRes["stopReason"].(string); sr != StopEndTurn {
		t.Fatalf("real prompt stopReason = %q; want end_turn", promptRes["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 1 {
		t.Fatalf("model called %d times after one real turn; want 1", got)
	}
	ag := factory.lastNewAgent()
	if ag == nil {
		t.Fatal("no agent built")
	}
	if len(ag.Messages()) == 0 {
		t.Fatal("transcript is empty after a real turn; expected user + assistant messages")
	}

	// Now /clear. It must execute natively: no model call, transcript cleared,
	// turn resolves end_turn.
	clearRes := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/clear"}},
	})
	if sr, _ := clearRes["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/clear stopReason = %q; want end_turn", clearRes["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 1 {
		t.Errorf("model called %d times after /clear; want 1 (native execution must NOT call the model)", got)
	}
	if msgs := ag.Messages(); len(msgs) != 0 {
		t.Errorf("transcript has %d messages after /clear; want 0 (SetMessages(nil))", len(msgs))
	}
}

// TestACPUnknownSlashDegrades proves an advertised-but-unhandled or unknown
// slash command degrades gracefully: a brief agent_message_chunk note, the
// turn resolves end_turn, and the model is NOT called (the "/foo" is never
// forwarded as a literal prompt).
func TestACPUnknownSlashDegrades(t *testing.T) {
	client := &countingTextClient{}
	factory := &fakeFactory{client: client, tools: core.Registry{}}
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()
	_ = h.drainUpdates() // catalog now follows the response; the prompt below pulls it (drained at the end)

	// /model is a real built-in but TUI-only (excluded from the ACP catalog);
	// if the editor sends it anyway it must degrade, not forward to the model.
	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/model gpt-5"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/model stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for an unhandled slash command; want 0 (must not forward)", got)
	}
	var sawNote bool
	for _, u := range h.drainUpdates() {
		upd, _ := u["update"].(map[string]any)
		if upd["sessionUpdate"] == UpdateAgentMessageChunk {
			content, _ := upd["content"].(map[string]any)
			if text, _ := content["text"].(string); text != "" {
				sawNote = true
			}
		}
	}
	if !sawNote {
		t.Error("no agent_message_chunk note for the unhandled slash command (must degrade gracefully)")
	}

	// A genuinely unknown verb degrades the same way (and is NOT forwarded).
	res2 := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/definitely-not-a-command"}},
	})
	if sr, _ := res2["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("unknown-command stopReason = %q; want end_turn", res2["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for an unknown slash command; want 0", got)
	}
}

// TestACPSlashLikePathRunsAsPrompt proves a path-looking input ("/etc/hosts")
// is NOT mistaken for a slash command — it runs as an ordinary model turn
// (the model IS called), so legitimate prompts starting with "/" still work.
func TestACPSlashLikePathRunsAsPrompt(t *testing.T) {
	client := &countingTextClient{}
	factory := &fakeFactory{client: client, tools: core.Registry{}}
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/etc/hosts what is in here?"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("path-prompt stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 1 {
		t.Errorf("model called %d times for a path-looking prompt; want 1 (must run as a normal turn)", got)
	}
}

// TestACPSlashTrustInvokesAndReadvertises proves the /trust verification: it
// invokes the host's trustWorkspace closure (the counter increments), emits a
// confirmation chunk, ends the turn without calling the model, AND re-emits
// available_commands_update (the reload may bring project extension commands
// online, so the catalog is re-advertised — the same lockstep /reload-ext keeps).
func TestACPSlashTrustInvokesAndReadvertises(t *testing.T) {
	client := &countingTextClient{}
	factory := &fakeFactory{client: client, tools: core.Registry{}, wireTrust: true}
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()
	_ = h.drainUpdates() // the post-session/new catalog

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/trust"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/trust stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for /trust; want 0", got)
	}
	if got := atomic.LoadInt32(&factory.trustCalls); got != 1 {
		t.Errorf("TrustWorkspace ran %d times; want 1 (the command must invoke the trust closure)", got)
	}
	if got := atomic.LoadInt32(&factory.trustParent); got != 0 {
		t.Errorf("bare /trust requested parent scope (parentFlag=%d); want 0 (this directory only)", got)
	}

	updates := h.drainUpdates()
	if text := drainChunkText(updates); !strings.Contains(text, "Trusted") {
		t.Errorf("/trust did not emit a confirmation chunk: %q", text)
	}
	if _, found := findAvailableCommands(updates); !found {
		t.Error("/trust did not re-emit an available_commands_update")
	}
}

// TestACPSlashTrustParentThreadsScope proves `/trust parent` threads the wider
// "trust descendants too" scope through to the host closure (parentFlag set),
// and the confirmation names the wider scope.
func TestACPSlashTrustParentThreadsScope(t *testing.T) {
	client := &countingTextClient{}
	factory := &fakeFactory{client: client, tools: core.Registry{}, wireTrust: true}
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()
	_ = h.drainUpdates()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/trust parent"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/trust parent stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&factory.trustCalls); got != 1 {
		t.Errorf("TrustWorkspace ran %d times for /trust parent; want 1", got)
	}
	if got := atomic.LoadInt32(&factory.trustParent); got != 1 {
		t.Errorf("/trust parent did not request parent scope (parentFlag=%d); want 1", got)
	}
	if text := drainChunkText(h.drainUpdates()); !strings.Contains(text, "descendants") {
		t.Errorf("/trust parent confirmation did not name the wider scope: %q", text)
	}
}

// TestACPSlashUntrustInvokesAndReadvertises proves the /untrust verification:
// it invokes the host's untrustWorkspace closure, emits a confirmation chunk,
// ends the turn without a model call, and re-emits available_commands_update.
func TestACPSlashUntrustInvokesAndReadvertises(t *testing.T) {
	client := &countingTextClient{}
	factory := &fakeFactory{client: client, tools: core.Registry{}, wireTrust: true}
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()
	_ = h.drainUpdates()

	res := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "/untrust"}},
	})
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("/untrust stopReason = %q; want end_turn", res["stopReason"])
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for /untrust; want 0", got)
	}
	if got := atomic.LoadInt32(&factory.untrustCalls); got != 1 {
		t.Errorf("UntrustWorkspace ran %d times; want 1 (the command must invoke the untrust closure)", got)
	}

	updates := h.drainUpdates()
	if text := drainChunkText(updates); !strings.Contains(text, "Untrusted") {
		t.Errorf("/untrust did not emit a confirmation chunk: %q", text)
	}
	if _, found := findAvailableCommands(updates); !found {
		t.Error("/untrust did not re-emit an available_commands_update")
	}
}

// TestACPSlashTrustAdvertisedOnNew proves /trust and /untrust are in the
// session/new command catalog so an editor's palette offers them.
func TestACPSlashTrustAdvertisedOnNew(t *testing.T) {
	factory := &fakeFactory{client: &countingTextClient{}, tools: core.Registry{}, wireTrust: true}
	h, _, teardown := commandSetup(t, factory)
	defer teardown()

	names, found := findAvailableCommands([]map[string]any{h.expectUpdate()})
	if !found {
		t.Fatal("session/new did not emit an available_commands_update")
	}
	for _, want := range []string{"trust", "untrust"} {
		if !names[want] {
			t.Errorf("available_commands_update missing /%s (it is executed natively)", want)
		}
	}
}

// TestACPSlashTrustNoClosureDegrades proves the nil-closure path: with no
// trustWorkspace wired (host didn't supply it), /trust degrades to a clear note
// — a chunk, end_turn, no model call, no panic.
func TestACPSlashTrustNoClosureDegrades(t *testing.T) {
	client := &countingTextClient{}
	factory := &fakeFactory{client: client, tools: core.Registry{}} // wireTrust false -> nil closures
	h, sid, teardown := commandSetup(t, factory)
	defer teardown()
	_ = h.drainUpdates()

	for _, cmd := range []string{"/trust", "/untrust"} {
		res := h.call(MethodSessionPromptName, map[string]any{
			"sessionId": sid,
			"prompt":    []map[string]any{{"type": "text", "text": cmd}},
		})
		if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
			t.Errorf("%s (no closure) stopReason = %q; want end_turn", cmd, res["stopReason"])
		}
		if text := drainChunkText(h.drainUpdates()); !strings.Contains(text, "isn't available") {
			t.Errorf("%s with no closure did not degrade gracefully: %q", cmd, text)
		}
	}
	if got := atomic.LoadInt32(&client.calls); got != 0 {
		t.Errorf("model called %d times for trust commands with no closures; want 0", got)
	}
}
