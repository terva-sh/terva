//go:build terva_acp

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/acp"
	"terva.sh/terva/packages/agent/authrefresh"
	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extensions"
	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/agent/mcp"
	"terva.sh/terva/packages/agent/permissions"
	"terva.sh/terva/packages/agent/skills"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/privfs"
	"terva.sh/terva/packages/provider"
)

// runACPMode runs the in-process Agent Client Protocol mode (the editor↔agent
// JSON-RPC 2.0 transport on stdio). It is the sibling of runRPCMode: same
// Resolve→NewAgent→hook-wiring skeleton, but the wire is ACP's JSON-RPC and
// the outbound translation is core.AgentEvent→session/update instead of
// modes.EventToJSON.
//
// Phase 0–1: initialize handshake + single-turn session/prompt with the
// event→session/update translation. Phase 2 (here): the real ACP confirmer
// drives session/request_permission when the policy says "ask", session/cancel
// cancels the in-flight turn (and any outstanding permission), and one turn
// runs at a time per session.
func runACPMode(ctx context.Context, args build.Args, version string) error {
	// acp.Serve runs the editor's connection for as long as the editor keeps it
	// open — a session map, turns, and a closeSessions teardown on disconnect —
	// so its stored subscriptions age exactly like the TUI's and the web
	// daemon's. Binding core.Agent directly rather than through the workspace,
	// it does not inherit the refresher NewWorkspace starts, so it starts its
	// own. Reported on stderr: ACP owns stdout for the JSON-RPC framing, and a
	// stray line there is a protocol error, not a message.
	defer authrefresh.Start(ctx, func(provider string, err error) {
		fmt.Fprintf(os.Stderr, "terva: %s login expired and could not be refreshed (%v) — sign in again with /login\n", provider, err)
	})()
	factory := &acpFactory{ctx: ctx, args: args, version: version}
	return acp.Serve(ctx, os.Stdin, os.Stdout, factory, acp.AgentInfo{
		Name:    "terva",
		Version: version,
	})
}

// acpFactory builds a core.Agent per ACP session, wiring the canonical
// per-turn hook ladder (confirm gate) exactly as the headless modes do. One
// resolve per session keeps each session's cwd/tools independent (§3).
//
// Phase 4a: the editor-provided mcpServers payload on session/new|load is
// wired — stdio entries spawn per-session MCP servers whose tools merge into
// the agent's registry (so they inherit the confirm-gate ladder + plan-mode
// filtering). One mcp.Manager per session.
//
// Extensions are now wired too: each session discovers/loads the user's
// installed extensions (and any --ext paths), merges their tools into the
// registry BEFORE MCP (so an extension tool name wins a collision), applies
// the manifest permission rules through the gate, and wires the applicable
// extension hooks (tool-call/turn/assistant-message intercepts, context cards,
// open-work continuation, event fanout). One extensions.Manager per session;
// its Stop is combined with the MCP StopAll into the single session cleanup
// func, so all subprocesses are torn down on disconnect/rebind.
//
// Extension-registered slash commands are wired too (Phase 4c): the
// SessionAgent carries an ExtCommands snapshot + an InvokeExtCommand closure
// built from the manager (acpExtCommands / acpInvokeExtCommand), so the acp run
// mode advertises every extension command alongside the built-ins and executes
// one by translating the extension's response action to ACP. The two built-ins
// that read extension-manager state — /context (ExtContext, from
// ContextSnapshot) and /reload-ext (ReloadExtensions, from Reload) — are wired
// the same way, completing the ACP slash surface.
type acpFactory struct {
	ctx     context.Context
	args    build.Args
	version string
}

// NewSessionAgent builds the agent + a fresh durable session at cwd, with
// persistence hooks wired (§3). The ACP sessionId becomes the session's file
// path, so a later session/load reopens exactly this transcript.
func (f *acpFactory) NewSessionAgent(ctx context.Context, cwd string, mcpServers json.RawMessage, confirmer core.Confirmer) (acp.SessionAgent, error) {
	r, ag, gate, cleanup, observe, extMgr, hooks, err := f.buildAgent(ctx, cwd, mcpServers, confirmer)
	if err != nil {
		return acp.SessionAgent{}, err
	}
	sess, err := core.NewSession(config.TervaHome(), r.CWD, r.Provider, r.Model, f.version)
	if err != nil {
		cleanup()
		return acp.SessionAgent{}, err
	}
	build.WireHeadlessSessionPersist(ag, sess)
	build.BindSession(build.SessionBinding{Agent: ag, Tasks: r.Tasks, Ext: extMgr, Session: sess})
	return acp.SessionAgent{
		Agent:            ag,
		Session:          sess,
		Cleanup:          cleanup,
		Gate:             gate,
		Provider:         r.Provider,
		Model:            r.Model,
		Sandbox:          r.Sandbox,
		Skills:           f.skillSnapshot(r.CWD),
		ReloadSkills:     f.reloadSkills(ag, r.CWD),
		ObserveEvent:     observe,
		ExtCommands:      acpExtCommands(extMgr),
		InvokeExtCommand: acpInvokeExtCommand(extMgr),
		ExtContext:       acpExtContext(extMgr),
		ReloadExtensions: acpReloadExtensions(extMgr),
		TrustWorkspace:   acpTrustWorkspace(ctx, r.CWD, hooks.ApplyTrust),
		UntrustWorkspace: acpUntrustWorkspace(ctx, r.CWD, hooks.ApplyTrust),
		RecordModelSwap:  hooks.RecordSwap,
	}, nil
}

// LoadSessionAgent reopens the durable session at sessionPath (the ACP
// sessionId), builds an agent for it, wires persistence, and returns the
// repaired transcript so the acp package can rehydrate + replay it (§10/§13).
func (f *acpFactory) LoadSessionAgent(ctx context.Context, sessionPath, cwd string, mcpServers json.RawMessage, confirmer core.Confirmer) (acp.SessionAgent, []provider.Message, error) {
	sess, msgs, err := core.OpenSession(sessionPath)
	if err != nil {
		// Propagate os.IsNotExist verbatim so the acp package maps a missing
		// session file to resource_not_found rather than internal_error.
		return acp.SessionAgent{}, nil, err
	}
	// Reopened to keep talking in it: the rows this build appends are this
	// build's. Best-effort — never fail a resume over a provenance row.
	_ = sess.StampVersion(f.version)
	// Prefer the request cwd; fall back to the session's recorded cwd so the
	// agent's tools/system-prompt bind to the right working directory even
	// when the editor omits it.
	if cwd == "" {
		cwd = sess.Meta.CWD
	}
	r, ag, gate, cleanup, observe, extMgr, hooks, err := f.buildAgent(ctx, cwd, mcpServers, confirmer)
	if err != nil {
		_ = sess.Close()
		return acp.SessionAgent{}, nil, err
	}
	// As in NewSessionAgent: a resumed session reopens the board it left behind
	// rather than an empty one, and announces itself to subscribing extensions.
	build.WireHeadlessSessionPersist(ag, sess)
	build.BindSession(build.SessionBinding{Agent: ag, Tasks: r.Tasks, Ext: extMgr, Session: sess})
	// Re-point the agent at the session's OWN stored model, not just the menu:
	// before this, ACP resume DISPLAYED the stored model (a prior model switch,
	// recorded in meta) but RAN on the resolved default until a later switch
	// rebuilt the client. Non-fatal — on failure the built model stands; the
	// returned pair drives the menu, so display and runtime can no longer diverge.
	prov, model, note := applyResumedModel(ag, f.args, sess, r.Provider, r.Model)
	if note != "" {
		fmt.Fprintln(os.Stderr, "terva:", note)
	}
	// A resume onto the session's stored model is a model swap — the same
	// build.ApplyModelSwap the /model verb runs — so it needs the same second
	// half. Without this the session RUNS on its stored model while its rebuild
	// args still name the one it was built with, and the first extension reload
	// hands the model a terva_status naming the wrong provider. applyResumedModel
	// always builds a fresh client when it moves at all, so a move is a moved
	// endpoint; when it declines to move it returns the built pair unchanged and
	// this records what is already true.
	moved := prov != r.Provider || model != r.Model
	hooks.RecordSwap(prov, model, moved)
	return acp.SessionAgent{
		Agent:            ag,
		Session:          sess,
		Cleanup:          cleanup,
		Gate:             gate,
		Provider:         prov,
		Model:            model,
		Sandbox:          r.Sandbox,
		Skills:           f.skillSnapshot(r.CWD),
		ReloadSkills:     f.reloadSkills(ag, r.CWD),
		ObserveEvent:     observe,
		ExtCommands:      acpExtCommands(extMgr),
		InvokeExtCommand: acpInvokeExtCommand(extMgr),
		ExtContext:       acpExtContext(extMgr),
		ReloadExtensions: acpReloadExtensions(extMgr),
		TrustWorkspace:   acpTrustWorkspace(ctx, r.CWD, hooks.ApplyTrust),
		UntrustWorkspace: acpUntrustWorkspace(ctx, r.CWD, hooks.ApplyTrust),
		RecordModelSwap:  hooks.RecordSwap,
	}, msgs, nil
}

// ListSessions enumerates durable terva sessions for session/list (§10),
// newest first, optionally filtered to cwd. When cwd is empty the factory's
// resolved cwd is used — the in-process agent buckets sessions per working
// directory and cannot cheaply enumerate every project, so we scope to the
// one the editor is most likely asking about (its own cwd).
func (f *acpFactory) ListSessions(cwd string) []acp.SessionInfo {
	if cwd == "" {
		cwd = f.args.CWD
	}
	if cwd == "" {
		return nil
	}
	root := config.TervaHome()
	summaries := core.DescribeSessions(root, cwd)
	out := make([]acp.SessionInfo, 0, len(summaries))
	for _, s := range summaries {
		// Skip empty meta-only stubs (no messages) — they aren't resumable
		// conversations and would clutter the editor's list.
		if s.MessageCount == 0 {
			continue
		}
		title := s.Title
		if title == "" {
			title = s.FirstUserText
		}
		out = append(out, acp.SessionInfo{
			SessionID: s.Path, // the durable id is the file path
			CWD:       cwd,
			Title:     title,
			UpdatedAt: sessionUpdatedAt(s.Path),
		})
	}
	return out
}

// acpLoggedInProviders scopes the ACP model menu to the providers the user can
// actually reach (plan §14) — the catalog offers every known provider regardless
// of credentials, and a menu entry that cannot run a turn is not an offer.
func acpLoggedInProviders() map[string]bool { return build.LoggedInProviderSet() }

// ModelOptions returns the catalog filtered to AUTHENTICATED providers — the
// ACP `model` config option's selectable values (plan §14 "model menu scope").
// Speculative models (vendor-announced, not yet live on the public API) are
// INCLUDED, matching terva's own /model picker and `terva models`: an entire
// provider's catalog can be speculative (e.g. openai-codex today), so excluding
// them would hide that provider from the editor even when the user is logged in.
func (f *acpFactory) ModelOptions() []acp.ModelOption {
	return modelOptionsFor(provider.Active(), acpLoggedInProviders())
}

// modelOptionsFor builds the model-menu options from a catalog and the set of
// authenticated providers: a model is offered iff its provider is authenticated
// (speculative or not). Factored out of ModelOptions so the auth/speculative
// gating is unit-testable without the global catalog or the credential store.
func modelOptionsFor(models []provider.Model, authed map[string]bool) []acp.ModelOption {
	var out []acp.ModelOption
	for _, m := range models {
		if !authed[m.Provider] {
			continue
		}
		label := m.DisplayName
		if label == "" {
			label = m.ID
		}
		out = append(out, acp.ModelOption{
			ID:          m.ID,
			Provider:    m.Provider,
			DisplayName: label,
		})
	}
	return out
}

// SwitchModel resolves a model switch for the ACP `model` config option. It
// mirrors interactive_model.go's swapModel and rpc.go's cross-endpoint
// rejection:
//
//   - An unknown model id is an error (the editor sent a value not in our
//     catalog).
//   - A model whose provider is not authenticated is an error (we must never
//     route a turn at a provider the user can't reach).
//   - Same provider AND same resolved endpoint: the existing client serves the
//     new model, so Reuse=true (the acp package SetModel's in place).
//   - Otherwise (cross-provider, or same provider routing to a different base
//     URL): build a fresh provider client via Resolve+NewClient so the new
//     model's endpoint/credentials are honored, and return it with Reuse=false
//     (the acp package SetClientAndModel's, keeping the transcript). A
//     same-provider model that routes to a DIFFERENT endpoint is rejected only
//     if we cannot build a client for it — building one is the in-process win
//     over rpc (which has no rebuild path and rejects outright).
func (f *acpFactory) SwitchModel(currentProvider, currentModel, targetModelID string) (acp.ModelSwitch, error) {
	target, err := provider.FindModel("", targetModelID)
	if err != nil {
		return acp.ModelSwitch{}, err
	}
	if authed := acpLoggedInProviders(); !authed[target.Provider] {
		return acp.ModelSwitch{}, fmt.Errorf("provider %q is not authenticated; run terva interactively and /login first", target.Provider)
	}

	// Same provider + same endpoint: the current client is reusable, so swap
	// the model id in place (no rebuild). The endpoint check guards against a
	// per-model models.json baseUrl routing two same-provider models to
	// different backends — mutating the id alone would keep firing at the old
	// endpoint (rpc.go's rejection rationale).
	if target.Provider == currentProvider {
		if cur, curErr := provider.FindModel(currentProvider, currentModel); curErr == nil && cur.BaseURL == target.BaseURL {
			sw := acp.ModelSwitch{Provider: target.Provider, Model: target.ID, Reuse: true}
			// A nil Client is how the shared event spells "same endpoint, new
			// id" — and it still re-points the host-routed dispatch tools,
			// which an id swap changes just as much as a rebuild does.
			sw.Apply = func(ag *core.Agent) {
				build.ApplyModelSwap(build.ModelSwap{Agent: ag, Provider: sw.Provider, Model: sw.Model})
			}
			return sw, nil
		}
	}

	// Cross-provider, or same provider with a different endpoint: build a fresh
	// client for the target model so its endpoint + credentials are honored.
	next := f.args
	next.Provider = target.Provider
	next.Model = target.ID
	// Drop launch-time overrides that pin the original provider's endpoint /
	// key so a cross-provider switch resolves the target's own creds (mirrors
	// buildAgentForRescue's intent).
	next.APIKey = ""
	next.BaseURL = ""
	r, err := build.Resolve(next, true)
	if err != nil {
		return acp.ModelSwitch{}, err
	}
	if !r.HasCredential() {
		return acp.ModelSwitch{}, fmt.Errorf("no credential resolved for provider %q", target.Provider)
	}
	sw := acp.ModelSwitch{
		Provider:   r.Provider,
		Model:      r.Model,
		Client:     r.NewClient(),
		Reuse:      false,
		AuthMethod: r.AuthMethod,
		BaseURL:    r.BaseURL,
	}
	sw.Apply = func(ag *core.Agent) {
		build.ApplyModelSwap(build.ModelSwap{
			Agent:      ag,
			Client:     sw.Client,
			Provider:   sw.Provider,
			Model:      sw.Model,
			AuthMethod: sw.AuthMethod,
			BaseURL:    sw.BaseURL,
		})
	}
	return sw, nil
}

// buildAgent resolves args for cwd and constructs the agent with the canonical
// ACP hook ladder (confirm gate + correlation seam). Shared by
// NewSessionAgent and LoadSessionAgent so the two paths build identical
// agents; only the durable session (new vs reopened) differs.
//
// mcpServers is the raw editor-provided session/new|load mcpServers array
// (Phase 4a): its stdio entries spawn per-session MCP servers whose tools are
// merged onto the resolved registry BEFORE r.NewAgent(), exactly as setupMCP
// does for the headless modes — so MCP tools sit in the registry the agent
// receives and inherit the confirm-gate ladder + plan-mode filtering. The
// returned stop func (never nil) tears those subprocesses down. The user's
// config-file MCP (cfg.MCP) is deliberately NOT merged in, because under ACP the
// editor owns the MCP server set (it manages the user's MCP config and sends it
// on the wire), so honoring cfg.MCP too would double-spawn editor-managed
// servers. A TRUSTED project's .terva/config.json MCP — a source the editor does
// NOT manage — IS merged (trust-gated, editor wins on a collision); see
// setupACPMCP.
//
// The applyTrust func it returns is the live half of /trust and /untrust: the
// ACP twin of Workspace.applyTrust, closed over the pieces a flip has to move
// (hook engine, extension manager, tool set). It is what makes ACP a live-trust
// host rather than one whose verdict is fixed at launch.
func (f *acpFactory) buildAgent(ctx context.Context, cwd string, mcpServers json.RawMessage, confirmer core.Confirmer) (build.Resolved, *core.Agent, *core.ConfirmGate, func(), func(core.AgentEvent), *extensions.Manager, acpSessionHooks, error) {
	// Each session resolves with its own cwd so tools, system prompt, and
	// session dir bind to the editor-provided working directory.
	args := f.args
	if cwd != "" {
		args.CWD = cwd
	}

	// Build the permission gate with the ACP confirmer as its inner
	// Confirmer (§8): unlike the headless modes — which pass a nil inner
	// (refuse-by-default) — ACP can actually ask, by issuing
	// session/request_permission to the editor. A policy "ask" therefore
	// reaches the confirmer instead of being refused; allow/deny rules and
	// plan-mode read-only auto-allows still short-circuit before it.
	//
	// buildPermissionPolicy already folds the INSTALLED extensions' manifest
	// permission contributions (the `permissions` key in extension.json — e.g.
	// a writer tool declared `ask`) into the rule list via
	// extensionPermissionRules(args.CWD), so an extension's ask/deny default
	// reaches the gate here regardless of whether the manager has finished
	// spawning. Merging the extension TOOLS below is what makes those rules
	// reachable (the model can now call the tool).
	pol, polWarns := permissions.BuildPolicy(args.PermInputs())
	for _, w := range polWarns {
		fmt.Fprintf(os.Stderr, "note: %s\n", w)
	}
	// Phase 4b: build a gate UNCONDITIONALLY for ACP sessions, even when the
	// launch mode is pure yolo (buildPermissionPolicy returns nil). The
	// headless modes take a no-gate fast path in yolo, but ACP exposes
	// session/set_mode, so a yolo-launched session must still be able to
	// switch DOWN to a stricter mode at runtime — which needs a live gate to
	// SetMode on. A yolo policy auto-allows everything, so the gate is
	// behaviourally identical to the no-gate fast path until the user changes
	// the mode.
	if pol == nil {
		pol = synthYoloPolicy()
	}
	confirmGate := core.NewPolicyGate(pol, confirmer)
	roSet := pol.ReadOnly

	r, err := build.Resolve(args, true)
	if err != nil {
		return build.Resolved{}, nil, nil, nil, nil, nil, acpSessionHooks{}, err
	}
	// ACP untrusted = restricted for now (Phase 4 — an editor
	// session/request_permission trust prompt — is deferred). Log a
	// warning naming how to enable; --trust still works over the wire.
	permissions.WarnRestrictedWorkspace(args.CWD, r.Trusted)
	permissions.WarnPersistentlyUnjailed(args.PermInputs())
	r.AdoptReadOnlySet(roSet)

	// Wire this session's extensions (tools + read-only classification) BEFORE
	// MCP, so an extension tool name wins a collision against an MCP tool of
	// the same name — preserving the non-interactive policy
	// (setupNonInteractiveExtensions merges extensions, then MCP). stopExt is
	// never nil. Returns the manager so the hook ladder and the event observer
	// below can use it; nil only when extensions are entirely disabled
	// (--no-ext with no --ext paths still yields a live manager that simply
	// has no tools, so extMgr is non-nil whenever wiring is requested).
	extMgr, stopExt := f.setupACPExtensions(ctx, args, &r)
	// Wire the editor-provided MCP servers AFTER extensions so their tools are
	// in the registry the agent receives (mirrors setupMCP's order in the
	// headless modes). stopMCP is never nil. Honors --no-mcp. The adapter comes
	// back so a later tool-set rebuild can re-merge these servers' tools — a
	// fresh Resolve carries none of them.
	mcpAdapter, stopMCP := f.setupACPMCP(ctx, args, &r, mcpServers)
	// One cleanup func stops BOTH subprocess sets (extensions + MCP). The acp
	// package calls it on session close, rebind, and disconnect, so neither
	// leaks. Extensions stop first (they may hold context the MCP teardown
	// doesn't need); order is otherwise immaterial. stopExt/stopMCP are never
	// nil.
	cleanup := func() {
		stopExt()
		stopMCP()
	}

	ag := r.NewAgent()
	// ACP is NOT a fixed-trust host: /trust and /untrust flip Workspace Trust
	// over the wire, mid-session. The live-trust engine is what makes that
	// reachable — it keeps a standing engine for an untrusted project whose
	// hooks are on disk, so acpApplyTrust has somewhere to put the specs.
	// BuildHookEngine (which this used to call) returns nil in exactly that
	// case, and a newly trusted repo's pre/post-tool hooks stayed inert until
	// the editor opened a new session.
	hookEng := build.BuildLiveTrustHookEngine(args, r.Trusted)
	// Canonical tool-call ladder (pre-hooks, confirm gate, extension
	// intercept). Passing extMgr in activates BOTH the extension tool-call
	// intercept AND — through the confirm gate built above — the manifest
	// permission rules. The ladder is nil-safe across all three args.
	// The gate hands each call's id to ConfirmWithCall directly — the §13
	// correlation seam — so no wrapper records a "current call" ahead of the
	// ladder, and nothing collides when a host_tool_call approval parks
	// concurrently with a model call's.
	ag.BeforeToolExecute = build.BuildBeforeToolExecute(hookEng, confirmGate, extMgr, ag)
	build.WireHostToolDispatcher(ag, extMgr, confirmGate)
	// Apply the subset of the non-interactive extension hooks that make sense
	// under ACP: BeforeTurn / BeforeAssistantMessage (extension turn +
	// assistant-message intercepts), ContextProvider (live context cards), and
	// the open-work continuation gate (re-prompt once on open work). We deliberately do NOT set
	// ag.OnEvent here — the acp package owns that field for its session/update
	// translator — and instead hand the event observer back so bindSession can
	// COMPOSE it after the translator (see the returned observe func).
	if extMgr != nil {
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
		ag.BeforeUserMessage = func(text string) (bool, string, string) {
			res := extMgr.InterceptUserMessage(ctx, text)
			if res.Block {
				return false, res.Reason, ""
			}
			return true, "", res.ReplaceText
		}
	}
	// The live cards the model reads each turn. Outside the manager check: the
	// task board is not an extension, so its card and its open-work gate follow
	// r.Tasks. They were nested inside it, which made a built-in board's
	// visibility depend on whether this session happened to have extensions.
	//
	// The order lives in build.EphemeralTail now, which is also what the trust
	// flip's Lore re-derivation below reproduces — the two used to be written
	// out separately, and the re-derivation wrote neither.
	ephemeral := build.EphemeralTail{Ext: build.ExtEphemeral(extMgr), Tasks: r.Tasks}
	build.WireEphemeralTail(ag, ephemeral)
	ag.AddContinuationGate(build.OpenWorkGate(extMgr, r.Tasks))
	// observe is the extension-side event sink: it fans every event out to the
	// extensions and feeds the two tool events into the hook correlator —
	// exactly what wireNonInteractiveAgentExtHooks assigns to OnEvent, but here
	// returned as a value so the acp package composes it AFTER translateEvent
	// rather than clobbering the translator (the §OnEvent composition point).
	// nil when there is nothing to observe, so bindSession keeps the
	// translation-only OnEvent.
	var observe func(core.AgentEvent)
	if extMgr != nil || hookEng != nil {
		// Per-session workspace differ rooted at this session's cwd (ACP
		// has no interactive /cd, so the session cwd is the authoritative
		// workspace), so workspace_changed fires under ACP too. nil when
		// extensions are off (the observer no-ops on a nil differ).
		var differ *tools.WorkspaceDiffer
		if extMgr != nil {
			sessionCWD := cwd
			differ = tools.NewWorkspaceDiffer(func() string { return sessionCWD })
		}
		wsObserve := build.WorkspaceChangeObserver(differ, extMgr)
		observe = func(ev core.AgentEvent) {
			wsObserve(ev)
			build.FanoutAgentEvent(extMgr, ev)
			build.ObserveAgentEventForHooks(hookEng, ev)
		}
	}

	// The tool set the model sees, rebuilt from a fresh Resolve — the survivor
	// rule lives in build.LiveToolSet, shared with rpc. Note the ABSENT
	// TrustPin: unlike rpc, ACP can flip Workspace Trust mid-session, and the
	// trust verb persists the verdict before applying it, so re-reading the
	// store is how the rebuild learns what /trust just decided.
	//
	// Assembled HERE, inside the closure, rather than once at session build: the
	// editor can switch this session's model, recordSwap below moves args with
	// it, and a struct holding a launch-time copy would re-resolve the model the
	// session started on. See LiveToolSet.Args.
	// The memory tool holds this session's bound stores. Captured once, outside
	// the closure: a fresh Resolve mints fresh stores, and adopting those would
	// leave the model writing facts nothing reads. Nil when memory is off.
	memTool, _ := r.ToolRegistry["memory"].(*tools.MemoryTool)
	// Captured once, for the same reason as memTool: a rebuild's fresh tracker
	// would forget every file the model has read.
	fileState := r.Files()
	rebuildTools := func() {
		build.LiveToolSet{
			Args:     args,
			ReadOnly: roSet,
			Tasks:    r.Tasks,
			Memory:   memTool,
			Files:    fileState,
			Sandbox:  r.Sandbox,
			Ext:      extMgr,
			MCP:      mcpAdapter,
		}.Rebuild(ag)
	}

	// recordSwap is this session's half of a model switch — the acp equivalent
	// of ModelSwap.After, which is where the daemon does the same thing.
	//
	// build.ApplyModelSwap moves the running agent, and the acp package's
	// ModelSwitch.Apply carries it; neither can reach the args this host
	// re-resolves from, because SwitchModel is a factory method and the factory
	// has no session. So the acp package joins the two, calling this beside
	// Apply — the only place that knows both.
	//
	// Without it the swap held until the next rebuild (an extension reload, a
	// /trust flip), which re-minted terva_status naming the provider the session
	// had switched away from and re-derived read's vision support from the
	// launch model.
	recordSwap := func(prov, model string, rebuiltClient bool) {
		args.Provider, args.Model = prov, model
		if rebuiltClient {
			// A rebuilt client means the endpoint moved, so the launch-time
			// key/URL overrides now pin one this session has left — the same
			// clearing SwitchModel does on the copy it resolves the replacement
			// from, and the daemon's setModel on its own args.
			args.APIKey, args.BaseURL = "", ""
		}
	}
	// An extension reload has to reach the model, or a freshly discovered
	// extension's tools are running subprocesses nothing can call. This covers
	// /reload-ext as well as the reload a trust flip triggers.
	if extMgr != nil {
		extMgr.SetOnReload(rebuildTools)
	}
	// The live half of /trust and /untrust. Every surface a flip has to reach is
	// named in the literal, and the order they move in belongs to ApplyTrust —
	// shared with the daemon, so the two hosts cannot disagree about whether a
	// withdrawal stops the repo's programs before or after it stops showing the
	// repo to the model.
	//
	// Project skills and context files are baked into the system prompt at
	// resolve time and still land on a NEW session — the same deliberate limit
	// the interactive and daemon paths have, and what the /trust confirmation
	// says. (The skill TOOL returns on the rebuild, and keyed lore on the Lore
	// re-derivation; only the prompt-baked halves wait.)
	applyTrust := func(tctx context.Context, trusted bool) {
		build.ApplyTrust(tctx, trusted, build.TrustSurfaces{
			Args:    args,
			Hooks:   hookEng,
			Ext:     extMgr,
			Grace:   acpReloadGrace,
			Rebuild: rebuildTools,
			Lore:    func() { build.RewireLoreContext(ag, args, ephemeral) },
			// After: the acp session has no client to broadcast to — the
			// command's own confirmation chunk is the notification.
		})
	}
	return r, ag, confirmGate, cleanup, observe, extMgr, acpSessionHooks{
		ApplyTrust: applyTrust,
		RecordSwap: recordSwap,
	}, nil
}

// acpSessionHooks are the per-session host closures buildAgent hands back —
// the ones that close over state only the composition root has (this session's
// args, hook engine, extension manager, rebuild) and that the acp package
// invokes when the editor asks for something.
//
// A struct rather than more return values: buildAgent already returns seven,
// and each of these is the same kind of thing. It is also the list, in the
// TrustSurfaces sense — a host event that has to reach back into the build
// scope goes here, where the next one is visible next to the last.
type acpSessionHooks struct {
	// ApplyTrust brings this session in line with a new Workspace Trust
	// verdict (build.ApplyTrust's four surfaces, in its order).
	ApplyTrust func(ctx context.Context, trusted bool)

	// RecordSwap records a model switch into the args this session
	// re-resolves from, so a later tool-set rebuild reproduces the swap
	// instead of restoring the launch model. rebuiltClient says the endpoint
	// moved.
	RecordSwap func(prov, model string, rebuiltClient bool)
}

// acpExtCommands builds the SessionAgent.ExtCommands snapshot from a session's
// extensions.Manager: it maps each registered command into the acp package's
// neutral acp.ExtCommandInfo (the acp package can't import extensions). The
// extension command registry carries no argument hint today, so Hint is left
// empty — the acp boundary supports one for when register_command grows it.
// Returns nil when there is no manager (extensions disabled), so the acp
// command surface stays built-in only for that session.
func acpExtCommands(extMgr *extensions.Manager) func() []acp.ExtCommandInfo {
	if extMgr == nil {
		return nil
	}
	return func() []acp.ExtCommandInfo {
		cmds := extMgr.Commands()
		if len(cmds) == 0 {
			return nil
		}
		out := make([]acp.ExtCommandInfo, 0, len(cmds))
		for _, c := range cmds {
			out = append(out, acp.ExtCommandInfo{
				Name:        c.Name,
				Description: c.Description,
			})
		}
		return out
	}
}

// acpExtCommandTimeout caps how long the host waits for an extension to answer
// a command_invoked frame, mirroring the interactive invokeExtensionCommand's
// 30s budget so a hung extension can't wedge an ACP turn forever.
const acpExtCommandTimeout = 30 * time.Second

// acpInvokeExtCommand builds the SessionAgent.InvokeExtCommand closure from a
// session's extensions.Manager: it fires the named command through
// extensions.Manager.Invoke and maps the extproto.CommandResponseFromExt onto
// the acp package's neutral acp.ExtCommandResult so the acp run mode can apply
// the action without importing extproto. The mapping mirrors the interactive
// handler's action switch (interactive_extensions.go):
//
//   - the response's Error field (a command-level failure) -> Action=error.
//   - prompt   -> Action=prompt, carrying the task text.
//   - display  -> Action=display, carrying the text.
//   - insert / open_panel (TUI-only) -> Action degraded to the matching
//     acp action with the would-be text folded into Display, since ACP has no
//     editor-input or panel surface — the acp layer renders it so nothing is
//     lost.
//   - noop / "" / anything else -> Action=noop.
//
// Returns nil when there is no manager.
func acpInvokeExtCommand(extMgr *extensions.Manager) func(context.Context, string, string) (acp.ExtCommandResult, error) {
	if extMgr == nil {
		return nil
	}
	return func(ctx context.Context, name, args string) (acp.ExtCommandResult, error) {
		resp, err := extMgr.Invoke(ctx, name, args, acpExtCommandTimeout)
		if err != nil {
			return acp.ExtCommandResult{}, err
		}
		// A command-level error the extension reported takes precedence over
		// the action (the interactive handler surfaces resp.Error first too).
		if resp.Error != "" {
			return acp.ExtCommandResult{Action: acp.ExtActionError, Error: resp.Error}, nil
		}
		switch resp.Action {
		case "prompt":
			return acp.ExtCommandResult{Action: acp.ExtActionPrompt, Prompt: resp.Prompt}, nil
		case "display":
			return acp.ExtCommandResult{Action: acp.ExtActionDisplay, Display: resp.Display}, nil
		case "insert":
			// TUI-only: no ACP editor input. Degrade to display so the would-be
			// inserted text is still shown.
			return acp.ExtCommandResult{Action: acp.ExtActionInsert, Display: resp.Insert}, nil
		case "open_panel":
			// TUI-only: no ACP panel. Degrade to display, rendering the panel
			// content as text.
			return acp.ExtCommandResult{Action: acp.ExtActionOpenPanel, Display: renderPanelText(resp.OpenPanel)}, nil
		case "noop", "":
			return acp.ExtCommandResult{Action: acp.ExtActionNoop}, nil
		default:
			// An action the host doesn't understand is a no-op rather than a
			// crash — capability honesty over a forwarded surprise.
			return acp.ExtCommandResult{Action: acp.ExtActionNoop}, nil
		}
	}
}

// acpReloadGrace caps how long the host waits for the extensions to settle on a
// /reload-ext, matching the interactive runReloadExt's 2s budget (the manager
// then floors the ready-wait at its own 3s startup grace internally).
const acpReloadGrace = 2 * time.Second

// acpExtContext builds the SessionAgent.ExtContext snapshot from a session's
// extensions.Manager: it maps each extensions.ContextItem (static guidance or a
// live card) into the acp package's neutral acp.ContextItem so the native
// /context command can show what is being injected into the model without the
// acp package importing extensions. Re-read each call so it reflects live cards.
// Returns nil when there is no manager (extensions disabled), so /context
// degrades to "extensions are not enabled".
func acpExtContext(extMgr *extensions.Manager) func() []acp.ContextItem {
	if extMgr == nil {
		return nil
	}
	return func() []acp.ContextItem {
		snap := extMgr.ContextSnapshot()
		if len(snap) == 0 {
			return nil
		}
		out := make([]acp.ContextItem, 0, len(snap))
		for _, it := range snap {
			out = append(out, acp.ContextItem{
				Source: it.Source,
				Kind:   it.Kind,
				Label:  it.Label,
				Text:   it.Text,
			})
		}
		return out
	}
}

// acpReloadExtensions builds the SessionAgent.ReloadExtensions closure from a
// session's extensions.Manager: it drives extensions.Manager.Reload (teardown +
// respawn) and maps the extensions.ReloadStats onto the acp package's neutral
// acp.ReloadStats (flattening the []error to a count) so the native /reload-ext
// command can report the outcome without importing extensions. The acp layer
// re-advertises the command catalog afterwards; the manager's live Commands()
// (read through ExtCommands) reflects the reload automatically. Returns nil when
// there is no manager, so /reload-ext degrades to a note.
func acpReloadExtensions(extMgr *extensions.Manager) func(context.Context) acp.ReloadStats {
	if extMgr == nil {
		return nil
	}
	return func(ctx context.Context) acp.ReloadStats {
		stats := extMgr.Reload(ctx, acpReloadGrace)
		return acp.ReloadStats{
			Stopped: stats.Stopped,
			Loaded:  stats.Loaded,
			Ready:   stats.Ready,
			Errors:  len(stats.Errors),
		}
	}
}

// acpTrustWorkspace builds the SessionAgent.TrustWorkspace closure: it persists
// cwd's Workspace Trust verdict (TrustPath, parent marking "trust descendants
// too") and then hands off to acpApplyTrust, which makes the now-trusted
// project content go live for this session — hooks, extensions, and the tool
// set the model sees. Project skills/context are baked into the system prompt at
// build time and can't be re-injected mid-session, so they take effect on a new
// session (the ACP command's confirmation says so).
//
// The persist and the apply are split on purpose: persisting is what /trust
// MEANS, applying is what makes it true for the session already open. Doing only
// the first is what left ACP holding a launch snapshot.
func acpTrustWorkspace(ctx context.Context, cwd string, apply func(context.Context, bool)) func(parent bool) error {
	if apply == nil {
		return nil
	}
	return func(parent bool) error {
		if err := config.TrustPath(cwd, parent); err != nil {
			return err
		}
		apply(ctx, true)
		return nil
	}
}

// acpUntrustWorkspace builds the SessionAgent.UntrustWorkspace closure: the
// symmetric inverse of acpTrustWorkspace. It drops cwd from the trust store
// (UntrustPath) and applies the withdrawal live, so the project's extensions
// are torn down and its hooks stop running for this session rather than at the
// next launch.
func acpUntrustWorkspace(ctx context.Context, cwd string, apply func(context.Context, bool)) func() error {
	if apply == nil {
		return nil
	}
	return func() error {
		if err := config.UntrustPath(cwd); err != nil {
			return err
		}
		apply(ctx, false)
		return nil
	}
}

// renderPanelText flattens an extension's open_panel PanelSpec into plain text
// for the ACP display degradation: title, then each line, then footer. ACP has
// no interactive panel, so the panel's content is the most we can surface
// without silently dropping it. nil panel yields "".
func renderPanelText(p *extproto.PanelSpec) string {
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

// synthYoloPolicy builds a yolo PermissionPolicy with no rules — the policy a
// pure-yolo launch would otherwise short-circuit to nil. ACP needs a real gate
// even in yolo so session/set_mode can switch to a stricter mode at runtime
// (the gate's SetMode is copy-on-write over this policy).
//
// It goes through permissions.NewPolicy rather than composing the struct here.
// The hand-built version set four of the seven fields and claimed the rest did
// not matter; Interactive and DecomposeCommand decide what a rule MEANS once
// the mode tightens, which is the only reason this policy exists.
func synthYoloPolicy() *core.PermissionPolicy {
	return permissions.NewPolicy(core.ApprovalYolo, nil)
}

// setupACPMCP starts the editor-provided MCP servers for one session — plus a
// TRUSTED project's config-file MCP servers (editor wins on a name collision) —
// and merges their tools into the resolved registry, returning a stop func
// (never nil). It is the ACP sibling of setupMCP (cli.go): same StartAll →
// adapter → MergeExtensionTools shape. The editor-supplied set comes from the
// wire (session/new|load mcpServers); the USER's config-file MCP is still NOT
// merged in (the editor owns the user's MCP set — see buildAgent's doc), but a
// trusted project's .terva/config.json MCP IS a distinct source the editor does
// not manage, so it loads when r.Trusted, gated exactly like the headless modes
// (Workspace Trust Phase 6). An untrusted project contributes nothing. Startup
// problems (skipped http/sse entries, a server that won't spawn) are
// best-effort warnings, never fatal — one broken server must not take the
// session down. Per-server stderr goes to $TERVA_HOME/logs/mcp-<name>.log, like
// the headless modes. Warnings surface to stderr: session/new|load runs before
// the first turn, so there is no agent_message sink to route them to yet.
// It returns the tool adapter alongside the stop func so a later rebuild can
// re-merge these servers' tools onto a fresh Resolve; nil when no server ran, in
// which case there is nothing to re-merge.
func (f *acpFactory) setupACPMCP(ctx context.Context, args build.Args, r *build.Resolved, mcpServers json.RawMessage) (*build.MCPToolAdapter, func()) {
	noop := func() {}
	if args.NoMCP {
		return nil, noop
	}
	servers, warns := acp.ParseMCPServers(mcpServers)
	for _, w := range warns {
		fmt.Fprintln(os.Stderr, "note:", w)
	}

	cfg := &mcp.Config{Servers: make(map[string]mcp.ServerConfig, len(servers))}
	// A trusted project's config-file servers load first so the editor-provided
	// entries below win on a name collision (the editor's set is authoritative
	// under ACP). trustedProjectMCP returns nil unless r.Trusted, so an
	// untrusted workspace adds nothing.
	if proj := config.TrustedProjectMCP(args.CWD, r.Trusted); proj != nil {
		for name, sc := range proj.Servers {
			cfg.Servers[name] = sc
		}
	}
	for _, s := range servers {
		// Last-wins on a duplicate name, matching a JSON object's de-dup; the
		// editor shouldn't send duplicates, and StartAll already warns on
		// namespaced-tool collisions across distinct servers. Editor entries
		// also win over a trusted project's same-named server (added above).
		cfg.Servers[s.Name] = mcp.ServerConfig{
			Command: s.Command,
			Args:    s.Args,
			Env:     s.Env,
		}
	}
	// Honor disable_mcp (user ∪ restrict-only project) here too. ACP has no
	// /mcp dialog, so this is gating only: a disabled server never spawns.
	for name := range config.ResolvedDisableMCP(args.CWD, r.Trusted) {
		delete(cfg.Servers, name)
	}
	if len(cfg.Servers) == 0 {
		return nil, noop
	}

	stderrFor := func(server string) io.Writer {
		if mkErr := privfs.MkdirAll(config.LogsPath()); mkErr != nil {
			return nil
		}
		fh, ferr := privfs.OpenFile(filepath.Join(config.LogsPath(), "mcp-"+server+".log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY)
		if ferr != nil {
			return nil
		}
		return fh
	}

	mgr := mcp.StartAll(ctx, cfg, args.CWD, stderrFor)
	for _, w := range mgr.Warnings() {
		fmt.Fprintln(os.Stderr, "note:", w)
	}
	adapter := &build.MCPToolAdapter{Mgr: mgr}
	r.MergeExtensionTools(adapter)
	return adapter, mgr.StopAll
}

// setupACPExtensions builds a per-session extensions.Manager, applies the
// resolved disable config, loads any explicit --ext paths, discovers the
// installed extensions (unless --no-ext), waits briefly for them to flush
// their initial register_tool frames, and merges their tools into the
// resolved registry. It is the ACP sibling of setupNonInteractiveExtensions
// (cli.go): same New → SetContextDisabled/SetDisabledExtensions → LoadExplicit
// → Discover → WaitForReady → MergeExtensionTools shape, minus the TUI host
// hooks (ACP has no interactive surface, so notify/display go to stderr and
// submit/insert are no-ops via nonInteractiveExtHooks).
//
// It returns the live manager (so the hook ladder + event observer can wire to
// it) and a stop func (never nil) that tears the extension subprocesses down.
// The manager is non-nil whenever wiring runs — even under --no-ext with no
// --ext paths it is a live-but-toolless manager — so the caller's hook wiring
// stays uniform; only its Tools() is empty then. Read-only extension tools
// join r's read-only set during the merge (MergeToolsForMode adds read_only
// names to roSet), so plan/workspace modes don't prompt for them.
//
// Load errors are best-effort stderr notes, never fatal: one broken extension
// must not take the session down, exactly as in the headless/interactive
// paths.
//
// session_start is not emitted HERE — the durable session does not exist yet at
// agent-build time. It is emitted by the build.BindSession call in
// NewSessionAgent / LoadSessionAgent, which is where it does, mirroring how the
// headless modes defer it. (That was the plan this comment used to describe as
// future work; the manager is per-session, so each announces exactly one
// session and Manager.Stop supplies the matching session_end.)
func (f *acpFactory) setupACPExtensions(ctx context.Context, args build.Args, r *build.Resolved) (*extensions.Manager, func()) {
	extMgr := build.NewExtensionManager(config.TervaHome(), r.CWD, f.version, r.Provider, r.Model, build.NonInteractiveExtHooks{})
	extMgr.SetContextDisabled(r.DisableContextExtensions)
	extMgr.SetDisabledExtensions(r.DisableExtensions)      // before Discover/LoadExplicit
	extMgr.SetAllowedExtensions(args.WithExtensions)       // --extensions allowlist; --ext paths bypass
	extMgr.SetConfigResolver(build.ResolveExtensionConfig) // hello_ack config delivery
	build.WireSessionReader(extMgr, config.TervaHome(), r.CWD)
	build.WireExtensionSecrets(extMgr, config.TervaHome())
	extMgr.SetProjectTrusted(r.Trusted) // gate project ext dirs on Workspace Trust
	// --ext paths first so they win against installed extensions of the same
	// name (loadOne's first-write-wins semantics).
	for _, e := range extMgr.LoadExplicit(ctx, args.Exts) {
		fmt.Fprintln(os.Stderr, "extension load:", e)
	}
	if !args.NoExt {
		for _, e := range extMgr.Discover(ctx) {
			fmt.Fprintln(os.Stderr, "extension load:", e)
		}
	}
	// 3s is the per-extension grace for the ready frame; well-behaved
	// extensions release the wait the moment they signal ready.
	extMgr.WaitForReady(3 * time.Second)
	r.MergeExtensionTools(&build.ExtToolAdapter{Mgr: extMgr})
	return extMgr, func() { extMgr.Stop(2 * time.Second) }
}

// skillSnapshot returns a closure that re-discovers the visible SKILL.md skills
// for cwd, mirroring the TUI's SkillSnapshot (cli.go): each call rescans so the
// list reflects edits made during the session, filtering out built-in skills
// (implementation detail hidden from user-facing surfaces — the model still
// sees them via the system-prompt manifest + skill tool). Returns nil under
// --no-skill so the native /skills command reports "no skills". The closure is
// carried on the SessionAgent and consumed by the ACP /skills command.
func (f *acpFactory) skillSnapshot(cwd string) func() []*skills.Skill {
	if f.args.NoSkill {
		return nil
	}
	// Resolve trust once for this cwd so the picker matches what was
	// loaded for the model (project skills hidden while restricted).
	trusted := permissions.ResolveTrustState(cwd, f.args.Trust).IsTrusted()
	return func() []*skills.Skill {
		userHome, _ := os.UserHomeDir()
		list, _ := skills.Discover(config.TervaHome(), cwd, userHome, f.args.WithSkills, !f.args.NoBuiltinSkills,
			skills.Gate{TrustProject: trusted, Disabled: config.ResolveConfig(cwd, trusted).Config.DisableExtensions})
		return skills.VisibleSkills(list)
	}
}

// reloadSkills builds the SessionAgent.ReloadSkills closure: re-run the
// discovery ladder and swap the result into the agent's LIVE skill tool, so a
// SKILL.md written this session is loadable by name without a relaunch.
//
// The tool is looked up on the agent per call rather than captured, for the
// same reason the daemon derives it instead of caching it: build.Resolve mints
// the skill tool, so a held pointer risks writing into an instance the model no
// longer calls.
//
// Trust is re-resolved per call, unlike skillSnapshot's once-at-build verdict.
// /trust works mid-session over ACP, so `terva trust` followed by
// /reload-skills has to bring the project skills in — which is the whole point
// of pairing them. Returns nil under --no-skill, so the command degrades to a
// note.
func (f *acpFactory) reloadSkills(ag *core.Agent, cwd string) func() acp.SkillReloadStats {
	if f.args.NoSkill || ag == nil {
		return nil
	}
	return func() acp.SkillReloadStats {
		userHome, _ := os.UserHomeDir()
		trusted := permissions.ResolveTrustState(cwd, f.args.Trust).IsTrusted()
		list, _ := skills.Discover(config.TervaHome(), cwd, userHome, f.args.WithSkills, !f.args.NoBuiltinSkills,
			skills.Gate{TrustProject: trusted, Disabled: config.ResolveConfig(cwd, trusted).Config.DisableExtensions})

		var before []*skills.Skill
		if t, ok := ag.LookupTool("skill"); ok {
			if tool, _ := t.(*skills.Tool); tool != nil {
				before = tool.Skills()
				tool.SetSkills(list)
			}
		}
		return acp.SkillReloadStats{
			Available: len(skills.VisibleSkills(list)),
			Added:     skills.MissingFrom(list, before),
			Removed:   skills.MissingFrom(before, list),
		}
	}
}

// sessionUpdatedAt returns the session file's last-modified time as an RFC 3339
// (ISO 8601) string for SessionInfo.updatedAt, or "" if it can't be stat'd.
func sessionUpdatedAt(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return info.ModTime().UTC().Format(time.RFC3339)
}
