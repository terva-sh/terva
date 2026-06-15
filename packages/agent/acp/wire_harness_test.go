//go:build terva_acp

package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
)

// ---- fake provider client (mirrors the *_test.go fake-client pattern) ----
//
// toolTurnClient drives a two-turn tool conversation:
//
//	turn 1: stream a text delta, then a tool_use block, stop=tool_use
//	turn 2 (after the tool ran): stream a final text delta, stop=end
type toolTurnClient struct {
	calls    int32
	toolName string
	toolArgs string
}

func (c *toolTurnClient) Name() string { return "fake-acp" }

func (c *toolTurnClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	call := atomic.AddInt32(&c.calls, 1)
	out := make(chan provider.Event, 8)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "fake-acp", Model: req.Model}
		if call == 1 {
			out <- provider.EventTextDelta{Delta: "working on it"}
			out <- provider.EventDone{
				Stop: provider.StopToolUse,
				Message: provider.Message{
					Role: provider.RoleAssistant,
					Content: []provider.Content{
						provider.TextBlock{Text: "working on it"},
						provider.ToolCallBlock{ID: "call-1", Name: c.toolName, Arguments: json.RawMessage(c.toolArgs)},
					},
				},
			}
			return
		}
		out <- provider.EventTextDelta{Delta: "all done"}
		out <- provider.EventDone{
			Stop: provider.StopEnd,
			Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "all done"}},
			},
		}
	}()
	return out, nil
}

// editFileTool is a minimal tool that overwrites a file so the edit-diff
// snapshot path (pre/post file read) has something real to diff. Named
// "edit" so the translator derives kind=edit and snapshots the path.
type editFileTool struct{}

func (editFileTool) Name() string        { return "edit" }
func (editFileTool) Description() string { return "overwrite a file" }
func (editFileTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`)
}
func (editFileTool) Execute(_ context.Context, raw json.RawMessage, _ func(string)) (core.ToolResult, error) {
	var a struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return core.ToolResult{IsError: true, Content: []provider.Content{provider.TextBlock{Text: err.Error()}}}, nil
	}
	if err := os.WriteFile(a.Path, []byte(a.Content), 0o644); err != nil {
		return core.ToolResult{IsError: true, Content: []provider.Content{provider.TextBlock{Text: err.Error()}}}, nil
	}
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "wrote " + a.Path}}}, nil
}

// multiToolClient drives a conversation that calls the SAME tool once per
// turn for `toolTurns` turns, then a final text turn. Each tool-call turn
// uses a distinct toolCallId so the editor (and the confirmer) can correlate
// per call. Used by the permission tests, where a turn does not complete
// until the tool's BeforeToolExecute gate resolves.
type multiToolClient struct {
	calls     int32
	toolName  string
	toolArgs  string
	toolTurns int
}

func (c *multiToolClient) Name() string { return "fake-acp-multi" }

func (c *multiToolClient) Stream(ctx context.Context, req provider.Request) (<-chan provider.Event, error) {
	call := int(atomic.AddInt32(&c.calls, 1))
	out := make(chan provider.Event, 8)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "fake-acp-multi", Model: req.Model}
		if call <= c.toolTurns {
			id := "call-" + strconv.Itoa(call)
			out <- provider.EventTextDelta{Delta: "calling tool"}
			out <- provider.EventDone{
				Stop: provider.StopToolUse,
				Message: provider.Message{
					Role: provider.RoleAssistant,
					Content: []provider.Content{
						provider.TextBlock{Text: "calling tool"},
						provider.ToolCallBlock{ID: id, Name: c.toolName, Arguments: json.RawMessage(c.toolArgs)},
					},
				},
			}
			return
		}
		out <- provider.EventTextDelta{Delta: "all done"}
		out <- provider.EventDone{
			Stop: provider.StopEnd,
			Message: provider.Message{
				Role:    provider.RoleAssistant,
				Content: []provider.Content{provider.TextBlock{Text: "all done"}},
			},
		}
	}()
	return out, nil
}

// countingTool records how many times it ran; it has no path arg so it
// stays kind=other and never triggers the edit-snapshot path. Used to
// assert allow/deny actually gated execution.
type countingTool struct {
	name string
	runs int32
}

func (t *countingTool) Name() string        { return t.name }
func (t *countingTool) Description() string { return "counts invocations" }
func (t *countingTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *countingTool) Execute(_ context.Context, _ json.RawMessage, _ func(string)) (core.ToolResult, error) {
	atomic.AddInt32(&t.runs, 1)
	return core.ToolResult{Content: []provider.Content{provider.TextBlock{Text: "ran"}}}, nil
}

// fakeFactory builds an agent backed by the fake client + the edit tool.
//
// When askTool is set, the agent is wired with a real confirm gate in
// "ask" approval mode that asks about that tool — exercising the Phase 2
// session/request_permission round-trip. The provided confirmer (the ACP
// confirmer) is the gate's inner Confirmer, and recordCall is threaded into
// BeforeToolExecute exactly as the production acpFactory does, so the
// confirmer correlates by toolCallId.
type fakeFactory struct {
	client  provider.Client
	tools   core.Registry
	askTool string // when non-empty, the gate asks about this tool

	// gateMode, when set, builds a live gate at this approval mode even when
	// askTool is empty — so the Phase 4b session/set_mode tests have a gate to
	// switch and can assert the mode change actually re-gates tools.
	gateMode core.ApprovalMode

	// model is the agent's starting model id (the model selector's current
	// value). Defaults to "fake-model".
	model string

	// models is the authenticated-provider model menu ModelOptions returns
	// (Phase 4b). Empty means no model menu is advertised.
	models []ModelOption

	// switchClient is the provider.Client SwitchModel hands back on a
	// cross-provider switch (Reuse=false). When nil, SwitchModel returns
	// Reuse=true for any model in `models` whose provider matches the current
	// provider, else a fresh fake client is synthesized.
	switchClient provider.Client

	// switchMu guards lastSwitch, the most recent SwitchModel resolution, so a
	// test can assert what the host resolved.
	switchMu    sync.Mutex
	lastSwitch  ModelSwitch
	switchCalls int

	// root is the session storage root (a temp TERVA_HOME). When set, the
	// factory creates a real durable core.Session under it and wires the
	// agent's persistence hooks — exercising the Phase 3 durable-session
	// path. When empty, an in-memory session at a temp path is used so the
	// permission/cancel tests (which don't care about persistence) keep
	// working without a root.
	root string

	// loadedMu guards loadedAgent / newAgent, the agents built by the most
	// recent LoadSessionAgent / NewSessionAgent calls. The session/load test
	// reaches in to assert the reloaded agent's Messages() reflect the restored
	// transcript; the Phase 4b model-switch test reads newAgent.Model to assert
	// the effective model changed.
	loadedMu    sync.Mutex
	loadedAgent *core.Agent
	newAgent    *core.Agent

	// mcpEnabled turns on the Phase 4a MCP path in this fake factory: when
	// set, NewSessionAgent/LoadSessionAgent parse the editor-provided
	// mcpServers and start the stdio servers, merging their namespaced tools
	// into the agent's registry (mirrors the production setupACPMCP). Off by
	// default so every pre-Phase-4a test keeps its old behavior.
	mcpEnabled bool

	// mcpMu guards lastMCPTools, the registry built by the most recent
	// startMCP call (the integration test asserts the namespaced MCP tool is
	// registered on it).
	mcpMu        sync.Mutex
	lastMCPTools core.Registry

	// sandbox, when set, is carried onto every SessionAgent so the Phase 4c
	// /jail and /unjail tests can assert the command flips its Locked() state.
	// nil leaves SessionAgent.Sandbox nil (the no-sandbox degradation path).
	sandbox *tools.Sandbox

	// skillList, when non-nil, is returned by the SessionAgent's Skills
	// snapshot func so the /skills test can seed a known set. A nil slice with
	// withSkills set still yields a non-nil snapshot func returning no skills
	// (the "none discovered" path); leave withSkills false for the
	// no-skills-source path (SessionAgent.Skills stays nil).
	skillList  []*skills.Skill
	withSkills bool

	// extRoot, when set, turns on the extension-wiring path in this fake
	// factory: NewSessionAgent/LoadSessionAgent build a REAL
	// extensions.Manager rooted at extRoot (a temp TERVA_HOME with a fake
	// extension installed on disk), discover it, merge its tools into the
	// registry (read-only tools into the read-only set, exactly as the
	// production MergeToolsForMode does), wire the tool-call/turn/assistant
	// intercepts + the event observer (the ObserveEvent the acp package
	// composes after translateEvent), and stop the manager in Cleanup. The
	// gate's policy is workspace mode plus the manifest's permission rules
	// (compiled from extension.json the same way production does), so a writer
	// tool whose manifest says `ask` triggers session/request_permission while
	// a read-only tool runs unprompted. Off by default so every other test
	// keeps its old behavior.
	extRoot string

	// extMu guards lastExtMgr / lastExtReg, the most recent real
	// extensions.Manager and merged registry built by the extension path. The
	// registry test asserts the extension tool is present; the cleanup test
	// asserts the manager's subprocesses were stopped.
	extMu      sync.Mutex
	lastExtMgr *extensions.Manager
	lastExtReg core.Registry

	// extCleanups counts how many times an extension session's Cleanup func
	// ran — each call stops that session's extensions.Manager. The rebind test
	// asserts it increments when an open session id is reloaded (the superseded
	// manager is torn down, no leaked subprocess).
	extCleanups int32

	// extReloads counts how many times the SessionAgent's ReloadExtensions
	// closure ran — the /reload-ext test asserts it increments, proving the
	// reload closure was actually invoked (not just that a chunk was emitted).
	extReloads int32

	// trustCalls / untrustCalls count how many times the SessionAgent's
	// TrustWorkspace / UntrustWorkspace closures ran, and trustParent records the
	// parent flag the last /trust passed — the /trust and /untrust tests assert
	// the closure was actually invoked (not just that a chunk was emitted) and
	// that `/trust parent` threads the wider scope through. wireTrust turns the
	// closures on; left off, SessionAgent.TrustWorkspace/UntrustWorkspace stay nil
	// (the host-didn't-wire-it degradation path).
	wireTrust    bool
	trustCalls   int32
	untrustCalls int32
	trustParent  int32 // 1 if the last /trust requested parent scope, else 0

	// emptyExtContext, when set on a non-ext factory, carries an ExtContext
	// closure that returns no items — the "a manager is wired but nothing is
	// being injected" path, so /context reports the no-context note rather than
	// the not-enabled one. Distinct from a nil ExtContext (extensions disabled).
	emptyExtContext bool
}

func (f *fakeFactory) lastExtensions() *extensions.Manager {
	f.extMu.Lock()
	defer f.extMu.Unlock()
	return f.lastExtMgr
}

func (f *fakeFactory) lastExtRegistry() core.Registry {
	f.extMu.Lock()
	defer f.extMu.Unlock()
	return f.lastExtReg
}

// skillSnapshot returns the snapshot func carried on the SessionAgent: nil
// unless withSkills is set, else a closure handing back the seeded list.
func (f *fakeFactory) skillSnapshot() func() []*skills.Skill {
	if !f.withSkills {
		return nil
	}
	return func() []*skills.Skill { return f.skillList }
}

// trustWorkspaceFn / untrustWorkspaceFn return the SessionAgent trust closures:
// nil unless wireTrust is set (the host-didn't-wire-it path), else a
// counter-bumping closure mirroring the production acpTrustWorkspace /
// acpUntrustWorkspace boundary.
func (f *fakeFactory) trustWorkspaceFn() func(parent bool) error {
	if !f.wireTrust {
		return nil
	}
	return trustWorkspaceForTest(&f.trustCalls, &f.trustParent)
}

func (f *fakeFactory) untrustWorkspaceFn() func() error {
	if !f.wireTrust {
		return nil
	}
	return untrustWorkspaceForTest(&f.untrustCalls)
}

func (f *fakeFactory) lastLoadedAgent() *core.Agent {
	f.loadedMu.Lock()
	defer f.loadedMu.Unlock()
	return f.loadedAgent
}

func (f *fakeFactory) lastNewAgent() *core.Agent {
	f.loadedMu.Lock()
	defer f.loadedMu.Unlock()
	return f.newAgent
}

// buildFakeAgentWithRegistry constructs the fake-client agent + the optional
// gate over the given tool registry. Shared by NewSessionAgent and
// LoadSessionAgent so both build identical agents; only the durable session
// and the registry (with/without MCP tools) differ. Returns the agent and the
// gate (nil when no gate is built) so the SessionAgent bundle can hand the
// gate to the acp package for session/set_mode.
//
// A gate is built when askTool is set (the permission tests) OR when gateMode
// is set (the Phase 4b mode tests want a live gate to SetMode on, seeded to a
// known mode). The gate's mode is gateMode when set, else ApprovalAsk.
func (f *fakeFactory) buildFakeAgentWithRegistry(reg core.Registry, confirmer core.Confirmer, recordCall func(string)) (*core.Agent, *core.ConfirmGate) {
	if reg == nil {
		reg = core.Registry{"edit": editFileTool{}}
	}
	model := f.model
	if model == "" {
		model = "fake-model"
	}
	ag := core.NewAgent(f.client, model, "system", reg)

	if f.askTool == "" && f.gateMode == "" {
		return ag, nil
	}

	mode := f.gateMode
	if mode == "" {
		mode = core.ApprovalAsk
	}
	// A policy with an empty rule set: every tool not auto-allowed by the mode
	// defers to the Confirmer (the editor via session/request_permission). The
	// builtin/read-only classification mirrors the production gate so a switch
	// to plan/auto-edit/etc. evaluates tools the same way.
	pol := &core.PermissionPolicy{
		Mode:     mode,
		ReadOnly: core.NewReadOnlySet(),
		Builtin:  map[string]bool{},
	}
	gate := core.NewPolicyGate(pol, confirmer)
	ladder := func(call provider.ToolCallBlock) (bool, string, json.RawMessage) {
		ok, reason, _ := gate.Check(call.Name, call.Arguments, core.BuildPreview(call.Arguments, 120))
		return ok, reason, nil
	}
	ag.BeforeToolExecute = func(call provider.ToolCallBlock) (bool, string, json.RawMessage) {
		recordCall(call.ID)
		return ladder(call)
	}
	return ag, gate
}

// buildExtensionAgent mirrors the production acpFactory.buildAgent extension
// path against a fake provider client: it builds a REAL extensions.Manager
// rooted at f.extRoot, discovers the on-disk fake extension, merges its tools
// into the registry (read-only tools into roSet, exactly as production's
// MergeToolsForMode does), builds a workspace-mode gate carrying the manifest's
// permission rules, wires the canonical ladder (gate.Check THEN
// InterceptToolCall, recordCall OUTSIDE), wires the turn/assistant/context/
// continuation hooks, and returns the agent, gate, the event observer the acp
// package composes after translateEvent, and a cleanup that stops the manager.
//
// This exercises the genuine seam end-to-end: a real extension subprocess, the
// real tool-registry merge + read-only classification, the manifest -> ask rule
// compiled the same way buildPermissionPolicy compiles it, the real
// InterceptToolCall, and the production ACP OnEvent composition (bindSession is
// untouched production code).
func (f *fakeFactory) buildExtensionAgent(ctx context.Context, cwd string, confirmer core.Confirmer, recordCall func(string)) (*core.Agent, *core.ConfirmGate, func(core.AgentEvent), func(), *extensions.Manager) {
	extMgr := extensions.New(f.extRoot, cwd, "test", "fake", "fake-model", nonInteractiveExtHooksStub{})
	// Discover the on-disk fake extension; any load error surfaces as a failed
	// assertion downstream (no tools registered), so it is not swallowed.
	_ = extMgr.Discover(ctx)
	extMgr.WaitForReady(3 * time.Second)
	f.extMu.Lock()
	f.lastExtMgr = extMgr
	f.extMu.Unlock()

	// Base registry: the fakes plus a real extension-tool wrapper per
	// registered extension tool, with read-only tools joining roSet — the same
	// outcome MergeToolsForMode produces in production (replicated inline since
	// MergeToolsForMode lives in the unimportable parent package).
	reg := core.Registry{}
	for k, v := range f.tools {
		reg[k] = v
	}
	roSet := core.NewReadOnlySet()
	for _, ti := range extMgr.Tools() {
		reg[ti.Name] = extensions.NewTool(extMgr, ti)
		if ti.ReadOnly {
			roSet.Add(ti.Name)
		}
	}
	f.extMu.Lock()
	f.lastExtReg = reg
	f.extMu.Unlock()

	model := f.model
	if model == "" {
		model = "fake-model"
	}
	ag := core.NewAgent(f.client, model, "system", reg)

	// Workspace-mode policy carrying the manifest's permission rules. Workspace
	// auto-allows read-only foreign tools (so reader_tool needs no prompt) and
	// prompts for foreign side-effecting tools; the manifest's writer_tool->ask
	// rule makes the prompt explicit regardless of mode.
	pol := &core.PermissionPolicy{
		Mode:     core.ApprovalWorkspace,
		Rules:    manifestPermissionRules(f.extRoot),
		ReadOnly: roSet,
		Builtin:  map[string]bool{},
	}
	gate := core.NewPolicyGate(pol, confirmer)

	// Canonical ladder: gate.Check FIRST, then the extension intercept; the
	// recordCall correlation wrapper stays OUTSIDE so an extension tool's
	// permission request still carries the ACP toolCallId.
	ladder := func(call provider.ToolCallBlock) (bool, string, json.RawMessage) {
		ok, reason, _ := gate.Check(call.Name, call.Arguments, core.BuildPreview(call.Arguments, 120))
		if !ok {
			return false, reason, nil
		}
		res := extMgr.InterceptToolCall(ctx, call.ID, call.Name, call.Arguments)
		if res.Block {
			return false, res.Reason, nil
		}
		if res.ModifiedArgs != nil {
			return true, "", res.ModifiedArgs
		}
		return true, "", nil
	}
	ag.BeforeToolExecute = func(call provider.ToolCallBlock) (bool, string, json.RawMessage) {
		recordCall(call.ID)
		return ladder(call)
	}
	ag.BeforeTurn = func(step int) (bool, string) {
		res := extMgr.InterceptTurnStart(ctx, step)
		return !res.Block, res.Reason
	}
	ag.BeforeAssistantMessage = func(text string) (bool, string, string) {
		res := extMgr.InterceptAssistantMessage(ctx, text)
		if res.Block {
			return false, res.Reason, ""
		}
		return true, "", res.ReplaceText
	}
	ag.ContextProvider = extMgr.EphemeralContext

	// The event observer the acp package composes AFTER translateEvent: a
	// minimal fanout that drives the manager's event emit (the production
	// fanoutAgentEvent lives in the unimportable parent package; this mirrors
	// the events it forwards).
	observe := func(ev core.AgentEvent) { fanoutToExtensions(extMgr, ev) }
	cleanup := func() {
		atomic.AddInt32(&f.extCleanups, 1)
		extMgr.Stop(2 * time.Second)
	}
	return ag, gate, observe, cleanup, extMgr
}

// extCommandsFor mirrors the production acpExtCommands: it snapshots the
// manager's registered commands into the acp package's neutral ExtCommandInfo.
func extCommandsFor(extMgr *extensions.Manager) func() []ExtCommandInfo {
	if extMgr == nil {
		return nil
	}
	return func() []ExtCommandInfo {
		cmds := extMgr.Commands()
		if len(cmds) == 0 {
			return nil
		}
		out := make([]ExtCommandInfo, 0, len(cmds))
		for _, c := range cmds {
			out = append(out, ExtCommandInfo{Name: c.Name, Description: c.Description})
		}
		return out
	}
}

// invokeExtCommandFor mirrors the production acpInvokeExtCommand: it fires the
// command through the manager and maps the extproto response onto the acp
// package's neutral ExtCommandResult, folding the TUI-only insert/open_panel
// actions into a display payload exactly as the host does.
func invokeExtCommandFor(extMgr *extensions.Manager) func(context.Context, string, string) (ExtCommandResult, error) {
	if extMgr == nil {
		return nil
	}
	return func(ctx context.Context, name, args string) (ExtCommandResult, error) {
		resp, err := extMgr.Invoke(ctx, name, args, 30*time.Second)
		if err != nil {
			return ExtCommandResult{}, err
		}
		if resp.Error != "" {
			return ExtCommandResult{Action: ExtActionError, Error: resp.Error}, nil
		}
		switch resp.Action {
		case "prompt":
			return ExtCommandResult{Action: ExtActionPrompt, Prompt: resp.Prompt}, nil
		case "display":
			return ExtCommandResult{Action: ExtActionDisplay, Display: resp.Display}, nil
		case "insert":
			return ExtCommandResult{Action: ExtActionInsert, Display: resp.Insert}, nil
		case "open_panel":
			return ExtCommandResult{Action: ExtActionOpenPanel, Display: renderPanelTextForTest(resp.OpenPanel)}, nil
		case "noop", "":
			return ExtCommandResult{Action: ExtActionNoop}, nil
		default:
			return ExtCommandResult{Action: ExtActionNoop}, nil
		}
	}
}

// extContextFor mirrors the production acpExtContext: it snapshots the
// manager's live context contributions (static guidance + cards) into the acp
// package's neutral ContextItem, so the /context test drives the same boundary
// the host does.
func extContextFor(extMgr *extensions.Manager) func() []ContextItem {
	if extMgr == nil {
		return nil
	}
	return func() []ContextItem {
		snap := extMgr.ContextSnapshot()
		if len(snap) == 0 {
			return nil
		}
		out := make([]ContextItem, 0, len(snap))
		for _, it := range snap {
			out = append(out, ContextItem{Source: it.Source, Kind: it.Kind, Label: it.Label, Text: it.Text})
		}
		return out
	}
}

// reloadExtensionsForTest wraps the manager's Reload (mirroring the production
// acpReloadExtensions) and bumps a counter so the /reload-ext test can assert
// the reload closure actually ran. The mapping flattens []error to a count
// exactly as the host does.
func reloadExtensionsForTest(extMgr *extensions.Manager, ran *int32) func(context.Context) ReloadStats {
	if extMgr == nil {
		return nil
	}
	return func(ctx context.Context) ReloadStats {
		atomic.AddInt32(ran, 1)
		stats := extMgr.Reload(ctx, 2*time.Second)
		return ReloadStats{Stopped: stats.Stopped, Loaded: stats.Loaded, Ready: stats.Ready, Errors: len(stats.Errors)}
	}
}

// trustWorkspaceForTest mirrors the production acpTrustWorkspace at the acp
// boundary: it bumps a counter (and records the parent flag) so the /trust test
// can assert the closure actually ran and that `/trust parent` threaded the
// wider scope. It does not touch a real trust store — the production mapper's
// TrustPath + extMgr.Reload are covered by the tagged build/vet; the wire test
// only proves the command invokes the closure, emits the confirmation, ends the
// turn, and re-advertises the catalog.
func trustWorkspaceForTest(calls, parentFlag *int32) func(parent bool) error {
	return func(parent bool) error {
		atomic.AddInt32(calls, 1)
		if parent {
			atomic.StoreInt32(parentFlag, 1)
		} else {
			atomic.StoreInt32(parentFlag, 0)
		}
		return nil
	}
}

// untrustWorkspaceForTest is the symmetric inverse of trustWorkspaceForTest: it
// bumps a counter so the /untrust test can assert the closure ran.
func untrustWorkspaceForTest(calls *int32) func() error {
	return func() error {
		atomic.AddInt32(calls, 1)
		return nil
	}
}

// renderPanelTextForTest flattens a PanelSpec to text, mirroring the host's
// renderPanelText so the open_panel degradation surfaces the same content.
func renderPanelTextForTest(p *extproto.PanelSpec) string {
	if p == nil {
		return ""
	}
	var b strings.Builder
	if p.Title != "" {
		b.WriteString(p.Title)
		b.WriteString("\n")
	}
	for _, ln := range p.Lines {
		b.WriteString(ln)
		b.WriteString("\n")
	}
	if p.Footer != "" {
		b.WriteString(p.Footer)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// nonInteractiveExtHooksStub is the no-op HostHooks impl for the test
// extension manager — ACP has no interactive surface, mirroring the
// production nonInteractiveExtHooks.
type nonInteractiveExtHooksStub struct{}

func (nonInteractiveExtHooksStub) Notify(string, string, string)                        {}
func (nonInteractiveExtHooksStub) Submit(string)                                        {}
func (nonInteractiveExtHooksStub) SubmitSlash(string)                                   {}
func (nonInteractiveExtHooksStub) Insert(string)                                        {}
func (nonInteractiveExtHooksStub) Display(string, string)                               {}
func (nonInteractiveExtHooksStub) ClearNotes(string)                                    {}
func (nonInteractiveExtHooksStub) OpenPanel(string, extproto.PanelSpec)                 {}
func (nonInteractiveExtHooksStub) UpdatePanel(string, string, string, []string, string) {}
func (nonInteractiveExtHooksStub) ClosePanel(string, string)                            {}
func (nonInteractiveExtHooksStub) RefreshStatus()                                       {}

// manifestPermissionRules reads every installed extension's extension.json
// under root/extensions and compiles its `permissions` array into
// core.PermissionRule values, mirroring how the production
// buildPermissionPolicy -> extensionPermissionRules path turns an extension's
// manifest `ask`/`deny` contribution into a gate rule. Only the test's writer
// tool carries one (decision "ask"), so the gate prompts for it.
func manifestPermissionRules(root string) []core.PermissionRule {
	var rules []core.PermissionRule
	extRoot := filepath.Join(root, "extensions")
	entries, err := os.ReadDir(extRoot)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		mb, err := os.ReadFile(filepath.Join(extRoot, e.Name(), "extension.json"))
		if err != nil {
			continue
		}
		var m struct {
			Name        string `json:"name"`
			Permissions []struct {
				Tool     string `json:"tool"`
				Decision string `json:"decision"`
				Reason   string `json:"reason"`
			} `json:"permissions"`
		}
		if json.Unmarshal(mb, &m) != nil {
			continue
		}
		for _, p := range m.Permissions {
			dec := core.RuleDecision(p.Decision)
			// Like extensions, a manifest may only restrict (ask/deny), never
			// grant — drop an allow exactly as compilePermissionRules does for
			// a restrict-only layer.
			if dec == core.RuleAllow {
				continue
			}
			rules = append(rules, core.PermissionRule{
				Tool:     p.Tool,
				Decision: dec,
				Reason:   p.Reason,
				Source:   "extension " + m.Name,
			})
		}
	}
	return rules
}

// fanoutToExtensions mirrors the parent package's fanoutAgentEvent for the
// events that have a clear extension-facing meaning, driving the manager's
// per-extension event stream. Used as the ObserveEvent the acp package composes
// AFTER translateEvent, so this test proves the fanout coexists with the
// session/update translation without breaking it.
func fanoutToExtensions(mgr *extensions.Manager, ev core.AgentEvent) {
	if mgr == nil {
		return
	}
	switch e := ev.(type) {
	case core.EvTurnStart:
		mgr.EmitEvent(extproto.EventFromHost{Event: "turn_start", Step: e.Step})
	case core.EvToolCall:
		mgr.EmitEvent(extproto.EventFromHost{Event: "tool_call", ToolID: e.ID, ToolName: e.Name, ToolArgs: e.Args})
	case core.EvToolResult:
		mgr.EmitEvent(extproto.EventFromHost{Event: "tool_result", ToolID: e.ID, IsError: e.Result.IsError})
	case core.EvTurnEnd:
		mgr.EmitEvent(extproto.EventFromHost{Event: "turn_end", Stop: string(e.Stop)})
	}
}

// newDurableSession creates the session backing this ACP session. With a root
// set it is a real on-disk session under root (so session/list + session/load
// see it); otherwise a session at a throwaway temp path. Persistence hooks are
// wired in both cases so OnMessageAppended writes the transcript.
func (f *fakeFactory) newDurableSession(cwd string) (*core.Session, error) {
	if f.root != "" {
		return core.NewSession(f.root, cwd, "fake", "fake-model", "test")
	}
	dir, err := os.MkdirTemp("", "acp-sess-")
	if err != nil {
		return nil, err
	}
	return core.NewSessionAtPath(filepath.Join(dir, "s.jsonl"), cwd, "fake", "fake-model", "test")
}

func wireFakePersist(ag *core.Agent, sess *core.Session) {
	ag.OnMessageAppended = func(m provider.Message) { _ = sess.AppendMessage(m) }
	ag.OnUsage = func(cum provider.Usage) { _ = sess.AppendUsage(cum, cum) }
	ag.OnTranscriptCompacted = func(msgs []provider.Message) { _ = sess.AppendCompaction(msgs) }
}

func (f *fakeFactory) sessionModel() (prov, model string) {
	model = f.model
	if model == "" {
		model = "fake-model"
	}
	return "fake", model
}

func (f *fakeFactory) NewSessionAgent(ctx context.Context, cwd string, mcpServers json.RawMessage, confirmer core.Confirmer, recordCall func(string)) (SessionAgent, error) {
	// Extension path: build a real extensions.Manager + the ext-aware agent,
	// exactly mirroring the production buildAgent extension wiring.
	if f.extRoot != "" {
		ag, gate, observe, cleanup, extMgr := f.buildExtensionAgent(ctx, cwd, confirmer, recordCall)
		sess, err := f.newDurableSession(cwd)
		if err != nil {
			cleanup()
			return SessionAgent{}, err
		}
		wireFakePersist(ag, sess)
		f.loadedMu.Lock()
		f.newAgent = ag
		f.loadedMu.Unlock()
		prov, model := f.sessionModel()
		return SessionAgent{
			Agent:            ag,
			Session:          sess,
			Cleanup:          cleanup,
			Gate:             gate,
			Provider:         prov,
			Model:            model,
			Sandbox:          f.sandbox,
			Skills:           f.skillSnapshot(),
			ObserveEvent:     observe,
			ExtCommands:      extCommandsFor(extMgr),
			InvokeExtCommand: invokeExtCommandFor(extMgr),
			ExtContext:       extContextFor(extMgr),
			ReloadExtensions: reloadExtensionsForTest(extMgr, &f.extReloads),
			TrustWorkspace:   f.trustWorkspaceFn(),
			UntrustWorkspace: f.untrustWorkspaceFn(),
		}, nil
	}

	reg, cleanup := f.startMCP(ctx, mcpServers)
	ag, gate := f.buildFakeAgentWithRegistry(reg, confirmer, recordCall)
	sess, err := f.newDurableSession(cwd)
	if err != nil {
		cleanup()
		return SessionAgent{}, err
	}
	wireFakePersist(ag, sess)
	f.loadedMu.Lock()
	f.newAgent = ag
	f.loadedMu.Unlock()
	prov, model := f.sessionModel()
	return SessionAgent{
		Agent:            ag,
		Session:          sess,
		Cleanup:          cleanup,
		Gate:             gate,
		Provider:         prov,
		Model:            model,
		Sandbox:          f.sandbox,
		Skills:           f.skillSnapshot(),
		ExtContext:       f.emptyExtContextFunc(),
		TrustWorkspace:   f.trustWorkspaceFn(),
		UntrustWorkspace: f.untrustWorkspaceFn(),
	}, nil
}

// emptyExtContextFunc returns a non-nil ExtContext that yields no items when
// emptyExtContext is set (the "manager wired, nothing injected" path), else nil
// (extensions disabled). Lets the /context graceful tests distinguish the two
// notes without a real extension.
func (f *fakeFactory) emptyExtContextFunc() func() []ContextItem {
	if !f.emptyExtContext {
		return nil
	}
	return func() []ContextItem { return nil }
}

func (f *fakeFactory) LoadSessionAgent(ctx context.Context, sessionPath, cwd string, mcpServers json.RawMessage, confirmer core.Confirmer, recordCall func(string)) (SessionAgent, []provider.Message, error) {
	sess, msgs, err := core.OpenSession(sessionPath)
	if err != nil {
		return SessionAgent{}, nil, err
	}
	if f.extRoot != "" {
		ag, gate, observe, cleanup, extMgr := f.buildExtensionAgent(ctx, cwd, confirmer, recordCall)
		wireFakePersist(ag, sess)
		f.loadedMu.Lock()
		f.loadedAgent = ag
		f.loadedMu.Unlock()
		prov, model := f.sessionModel()
		if sess.Meta.Provider != "" {
			prov = sess.Meta.Provider
		}
		if sess.Meta.Model != "" {
			model = sess.Meta.Model
		}
		return SessionAgent{
			Agent:            ag,
			Session:          sess,
			Cleanup:          cleanup,
			Gate:             gate,
			Provider:         prov,
			Model:            model,
			Sandbox:          f.sandbox,
			Skills:           f.skillSnapshot(),
			ObserveEvent:     observe,
			ExtCommands:      extCommandsFor(extMgr),
			InvokeExtCommand: invokeExtCommandFor(extMgr),
			ExtContext:       extContextFor(extMgr),
			ReloadExtensions: reloadExtensionsForTest(extMgr, &f.extReloads),
			TrustWorkspace:   f.trustWorkspaceFn(),
			UntrustWorkspace: f.untrustWorkspaceFn(),
		}, msgs, nil
	}
	reg, cleanup := f.startMCP(ctx, mcpServers)
	ag, gate := f.buildFakeAgentWithRegistry(reg, confirmer, recordCall)
	wireFakePersist(ag, sess)
	_ = cwd
	f.loadedMu.Lock()
	f.loadedAgent = ag
	f.loadedMu.Unlock()
	prov, model := f.sessionModel()
	// Restore a persisted model switch the way the production factory does.
	if sess.Meta.Provider != "" {
		prov = sess.Meta.Provider
	}
	if sess.Meta.Model != "" {
		model = sess.Meta.Model
	}
	return SessionAgent{
		Agent:            ag,
		Session:          sess,
		Cleanup:          cleanup,
		Gate:             gate,
		Provider:         prov,
		Model:            model,
		Sandbox:          f.sandbox,
		Skills:           f.skillSnapshot(),
		TrustWorkspace:   f.trustWorkspaceFn(),
		UntrustWorkspace: f.untrustWorkspaceFn(),
	}, msgs, nil
}

// ModelOptions returns the seeded authenticated-provider model menu.
func (f *fakeFactory) ModelOptions() []ModelOption { return f.models }

// SwitchModel resolves a model switch against the seeded `models` menu. An
// unknown model id is an error (mirrors the production -32602 mapping). A model
// whose provider matches the current provider AND has no distinct switchClient
// reuses the existing client (Reuse=true); otherwise it returns the seeded
// switchClient (or a fresh fake) with Reuse=false (cross-provider, transcript
// preserved).
func (f *fakeFactory) SwitchModel(currentProvider, currentModel, targetModelID string) (ModelSwitch, error) {
	var target *ModelOption
	for i := range f.models {
		if f.models[i].ID == targetModelID {
			target = &f.models[i]
			break
		}
	}
	if target == nil {
		return ModelSwitch{}, fmt.Errorf("unknown model %q", targetModelID)
	}

	var sw ModelSwitch
	if target.Provider == currentProvider && f.switchClient == nil {
		sw = ModelSwitch{Provider: target.Provider, Model: target.ID, Reuse: true}
	} else {
		client := f.switchClient
		if client == nil {
			client = &textTurnClient{reply: "switched"}
		}
		sw = ModelSwitch{Provider: target.Provider, Model: target.ID, Client: client, Reuse: false}
	}

	f.switchMu.Lock()
	f.lastSwitch = sw
	f.switchCalls++
	f.switchMu.Unlock()
	return sw, nil
}

// startMCP mirrors the production acpFactory.setupACPMCP for the test factory:
// it parses the editor-provided mcpServers, starts the stdio servers (when
// f.mcpEnabled), and returns the registry the agent should use (the base tools
// plus the namespaced MCP tools) and a stop func (never nil). When MCP is
// disabled or no servers are sent, the base registry and a no-op stop are
// returned, so every existing test keeps its old behavior.
func (f *fakeFactory) startMCP(ctx context.Context, mcpServers json.RawMessage) (core.Registry, func()) {
	base := f.tools
	if base == nil {
		base = core.Registry{"edit": editFileTool{}}
	}
	if !f.mcpEnabled {
		return base, func() {}
	}
	servers, _ := ParseMCPServers(mcpServers)
	if len(servers) == 0 {
		return base, func() {}
	}
	cfg := &mcp.Config{Servers: make(map[string]mcp.ServerConfig, len(servers))}
	for _, s := range servers {
		cfg.Servers[s.Name] = mcp.ServerConfig{Command: s.Command, Args: s.Args, Env: s.Env}
	}
	mgr := mcp.StartAll(ctx, cfg, nil)
	// Merge the namespaced MCP tools onto a copy of the base registry so the
	// agent (and the test) see mcp_<server>_<tool> alongside the fakes.
	reg := core.Registry{}
	for k, v := range base {
		reg[k] = v
	}
	for _, ti := range mgr.Tools() {
		reg[ti.Name] = mgr.NewTool(ti)
	}
	f.mcpMu.Lock()
	f.lastMCPTools = reg
	f.mcpMu.Unlock()
	return reg, mgr.StopAll
}

// lastRegistry returns the registry built by the most recent startMCP call
// (the integration test inspects it to assert the MCP tool was registered).
func (f *fakeFactory) lastRegistry() core.Registry {
	f.mcpMu.Lock()
	defer f.mcpMu.Unlock()
	return f.lastMCPTools
}

func (f *fakeFactory) ListSessions(cwd string) []SessionInfo {
	if f.root == "" || cwd == "" {
		return nil
	}
	summaries := core.DescribeSessions(f.root, cwd)
	out := make([]SessionInfo, 0, len(summaries))
	for _, s := range summaries {
		if s.MessageCount == 0 {
			continue
		}
		title := s.Title
		if title == "" {
			title = s.FirstUserText
		}
		out = append(out, SessionInfo{
			SessionID: s.Path,
			CWD:       cwd,
			Title:     title,
		})
	}
	return out
}

// ---- client-side JSON-RPC harness over the io.Pipe pair ----

type harness struct {
	t       *testing.T
	enc     *json.Encoder
	dec     *json.Decoder
	nextID  int
	updates chan map[string]any // session/update notifications
}

func newHarness(t *testing.T, toAgent io.Writer, fromAgent io.Reader) *harness {
	h := &harness{
		t:       t,
		enc:     json.NewEncoder(toAgent),
		dec:     json.NewDecoder(fromAgent),
		updates: make(chan map[string]any, 64),
	}
	return h
}

// call sends a request and returns the matching response result. session/update
// notifications that arrive while waiting are buffered onto h.updates.
func (h *harness) call(method string, params any) map[string]any {
	h.t.Helper()
	h.nextID++
	id := h.nextID
	if err := h.enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		h.t.Fatalf("encode %s: %v", method, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out awaiting response to %s", method)
		}
		var msg map[string]any
		if err := h.dec.Decode(&msg); err != nil {
			h.t.Fatalf("decode while awaiting %s: %v", method, err)
		}
		if m, ok := msg["method"].(string); ok && m == MethodSessionUpdate {
			if p, ok := msg["params"].(map[string]any); ok {
				h.updates <- p
			}
			continue
		}
		// A response: match the id.
		if rid, ok := msg["id"].(float64); ok && int(rid) == id {
			if e, ok := msg["error"].(map[string]any); ok {
				h.t.Fatalf("%s returned error: %v", method, e)
			}
			if r, ok := msg["result"].(map[string]any); ok {
				return r
			}
			return map[string]any{}
		}
	}
}

// ---- Phase 2 helpers: async requests + the permission round-trip ----

// send encodes a request without waiting for its response and returns the
// request id, so a test can drive a blocking session/prompt and still
// service the session/request_permission it triggers mid-flight.
func (h *harness) send(method string, params any) int {
	h.t.Helper()
	h.nextID++
	id := h.nextID
	if err := h.enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		h.t.Fatalf("encode %s: %v", method, err)
	}
	return id
}

// notify sends a one-way notification (no id), e.g. session/cancel.
func (h *harness) notify(method string, params any) {
	h.t.Helper()
	if err := h.enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	}); err != nil {
		h.t.Fatalf("encode notify %s: %v", method, err)
	}
}

// reply answers an agent->client request (e.g. session/request_permission).
func (h *harness) reply(id any, result any) {
	h.t.Helper()
	if err := h.enc.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	}); err != nil {
		h.t.Fatalf("encode reply: %v", err)
	}
}

// frame is a decoded inbound message classified for the test reader.
type frame struct {
	method string
	id     any
	params map[string]any
	result map[string]any
	errObj map[string]any
}

// read decodes one frame, buffering session/update notifications onto
// h.updates so a caller scanning for a specific message never drops the
// narration stream.
func (h *harness) read() frame {
	h.t.Helper()
	for {
		var msg map[string]any
		if err := h.dec.Decode(&msg); err != nil {
			h.t.Fatalf("decode: %v", err)
		}
		f := frame{id: msg["id"]}
		if m, ok := msg["method"].(string); ok {
			f.method = m
			if p, ok := msg["params"].(map[string]any); ok {
				f.params = p
			}
			if m == MethodSessionUpdate {
				h.updates <- f.params
				continue
			}
			return f
		}
		if r, ok := msg["result"].(map[string]any); ok {
			f.result = r
		}
		if e, ok := msg["error"].(map[string]any); ok {
			f.errObj = e
		}
		return f
	}
}

// awaitPermission reads frames (buffering updates) until the agent issues a
// session/request_permission, then returns its id and the toolCallId it
// correlates to. Fails if a different request or the unexpected end of the
// stream arrives first.
func (h *harness) awaitPermission() (id any, toolCallID string) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			h.t.Fatal("timed out awaiting session/request_permission")
		}
		f := h.read()
		if f.method == MethodSessionRequestPermission {
			tc, _ := f.params["toolCall"].(map[string]any)
			id := f.id
			tcid, _ := tc["toolCallId"].(string)
			return id, tcid
		}
		// Ignore any other inbound request/response while we wait.
	}
}

// awaitResponse reads frames (buffering updates, and auto-handling any
// permission request via permHandler when non-nil) until the response to
// reqID arrives, then returns its result. permHandler returns the outcome
// map to reply with; a nil handler leaves permission requests unanswered
// (used by the cancel test, where session/cancel resolves them instead).
func (h *harness) awaitResponse(reqID int, permHandler func(toolCallID string) map[string]any) map[string]any {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out awaiting response to request %d", reqID)
		}
		f := h.read()
		if f.method == MethodSessionRequestPermission {
			if permHandler != nil {
				tc, _ := f.params["toolCall"].(map[string]any)
				tcid, _ := tc["toolCallId"].(string)
				h.reply(f.id, map[string]any{"outcome": permHandler(tcid)})
			}
			continue
		}
		if rid, ok := f.id.(float64); ok && int(rid) == reqID {
			if f.errObj != nil {
				h.t.Fatalf("request %d returned error: %v", reqID, f.errObj)
			}
			if f.result != nil {
				return f.result
			}
			return map[string]any{}
		}
	}
}

// drainUpdates collects every buffered session/update (those interleaved
// before responses). Call after the prompt response arrives.
func (h *harness) drainUpdates() []map[string]any {
	var out []map[string]any
	for {
		select {
		case u := <-h.updates:
			out = append(out, u)
		default:
			return out
		}
	}
}

// expectUpdate returns the next session/update params. h.call stops at a
// response, so a notification emitted AFTER a response — available_commands_update
// follows the session/new response (Zed must see the sessionId first) — is left
// unread in the pipe; this pulls it. An already-buffered update is returned
// first.
func (h *harness) expectUpdate() map[string]any {
	h.t.Helper()
	select {
	case u := <-h.updates:
		return u
	default:
	}
	for {
		var msg map[string]any
		if err := h.dec.Decode(&msg); err != nil {
			h.t.Fatalf("decode awaiting session/update: %v", err)
		}
		if m, _ := msg["method"].(string); m == MethodSessionUpdate {
			p, _ := msg["params"].(map[string]any)
			return p
		}
	}
}

func TestACPWireHandshakeAndToolTurn(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(target, []byte("old contents\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	client := &toolTurnClient{
		toolName: "edit",
		toolArgs: `{"path":"` + jsonEscape(target) + `","content":"new contents\n"}`,
	}
	factory := &fakeFactory{client: client}

	// Two pipes: clientToAgent (client writes, agent reads) and
	// agentToClient (agent writes, client reads).
	caR, caW := io.Pipe()
	acR, acW := io.Pipe()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- Serve(ctx, caR, acW, factory, AgentInfo{Name: "terva", Version: "test"})
	}()

	h := newHarness(t, caW, acR)

	// ---- initialize (MUST be first) ----
	initRes := h.call(MethodInitialize, map[string]any{
		"protocolVersion": 1,
		"clientInfo":      map[string]any{"name": "test-client", "version": "0.0.0"},
	})
	if pv, ok := initRes["protocolVersion"].(float64); !ok || int(pv) != 1 {
		t.Fatalf("negotiated protocolVersion = %v; want 1", initRes["protocolVersion"])
	}
	caps, _ := initRes["agentCapabilities"].(map[string]any)
	if caps == nil {
		t.Fatal("initialize result missing agentCapabilities")
	}
	// Phase 3: loadSession is now backed and advertised true (capability
	// honesty — §13).
	if ls, _ := caps["loadSession"].(bool); !ls {
		t.Error("loadSession advertised false; Phase 3 backs session/load")
	}
	// sessionCapabilities.list is advertised as the empty object {} (presence
	// = supported).
	if sc, _ := caps["sessionCapabilities"].(map[string]any); sc == nil {
		t.Error("missing sessionCapabilities")
	} else if _, ok := sc["list"]; !ok {
		t.Error("sessionCapabilities.list not advertised; Phase 3 backs session/list")
	}
	pc, _ := caps["promptCapabilities"].(map[string]any)
	if img, _ := pc["image"].(bool); !img {
		t.Error("promptCapabilities.image should be true")
	}

	// ---- session/new ----
	newRes := h.call(MethodSessionNew, map[string]any{"cwd": dir})
	sid, _ := newRes["sessionId"].(string)
	if sid == "" {
		t.Fatal("session/new returned empty sessionId")
	}

	// ---- session/prompt (a tool turn) ----
	promptRes := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "please edit"}},
	})
	if sr, _ := promptRes["stopReason"].(string); sr != StopEndTurn {
		t.Fatalf("stopReason = %q; want %q", promptRes["stopReason"], StopEndTurn)
	}

	// ---- assert the session/update stream ----
	updates := h.drainUpdates()
	var sawMsgChunk, sawToolCall, sawToolUpdate, sawDiff bool
	for _, u := range updates {
		upd, _ := u["update"].(map[string]any)
		if upd == nil {
			continue
		}
		switch upd["sessionUpdate"] {
		case UpdateAgentMessageChunk:
			sawMsgChunk = true
		case UpdateToolCall:
			sawToolCall = true
			if upd["toolCallId"] != "call-1" {
				t.Errorf("tool_call toolCallId = %v; want call-1", upd["toolCallId"])
			}
			if upd["kind"] != ToolKindEdit {
				t.Errorf("tool_call kind = %v; want edit", upd["kind"])
			}
			if upd["status"] != ToolStatusPending {
				t.Errorf("tool_call status = %v; want pending", upd["status"])
			}
		case UpdateToolCallUpdate:
			sawToolUpdate = true
			if content, ok := upd["content"].([]any); ok {
				for _, ci := range content {
					if cm, ok := ci.(map[string]any); ok && cm["type"] == ToolCallContentDiff {
						sawDiff = true
						if cm["newText"] != "new contents\n" {
							t.Errorf("diff newText = %q; want new contents", cm["newText"])
						}
						if cm["oldText"] != "old contents\n" {
							t.Errorf("diff oldText = %q; want old contents", cm["oldText"])
						}
					}
				}
			}
		}
	}
	if !sawMsgChunk {
		t.Error("no agent_message_chunk in the session/update stream")
	}
	if !sawToolCall {
		t.Error("no tool_call in the session/update stream")
	}
	if !sawToolUpdate {
		t.Error("no tool_call_update in the session/update stream")
	}
	if !sawDiff {
		t.Error("no diff content block on the edit tool_call_update")
	}

	// Tear down the connection so Serve returns.
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

// TestACPInitializeMustBeFirst proves a method before initialize is rejected
// with a JSON-RPC error (the §13 initialize-first MUST).
func TestACPInitializeMustBeFirst(t *testing.T) {
	factory := &fakeFactory{client: &toolTurnClient{toolName: "edit", toolArgs: "{}"}}
	caR, caW := io.Pipe()
	acR, acW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Serve(ctx, caR, acW, factory, AgentInfo{Name: "terva", Version: "test"}) }()

	enc := json.NewEncoder(caW)
	dec := json.NewDecoder(acR)
	if err := enc.Encode(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": MethodSessionNew, "params": map[string]any{"cwd": t.TempDir()},
	}); err != nil {
		t.Fatal(err)
	}
	var resp map[string]any
	if err := dec.Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	e, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error before initialize; got %v", resp)
	}
	if code, _ := e["code"].(float64); int(code) != CodeInvalidRequest {
		t.Errorf("error code = %v; want %d (invalid request)", e["code"], CodeInvalidRequest)
	}
	cancel()
	_ = caW.Close()
	_ = acW.Close()
}

// TestACPMethodNotFound proves an unknown method returns -32601.
func TestACPMethodNotFound(t *testing.T) {
	factory := &fakeFactory{client: &toolTurnClient{toolName: "edit", toolArgs: "{}"}}
	caR, caW := io.Pipe()
	acR, acW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Serve(ctx, caR, acW, factory, AgentInfo{Name: "terva", Version: "test"}) }()

	h := newHarness(t, caW, acR)
	h.call(MethodInitialize, map[string]any{"protocolVersion": 1})

	// Raw call to a bogus method, asserting the error code.
	enc := json.NewEncoder(caW)
	dec := json.NewDecoder(acR)
	if err := enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 99, "method": "bogus/method"}); err != nil {
		t.Fatal(err)
	}
	for {
		var resp map[string]any
		if err := dec.Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if m, ok := resp["method"].(string); ok && m == MethodSessionUpdate {
			continue
		}
		if rid, _ := resp["id"].(float64); int(rid) == 99 {
			e, ok := resp["error"].(map[string]any)
			if !ok {
				t.Fatalf("expected method-not-found error; got %v", resp)
			}
			if code, _ := e["code"].(float64); int(code) != CodeMethodNotFound {
				t.Errorf("error code = %v; want %d", e["code"], CodeMethodNotFound)
			}
			break
		}
	}
	cancel()
	_ = caW.Close()
	_ = acW.Close()
}

// ---- Phase 2: tool-permission round-trip + cancellation ----

// permSetup spins up Serve over an io.Pipe pair with the given factory,
// runs initialize + session/new, and returns the harness, the sessionId,
// and a teardown func. Shared by the permission/cancel tests.
func permSetup(t *testing.T, factory acpFactoryIface) (*harness, string, func()) {
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

// acpFactoryIface is the local alias for the package AgentFactory so the
// helper signature reads cleanly.
type acpFactoryIface = AgentFactory

// TestACPPermissionAllowRunsTool: an "ask" tool turn triggers
// session/request_permission; replying selected=allow_once lets the tool
// run and the turn completes with end_turn (§8, verification (a)).
func TestACPPermissionAllowRunsTool(t *testing.T) {
	tool := &countingTool{name: "do_thing"}
	client := &multiToolClient{toolName: "do_thing", toolArgs: "{}", toolTurns: 1}
	factory := &fakeFactory{
		client:  client,
		tools:   core.Registry{"do_thing": tool},
		askTool: "do_thing",
	}
	h, sid, teardown := permSetup(t, factory)
	defer teardown()

	reqID := h.send(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "go"}},
	})

	var sawCorrelatedToolCallID string
	res := h.awaitResponse(reqID, func(toolCallID string) map[string]any {
		sawCorrelatedToolCallID = toolCallID
		return map[string]any{"outcome": PermOutcomeSelected, "optionId": PermAllowOnce}
	})

	if sawCorrelatedToolCallID != "call-1" {
		t.Errorf("request_permission toolCallId = %q; want call-1 (§13 correlation)", sawCorrelatedToolCallID)
	}
	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("stopReason = %v; want %q", res["stopReason"], StopEndTurn)
	}
	if got := atomic.LoadInt32(&tool.runs); got != 1 {
		t.Errorf("tool ran %d times; want 1 (allow should execute it)", got)
	}
}

// TestACPPermissionRejectRefusesTool: replying with a reject option refuses
// the tool (it does not run; the model sees a refusal) and the turn still
// completes (§8, verification (b)).
func TestACPPermissionRejectRefusesTool(t *testing.T) {
	tool := &countingTool{name: "do_thing"}
	client := &multiToolClient{toolName: "do_thing", toolArgs: "{}", toolTurns: 1}
	factory := &fakeFactory{
		client:  client,
		tools:   core.Registry{"do_thing": tool},
		askTool: "do_thing",
	}
	h, sid, teardown := permSetup(t, factory)
	defer teardown()

	reqID := h.send(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "go"}},
	})

	res := h.awaitResponse(reqID, func(string) map[string]any {
		return map[string]any{"outcome": PermOutcomeSelected, "optionId": PermRejectOnce}
	})

	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("stopReason = %v; want %q (turn still completes after refusal)", res["stopReason"], StopEndTurn)
	}
	if got := atomic.LoadInt32(&tool.runs); got != 0 {
		t.Errorf("tool ran %d times; want 0 (reject must block execution)", got)
	}
	// The refusal must reach the model as a failed tool_call_update.
	var sawFailed bool
	for _, u := range h.drainUpdates() {
		upd, _ := u["update"].(map[string]any)
		if upd["sessionUpdate"] == UpdateToolCallUpdate && upd["status"] == ToolStatusFailed {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Error("no failed tool_call_update after reject (model should see a refusal)")
	}
}

// TestACPPermissionAllowAlwaysSkipsSecondPrompt: replying allow_always to
// the first call means a second call to the same tool does NOT prompt again
// — exactly one session/request_permission for two tool calls (§8,
// verification (c)).
func TestACPPermissionAllowAlwaysSkipsSecondPrompt(t *testing.T) {
	tool := &countingTool{name: "do_thing"}
	client := &multiToolClient{toolName: "do_thing", toolArgs: "{}", toolTurns: 2}
	factory := &fakeFactory{
		client:  client,
		tools:   core.Registry{"do_thing": tool},
		askTool: "do_thing",
	}
	h, sid, teardown := permSetup(t, factory)
	defer teardown()

	reqID := h.send(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "go twice"}},
	})

	var permCount int32
	res := h.awaitResponse(reqID, func(string) map[string]any {
		atomic.AddInt32(&permCount, 1)
		return map[string]any{"outcome": PermOutcomeSelected, "optionId": PermAllowAlways}
	})

	if sr, _ := res["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("stopReason = %v; want %q", res["stopReason"], StopEndTurn)
	}
	if got := atomic.LoadInt32(&permCount); got != 1 {
		t.Errorf("session/request_permission issued %d times; want 1 (allow_always must not re-prompt)", got)
	}
	if got := atomic.LoadInt32(&tool.runs); got != 2 {
		t.Errorf("tool ran %d times; want 2 (both calls should execute)", got)
	}
}

// TestACPCancelResolvesPendingPermission: session/cancel mid-turn while a
// permission is outstanding resolves that permission cancelled and the
// prompt resolves stopReason "cancelled" (§8/§13, verification (d)).
func TestACPCancelResolvesPendingPermission(t *testing.T) {
	tool := &countingTool{name: "do_thing"}
	client := &multiToolClient{toolName: "do_thing", toolArgs: "{}", toolTurns: 1}
	factory := &fakeFactory{
		client:  client,
		tools:   core.Registry{"do_thing": tool},
		askTool: "do_thing",
	}
	h, sid, teardown := permSetup(t, factory)
	defer teardown()

	reqID := h.send(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "go"}},
	})

	// Wait until the agent is blocked on the permission round-trip, then
	// cancel WITHOUT answering it. Cancellation must unblock the confirmer
	// (cancelled verdict) so the prompt resolves.
	permID, tcid := h.awaitPermission()
	if tcid != "call-1" {
		t.Errorf("outstanding permission toolCallId = %q; want call-1", tcid)
	}
	h.notify(MethodSessionCancel, map[string]any{"sessionId": sid})

	// The prompt must now resolve with cancelled. We pass a nil permHandler
	// so the outstanding permission is intentionally left unanswered by the
	// client — cancellation, not a client reply, must end the turn.
	res := h.awaitResponse(reqID, nil)
	if sr, _ := res["stopReason"].(string); sr != StopCancelled {
		t.Errorf("stopReason = %v; want %q after cancel", res["stopReason"], StopCancelled)
	}
	if got := atomic.LoadInt32(&tool.runs); got != 0 {
		t.Errorf("tool ran %d times; want 0 (cancel before approval)", got)
	}

	// Tolerate a late client reply to the cancelled permission (no panic /
	// deadlock): the pending entry is already gone, so deliver is a no-op.
	h.reply(permID, map[string]any{"outcome": map[string]any{"outcome": PermOutcomeCancelled}})
}

// TestACPOneTurnAtATime: a second session/prompt issued while the first is
// in flight is serialized rather than run concurrently (requirement #3). We
// block the first turn on its permission, send the second prompt, then prove
// the second turn has NOT started while the first is blocked — its tool has
// not run and no second permission has been requested. Only after the first
// permission is answered (and the first turn completes) does the second turn
// run. Both prompts resolve with their own stopReason and the tool runs
// exactly twice (once per serialized prompt), with no deadlock.
func TestACPOneTurnAtATime(t *testing.T) {
	toolA := &countingTool{name: "do_thing"}
	// One client serves both prompts: it calls the tool on odd turns and
	// finishes on even turns, so each top-level prompt is one tool turn then
	// a text turn (prompt 1 -> turns 1,2; prompt 2 -> turns 3,4).
	client := &oddToolClient{toolName: "do_thing"}
	factory := &fakeFactory{
		client:  client,
		tools:   core.Registry{"do_thing": toolA},
		askTool: "do_thing",
	}
	h, sid, teardown := permSetup(t, factory)
	defer teardown()

	req1 := h.send(MethodSessionPromptName, map[string]any{
		"sessionId": sid, "prompt": []map[string]any{{"type": "text", "text": "first"}},
	})

	// First turn is now blocked on its permission (and has not run the tool).
	perm1, _ := h.awaitPermission()

	// Issue the second prompt while the first is blocked. It must wait on the
	// session turn mutex (the core turn does not overlap), so the client's
	// turn counter must NOT advance to the second prompt's turns yet.
	req2 := h.send(MethodSessionPromptName, map[string]any{
		"sessionId": sid, "prompt": []map[string]any{{"type": "text", "text": "second"}},
	})

	// Let the second prompt goroutine reach the turn mutex, then prove the
	// second turn has not started: the client has not produced any turn
	// beyond prompt 1's first (only one call so far), so no second tool has
	// run and the tool count is still 0 (the first tool is gated, unrun).
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&toolA.runs); got != 0 {
		t.Errorf("tool ran %d times while prompt 1 was blocked; want 0 (no overlap)", got)
	}
	if got := atomic.LoadInt32(&client.calls); got != 1 {
		t.Errorf("client issued %d turns while prompt 1 was blocked; want 1 (second prompt must wait)", got)
	}

	// Answer prompt 1's permission and resolve both prompts in order.
	h.reply(perm1, map[string]any{"outcome": map[string]any{"outcome": PermOutcomeSelected, "optionId": PermAllowOnce}})

	allow := func(string) map[string]any {
		return map[string]any{"outcome": PermOutcomeSelected, "optionId": PermAllowOnce}
	}
	res1 := h.awaitResponse(req1, allow)
	if sr, _ := res1["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("prompt 1 stopReason = %v; want %q", res1["stopReason"], StopEndTurn)
	}
	res2 := h.awaitResponse(req2, allow)
	if sr, _ := res2["stopReason"].(string); sr != StopEndTurn {
		t.Errorf("prompt 2 stopReason = %v; want %q", res2["stopReason"], StopEndTurn)
	}
	if got := atomic.LoadInt32(&toolA.runs); got != 2 {
		t.Errorf("tool ran %d times; want 2 (once per serialized prompt)", got)
	}
}

// oddToolClient calls its tool on odd-numbered turns and finishes on
// even-numbered turns, so each top-level prompt is exactly one tool turn
// followed by a text turn — letting one client serve two serialized prompts.
type oddToolClient struct {
	calls    int32
	toolName string
}

func (c *oddToolClient) Name() string { return "fake-acp-odd" }

func (c *oddToolClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	call := int(atomic.AddInt32(&c.calls, 1))
	out := make(chan provider.Event, 8)
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "fake-acp-odd", Model: req.Model}
		if call%2 == 1 {
			out <- provider.EventDone{
				Stop: provider.StopToolUse,
				Message: provider.Message{
					Role: provider.RoleAssistant,
					Content: []provider.Content{
						provider.ToolCallBlock{ID: "call-" + strconv.Itoa(call), Name: c.toolName, Arguments: json.RawMessage("{}")},
					},
				},
			}
			return
		}
		out <- provider.EventTextDelta{Delta: "done"}
		out <- provider.EventDone{
			Stop:    provider.StopEnd,
			Message: provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: "done"}}},
		}
	}()
	return out, nil
}

// jsonEscape escapes backslashes (Windows paths) for embedding a path in a
// hand-built JSON string literal.
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	// strip the surrounding quotes json.Marshal adds
	return string(b[1 : len(b)-1])
}

// ---- Phase 3: durable sessions, session/list, session/load ----

// readAny decodes one inbound frame WITHOUT buffering session/update
// notifications away — every frame is returned in arrival order, so a test can
// assert the relative ordering of replay updates and the load response (the
// §13 "replay before response" MUST).
func (h *harness) readAny() frame {
	h.t.Helper()
	var msg map[string]any
	if err := h.dec.Decode(&msg); err != nil {
		h.t.Fatalf("decode: %v", err)
	}
	f := frame{id: msg["id"]}
	if m, ok := msg["method"].(string); ok {
		f.method = m
		if p, ok := msg["params"].(map[string]any); ok {
			f.params = p
		}
		return f
	}
	if r, ok := msg["result"].(map[string]any); ok {
		f.result = r
	}
	if e, ok := msg["error"].(map[string]any); ok {
		f.errObj = e
	}
	return f
}

// textTurnClient streams a single text turn (no tools): one delta, stop=end.
// Used by the durable-session/list/load tests, where we just need a real
// user+assistant exchange persisted to disk.
type textTurnClient struct {
	reply string
}

func (c *textTurnClient) Name() string { return "fake-acp-text" }

func (c *textTurnClient) Stream(_ context.Context, req provider.Request) (<-chan provider.Event, error) {
	out := make(chan provider.Event, 8)
	reply := c.reply
	if reply == "" {
		reply = "assistant reply"
	}
	go func() {
		defer close(out)
		out <- provider.EventStart{Provider: "fake-acp-text", Model: req.Model}
		out <- provider.EventTextDelta{Delta: reply}
		out <- provider.EventDone{
			Stop:    provider.StopEnd,
			Message: provider.Message{Role: provider.RoleAssistant, Content: []provider.Content{provider.TextBlock{Text: reply}}},
		}
	}()
	return out, nil
}

// TestACPSessionPersistsAndLists proves verification (a): a session created +
// prompted persists a transcript on disk under the session root, and
// session/list then returns it with the right sessionId (the durable file
// path) and cwd.
func TestACPSessionPersistsAndLists(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	factory := &fakeFactory{
		client: &textTurnClient{reply: "hello from the assistant"},
		tools:  core.Registry{},
		root:   root,
	}

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
	if sid == "" {
		t.Fatal("session/new returned empty sessionId")
	}
	// The durable id is the on-disk session file path under the root.
	if _, err := os.Stat(sid); err != nil {
		t.Fatalf("sessionId %q is not an existing session file: %v", sid, err)
	}

	// Prompt so a real user+assistant exchange is persisted.
	promptRes := h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "what is up"}},
	})
	if sr, _ := promptRes["stopReason"].(string); sr != StopEndTurn {
		t.Fatalf("stopReason = %q; want %q", promptRes["stopReason"], StopEndTurn)
	}

	// The transcript must actually be on disk: reopen it directly and check
	// the user + assistant messages landed.
	_, msgs, err := core.OpenSession(sid)
	if err != nil {
		t.Fatalf("OpenSession(%q): %v", sid, err)
	}
	if len(msgs) < 2 {
		t.Fatalf("persisted transcript has %d messages; want >= 2 (user + assistant)", len(msgs))
	}
	if msgs[0].Role != provider.RoleUser {
		t.Errorf("first persisted message role = %v; want user", msgs[0].Role)
	}

	// session/list must return this session with the right id + cwd.
	listRes := h.call(MethodSessionList, map[string]any{"cwd": cwd})
	sessions, _ := listRes["sessions"].([]any)
	if len(sessions) == 0 {
		t.Fatalf("session/list returned no sessions; want the one we created")
	}
	var found bool
	for _, si := range sessions {
		m, _ := si.(map[string]any)
		if m["sessionId"] == sid {
			found = true
			if m["cwd"] != cwd {
				t.Errorf("listed session cwd = %v; want %q", m["cwd"], cwd)
			}
		}
	}
	if !found {
		t.Errorf("session/list did not include sessionId %q", sid)
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

// TestACPSessionLoadReplaysThenResponds proves verification (b): session/load
// on a persisted sessionId replays the prior user + assistant messages as
// session/update notifications BEFORE the load response resolves (§13 ordering
// MUST), and the reloaded agent's Messages() reflect the restored transcript
// (model context rehydrated, not just the UI).
func TestACPSessionLoadReplaysThenResponds(t *testing.T) {
	root := t.TempDir()
	cwd := t.TempDir()

	factory := &fakeFactory{
		client: &textTurnClient{reply: "the assistant answer"},
		tools:  core.Registry{},
		root:   root,
	}

	caR, caW := io.Pipe()
	acR, acW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(ctx, caR, acW, factory, AgentInfo{Name: "terva", Version: "test"}) }()

	h := newHarness(t, caW, acR)
	h.call(MethodInitialize, map[string]any{"protocolVersion": 1})

	// Create + prompt a session so it has a user turn and an assistant turn.
	newRes := h.call(MethodSessionNew, map[string]any{"cwd": cwd})
	sid, _ := newRes["sessionId"].(string)
	h.call(MethodSessionPromptName, map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "the user question"}},
	})

	// Now load it. We read frames in arrival order and assert that the replay
	// session/update chunks (user + assistant) all arrive BEFORE the load
	// response frame.
	loadID := h.send(MethodSessionLoad, map[string]any{
		"sessionId":  sid,
		"cwd":        cwd,
		"mcpServers": []any{},
	})

	var replayUser, replayAssistant bool
	var sawResponse bool
	var updatesBeforeResponse, updatesAfterResponse int
	deadline := time.Now().Add(5 * time.Second)
	for !sawResponse {
		if time.Now().After(deadline) {
			t.Fatal("timed out awaiting session/load response")
		}
		f := h.readAny()
		switch {
		case f.method == MethodSessionUpdate:
			upd, _ := f.params["update"].(map[string]any)
			content, _ := upd["content"].(map[string]any)
			text, _ := content["text"].(string)
			switch upd["sessionUpdate"] {
			case UpdateUserMessageChunk:
				if text == "the user question" {
					replayUser = true
				}
			case UpdateAgentMessageChunk:
				if text == "the assistant answer" {
					replayAssistant = true
				}
			}
			if !sawResponse {
				updatesBeforeResponse++
			} else {
				updatesAfterResponse++
			}
		case f.id != nil:
			if rid, ok := f.id.(float64); ok && int(rid) == loadID {
				if f.errObj != nil {
					t.Fatalf("session/load returned error: %v", f.errObj)
				}
				sawResponse = true
			}
		}
	}

	if !replayUser {
		t.Error("session/load did not replay the prior user message as a user_message_chunk")
	}
	if !replayAssistant {
		t.Error("session/load did not replay the prior assistant message as an agent_message_chunk")
	}
	// The §13 MUST: every replay chunk must precede the load response.
	if updatesBeforeResponse < 2 {
		t.Errorf("only %d replay updates arrived before the load response; want >= 2 (user + assistant) — replay MUST precede the response", updatesBeforeResponse)
	}
	if updatesAfterResponse != 0 {
		t.Errorf("%d replay updates arrived AFTER the load response; the §13 ordering MUST is violated", updatesAfterResponse)
	}

	// Model context must be rehydrated: the reloaded agent's transcript holds
	// the restored user + assistant messages (not just a repainted UI).
	ag := factory.lastLoadedAgent()
	if ag == nil {
		t.Fatal("no agent was loaded")
	}
	restored := ag.Messages()
	if len(restored) < 2 {
		t.Fatalf("reloaded agent has %d messages; want >= 2 (context rehydrated)", len(restored))
	}
	var haveUser, haveAssistant bool
	for _, m := range restored {
		text := messageText(m)
		if m.Role == provider.RoleUser && text == "the user question" {
			haveUser = true
		}
		if m.Role == provider.RoleAssistant && text == "the assistant answer" {
			haveAssistant = true
		}
	}
	if !haveUser || !haveAssistant {
		t.Errorf("reloaded agent transcript missing restored turns (user=%v assistant=%v)", haveUser, haveAssistant)
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

// TestACPSessionLoadUnknownIsNotFound proves a session/load on a non-existent
// sessionId returns the resource_not_found error rather than crashing.
func TestACPSessionLoadUnknownIsNotFound(t *testing.T) {
	factory := &fakeFactory{client: &textTurnClient{}, tools: core.Registry{}, root: t.TempDir()}
	caR, caW := io.Pipe()
	acR, acW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Serve(ctx, caR, acW, factory, AgentInfo{Name: "terva", Version: "test"}) }()

	h := newHarness(t, caW, acR)
	h.call(MethodInitialize, map[string]any{"protocolVersion": 1})

	missing := filepath.Join(t.TempDir(), "does-not-exist.jsonl")
	loadID := h.send(MethodSessionLoad, map[string]any{
		"sessionId":  missing,
		"cwd":        t.TempDir(),
		"mcpServers": []any{},
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if time.Now().After(deadline) {
			t.Fatal("timed out awaiting load error")
		}
		f := h.readAny()
		if rid, ok := f.id.(float64); ok && int(rid) == loadID {
			if f.errObj == nil {
				t.Fatalf("session/load on a missing session did not error: %v", f.result)
			}
			if code, _ := f.errObj["code"].(float64); int(code) != CodeResourceNotFound {
				t.Errorf("error code = %v; want %d (resource_not_found)", f.errObj["code"], CodeResourceNotFound)
			}
			break
		}
	}
	cancel()
	_ = caW.Close()
	_ = acW.Close()
}
