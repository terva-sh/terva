package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"terva.sh/terva/packages/agent/build"
	"terva.sh/terva/packages/agent/config"
	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/agent/modes"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/lineframe"
	"terva.sh/terva/packages/provider"
)

// runRPCMode implements the JSON-over-stdin/stdout RPC protocol.
//
// Wire format: one JSON object per line in both directions.
//
// Commands (stdin):
//
//	{"id":"1","type":"prompt","message":"hello","images":[]}
//	{"id":"2","type":"abort"}
//	{"id":"3","type":"compact"}
//	{"id":"4","type":"get_state"}
//	{"id":"5","type":"set_model","model":"claude-opus-4-5"}
//	{"id":"6","type":"get_messages"}
//	{"id":"7","type":"clear"}
//	{"id":"8","type":"get_models"}
//
// Responses (stdout): {"type":"response","id":"1","command":"prompt","success":true}
// Events (stdout): one JSON object per AgentEvent (same schema as --json mode).
//
// Auth: if $TERVACORE_RPC_TOKEN is set, the first command must be
// {"type":"hello","token":"..."} or the connection is closed.
func runRPCMode(ctx context.Context, args build.Args, version string) error {
	// When --no-yolo is set there is no interactive prompt to confirm
	// tool calls, so the gate is built with a nil inner Confirmer and
	// refuses every call with a model-readable reason (see
	// core.ConfirmGate.Check). headlessConfirmGate also prints the
	// one-line stderr note. nil when yolo is on (gate.Check on a nil
	// *core.ConfirmGate always allows).
	confirmGate, roSet := build.HeadlessConfirmGate(args, "rpc")
	r, err := build.Resolve(args, true)
	if err != nil {
		return err
	}
	build.WarnRestrictedWorkspace(args, r.Trusted)
	build.WarnPersistentlyUnjailed(args)
	r.AdoptReadOnlySet(roSet)

	// Extensions: same lifecycle as interactive mode, minus the
	// host-hooks integration. Notify/Display calls from extensions
	// emit RPC events instead of TUI lines so any consumer can react.
	extHooks := &rpcExtHooks{}
	extMgr := build.NewExtensionManager(config.TervaHome(), r.CWD, version, r.Provider, r.Model, extHooks)
	extMgr.SetContextDisabled(r.DisableContextExtensions)
	extMgr.SetDisabledExtensions(r.DisableExtensions) // before Discover/LoadExplicit
	extMgr.SetAllowedExtensions(args.WithExtensions)  // --extensions allowlist; --ext paths bypass
	build.WireSessionReader(extMgr, config.TervaHome(), r.CWD)
	extMgr.SetProjectTrusted(r.Trusted) // gate project ext dirs on Workspace Trust
	// Start the subprocesses in the background rather than blocking the whole
	// launch on their handshakes: rpc is a long-lived server a driver spawns
	// and waits on, so up to ~6s of hello + ready grace is latency the driver
	// pays before it can send anything. runPrompt / runCompact join the start
	// below, so the first turn still sees the complete tool set.
	startCtx, cancelStart := context.WithCancel(ctx)
	defer extMgr.Stop(2 * time.Second)
	defer cancelStart() // LIFO: abort an in-flight spawn before tearing down
	extMgr.StartAsync(startCtx, args.Exts, !args.NoExt, 3*time.Second, func(err error) {
		fmt.Fprintln(os.Stderr, "extension load:", err)
	})
	_, stopMCP := build.SetupMCP(ctx, args, &r)
	defer stopMCP()

	ag := r.NewAgent()
	hookEng := build.BuildHookEngine(args, r.Trusted)
	// Canonical tool-call ladder (pre-hooks, confirm gate, extension
	// intercept) — shared with every other mode.
	ag.BeforeToolExecute = build.BuildBeforeToolExecute(ctx, hookEng, confirmGate, extMgr)
	build.WireHostToolDispatcher(ag, extMgr, confirmGate)
	ag.BeforeTurn = func(step int) (bool, string) {
		r := extMgr.InterceptTurnStart(ctx, step)
		return !r.Block, r.Reason
	}
	ag.BeforeAssistantMessage = func(text string) (bool, string, string) {
		r := extMgr.InterceptAssistantMessage(ctx, text)
		if r.Block {
			return false, r.Reason, ""
		}
		return true, "", r.ReplaceText
	}
	ag.BeforeUserMessage = func(text string) (bool, string, string) {
		r := extMgr.InterceptUserMessage(ctx, text)
		if r.Block {
			return false, r.Reason, ""
		}
		return true, "", r.ReplaceText
	}
	wsObserve := build.WorkspaceChangeObserver(tools.NewWorkspaceDiffer(workspaceRootFn(r.Sandbox, r.CWD)), extMgr)
	// Registration order is delivery order.
	ag.AddEventObserver(wsObserve)
	ag.AddEventObserver(func(ev core.AgentEvent) { build.FanoutAgentEvent(extMgr, ev) })
	ag.AddEventObserver(func(ev core.AgentEvent) { build.ObserveAgentEventForHooks(hookEng, ev) })
	// Inject extensions' live context cards into the model each turn (live
	// provider + sizing twin; ext context before the tail so PHI stays last).
	build.WireExtEphemeral(ag, extMgr.EphemeralContext)
	build.WireTasksEphemeral(ag, r.Tasks)
	// Re-prompt once at close if an extension flags open work or a task is open.
	ag.AddContinuationGate(build.OpenWorkGate(extMgr, r.Tasks))

	// /reload-ext hot-reload callback (also triggered via rpc
	// `reload_ext` if/when added). Rebuilds the tool registry on the
	// current agent so freshly-registered extension tools become
	// callable without restarting the rpc process.
	adapter := &build.ExtToolAdapter{Mgr: extMgr}
	mergeExtTools := func() {
		resolved, err := build.Resolve(args, true)
		if err != nil {
			return
		}
		// Merge into the LIVE gate's read-only set: MergeToolsForMode records
		// each read_only tool's name in whichever set the Resolved carries, and
		// a fresh Resolve carries a fresh one — without this the gate stops
		// auto-allowing an extension's read-only tools after a rebuild.
		resolved.AdoptReadOnlySet(roSet)
		resolved.MergeExtensionTools(adapter)
		ag.SetTools(resolved.ToolRegistry)
	}
	extMgr.SetOnReload(mergeExtTools)

	// Session persistence & resume. rpc mode is stateless by default — its
	// long-standing contract ("RPC persists no session") — so a run with no
	// --session keeps sess nil and writes nothing, exactly as before. A driver
	// that passes --session <path> opts into a persistent, RESUMABLE session:
	// openOrCreateSession restores the prior transcript onto the agent (or creates
	// the file fresh on first run), and WireHeadlessSessionPersist streams every
	// message, usage row, and post-compaction transcript to disk as it happens.
	// So a worker whose process died is revived with its conversation intact
	// rather than blank — the same resume the native --swarm-agent child has, now
	// on the rpc carrier. Persistence is observer-driven, so the turn handlers
	// (runPrompt/runCompact) need no changes.
	var sess *core.Session
	if args.Session != "" {
		s, serr := openOrCreateSession(args, r, ag, version)
		if serr != nil {
			return serr
		}
		sess = s
	}
	if sess != nil {
		defer sess.Close()
		build.WireHeadlessSessionPersist(ag, sess)
	}
	// Everything that needs a live extension waits for the background start:
	// fold their tools into the agent, then announce the session with its real
	// identity (a nil session emits a bare session_start, as before). Both land
	// before the first turn because runPrompt / runCompact block on extReady.
	extReady := make(chan struct{})
	go func() {
		defer close(extReady)
		extMgr.AwaitStarted(ctx)
		if ctx.Err() != nil {
			return
		}
		if extMgr.Count() > 0 {
			mergeExtTools()
		}
		build.EmitSessionStart(extMgr, sess)
	}()

	server := &rpcServer{
		ctx:      ctx,
		args:     args,
		agent:    ag,
		provider: r.Provider,
		model:    r.Model,
		out:      os.Stdout,
		version:  version,
		extReady: extReady,
	}
	extHooks.server = server
	// Fill the confirm gate's nil-inner hole with the rpc carrier when the driver
	// opted in: a tool that needs confirmation now asks over the wire instead of
	// being refused outright. Only when a gate exists (a non-yolo mode built one)
	// and only on opt-in — a driver that never answers must keep the safe
	// refuse-by-default rather than hang. See core.ConfirmGate.Check.
	if args.RPCApprovals && confirmGate != nil {
		confirmGate.SetConfirmer(server)
	}
	// The MCP approval carrier (the terva:portable worker): route the confirm gate
	// through the bridge at --approval-socket, using terva's OWN MCP client — the
	// config-opaque sibling of --rpc-approvals. Both fill the same gate hole, and a
	// backend sets one or the other, never both. Fail closed: if the bridge won't
	// start, leave the gate's refuse-by-default rather than opening it.
	if args.ApprovalSocket != "" && confirmGate != nil {
		exe, exeErr := os.Executable()
		if exeErr != nil {
			fmt.Fprintln(os.Stderr, "approval bridge: locate terva:", exeErr)
		} else if confirmer, stop, berr := startBridgeConfirmer(ctx, exe, args.ApprovalSocket, r.CWD); berr != nil {
			fmt.Fprintln(os.Stderr, "approval bridge:", berr)
		} else {
			defer stop()
			confirmGate.SetConfirmer(confirmer)
		}
	}
	// The HTTP approval carrier (a remote orchestrator): route the confirm gate
	// through a Streamable-HTTP MCP permission endpoint. The networked sibling of
	// --approval-socket, and a backend sets one or the other — so this only runs
	// when no local socket carrier claimed the gate. Fail closed: if the endpoint
	// won't start (bad descriptor, unreachable, or no http transport in this
	// build), leave the gate's refuse-by-default rather than opening it.
	if args.ApprovalHTTP != "" && args.ApprovalSocket == "" && confirmGate != nil {
		if confirmer, stop, herr := startHTTPConfirmer(ctx, args.ApprovalHTTP, r.CWD); herr != nil {
			fmt.Fprintln(os.Stderr, "approval http:", herr)
		} else {
			defer stop()
			confirmGate.SetConfirmer(confirmer)
		}
	}
	return server.run(os.Stdin)
}

// rpcExtHooks implements extensions.HostHooks for the headless RPC
// loop. Notify and Display surface as `event` frames so any RPC
// client can render them; Submit and Insert are no-ops because the
// RPC loop has no editor and the prompt comes from the client.
type rpcExtHooks struct {
	server *rpcServer
}

func (h *rpcExtHooks) Notify(extName, level, message string) {
	if h.server != nil {
		h.server.writeEvent(map[string]any{
			"type":      "ext_notify",
			"extension": extName,
			"level":     level,
			"message":   message,
		})
	}
}
func (h *rpcExtHooks) Display(extName, text string) {
	if h.server != nil {
		h.server.writeEvent(map[string]any{
			"type":      "ext_display",
			"extension": extName,
			"text":      text,
		})
	}
}
func (h *rpcExtHooks) ClearNotes(extName string) {
	if h.server != nil {
		h.server.writeEvent(map[string]any{
			"type":      "ext_clear_notes",
			"extension": extName,
		})
	}
}
func (h *rpcExtHooks) Submit(string)                                                           {} // ignored in rpc mode
func (h *rpcExtHooks) SubmitSlash(string)                                                      {} // ignored in rpc mode
func (h *rpcExtHooks) Insert(string)                                                           {} // ignored in rpc mode
func (h *rpcExtHooks) OpenPanel(string, extproto.PanelSpec)                                    {}
func (h *rpcExtHooks) UpdatePanel(string, string, string, []string, string, []extproto.Widget) {}
func (h *rpcExtHooks) ClosePanel(string, string)                                               {}
func (h *rpcExtHooks) RefreshStatus()                                                          {} // ignored in rpc mode
func (h *rpcExtHooks) RefreshContext()                                                         {} // prompt is fixed for the run
func (h *rpcExtHooks) RefreshTools()                                                           {} // tool set is fixed for the run

type rpcServer struct {
	ctx      context.Context
	args     build.Args
	agent    *core.Agent
	provider string
	model    string
	out      io.Writer
	version  string

	// extReady is closed once the background extension start has finished and
	// its tools have been merged into the agent. The turn verbs wait on it so
	// a driver that prompts the instant the process answers `hello` still gets
	// the complete tool set on turn 1. Nil in test fixtures.
	extReady <-chan struct{}

	writeMu      sync.Mutex
	turnMu       sync.Mutex // serialises one prompt at a time
	activeCancel context.CancelFunc
	authed       bool

	// inFlight tracks long-running command goroutines so run() can
	// wait for them before returning when stdin closes. Without this,
	// piping a single 'prompt' command into 'terva rpc' would race the
	// process exit against the agent loop and the prompt would never
	// produce output.
	inFlight sync.WaitGroup

	// asks correlates an outstanding `ask` frame with the goroutine blocked
	// in Confirm: the matching `approve` command delivers the decision by ask
	// id. Used only when the run opted into the ask/approve carrier
	// (--rpc-approvals); otherwise Confirm is never wired and this stays
	// empty. pendMu guards only the id counter.
	pendMu sync.Mutex
	askSeq int
	asks   core.ParkTable[core.ConfirmDecision]
}

// rpcAuthToken returns the embedder-supplied RPC auth token. Both
// spellings are honored: third-party embedders spawn this binary with
// ZOTCORE_RPC_TOKEN in the child env, and breaking them is a wire- // rename:keep
// compat violation, not a rename (docs/plans/rename-terva.md).
func rpcAuthToken() string {
	if v := os.Getenv("TERVACORE_RPC_TOKEN"); v != "" {
		return v
	}
	return os.Getenv("ZOTCORE_RPC_TOKEN") // rename:keep — embedder compat
}

// rpcMaxFrameBytes bounds one inbound NDJSON command frame — the historical
// 16 MiB Scanner ceiling, now enforced recoverably by lineframe.
const rpcMaxFrameBytes = 16 << 20 // 16 MiB

// run reads NDJSON commands from in and dispatches them. Returns when
// in is closed AND every in-flight long-running command (prompt /
// compact) has finished, so a quick `echo cmd | terva rpc` invocation
// still produces full output before the process exits.
func (s *rpcServer) run(in io.Reader) error {
	requireToken := rpcAuthToken() != ""
	s.authed = !requireToken

	// Read NDJSON through lineframe at a 16 MiB ceiling: an oversized command
	// frame from an embedder is skipped (reported back as an error frame) and
	// the stream continues, rather than tearing the RPC server down the way
	// bufio.Scanner's ErrTooLong would.
	fr := lineframe.NewReader(in, rpcMaxFrameBytes, func(msg string) {
		s.writeError("", "", msg)
	})
	var readErr error
	for {
		frame, err := fr.Read()
		if err != nil {
			if err != io.EOF {
				readErr = err // a clean EOF stays nil, matching Scanner.Err
			}
			break
		}
		line := strings.TrimSpace(string(frame))
		if line == "" {
			continue
		}
		var head struct {
			Type string `json:"type"`
			ID   string `json:"id,omitempty"`
		}
		if err := json.Unmarshal([]byte(line), &head); err != nil {
			s.writeError("", "", fmt.Sprintf("malformed json: %v", err))
			continue
		}
		if !s.authed {
			if head.Type != "hello" {
				s.writeError(head.ID, head.Type, "auth required: send hello with token first")
				continue
			}
			var hello struct {
				Token string `json:"token"`
			}
			_ = json.Unmarshal([]byte(line), &hello)
			if hello.Token != rpcAuthToken() {
				s.writeError(head.ID, head.Type, "invalid token")
				return fmt.Errorf("rpc: bad auth token")
			}
			s.authed = true
			s.writeResponse(head.ID, head.Type, map[string]any{
				"protocol_version": 1,
				"version":          s.version,
				"provider":         s.provider,
				"model":            s.model,
			})
			continue
		}
		s.dispatch(head.Type, head.ID, []byte(line))
	}
	s.inFlight.Wait()
	return readErr
}

// dispatch routes a command. Long-running commands (prompt, compact)
// run on their own goroutine so the read loop stays responsive.
func (s *rpcServer) dispatch(cmd, id string, raw []byte) {
	switch cmd {
	case "hello":
		s.writeResponse(id, cmd, map[string]any{
			"protocol_version": 1,
			"version":          s.version,
			"provider":         s.provider,
			"model":            s.model,
		})
	case "prompt":
		var req struct {
			Message string `json:"message"`
			Images  []struct {
				MimeType string `json:"mime_type"`
				Data     []byte `json:"data"`
			} `json:"images"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			s.writeError(id, cmd, err.Error())
			return
		}
		s.inFlight.Add(1)
		go func() {
			defer s.inFlight.Done()
			s.runPrompt(id, req.Message, req.Images)
		}()

	case "abort":
		if c := s.takeCancel(); c != nil {
			c()
		}
		// An aborted turn must also unpark any approval still waiting on the
		// driver: Confirm blocks outside the turn's context (BeforeToolExecute
		// is ctx-free), so without this an abort left the prompt goroutine
		// parked until the driver answered or the process ended.
		s.asks.CancelAll(core.ConfirmDecision{Allow: false, Reason: "the turn was aborted before this approval was answered (fail closed)"})
		s.writeResponse(id, cmd, nil)

	case "approve":
		// The driver's answer to an `ask` frame. `id` (the command id) is the
		// ask id being answered — the correlation the ask frame carried. The
		// decision fields mirror core.ConfirmDecision so a driver can also grant
		// a session-scoped "always" (remember) without a second round trip.
		var req struct {
			Allow        bool   `json:"allow"`
			Reason       string `json:"reason"`
			RememberTool bool   `json:"remember_tool"`
			RememberAll  bool   `json:"remember_all"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			s.writeError(id, cmd, err.Error())
			return
		}
		ok := s.asks.Deliver(id, core.ConfirmDecision{
			Allow:        req.Allow,
			Reason:       req.Reason,
			RememberTool: req.RememberTool,
			RememberAll:  req.RememberAll,
		})
		if !ok {
			s.writeError(id, cmd, "no pending approval with id "+id)
			return
		}
		s.writeResponse(id, cmd, nil)

	case "compact":
		s.inFlight.Add(1)
		go func() {
			defer s.inFlight.Done()
			s.runCompact(id)
		}()

	case "get_state":
		s.writeResponse(id, cmd, s.snapshotState())

	case "get_messages":
		s.writeResponse(id, cmd, map[string]any{
			"messages": messagesToJSON(s.agent.Messages()),
		})

	case "clear":
		s.agent.SetMessages(nil)
		s.writeResponse(id, cmd, nil)

	case "set_model":
		var req struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			s.writeError(id, cmd, err.Error())
			return
		}
		next, err := provider.FindModel(s.provider, req.Model)
		if err != nil {
			s.writeError(id, cmd, err.Error())
			return
		}
		// In-place swap reuses the existing client, which captured its
		// base URL immutably at construction. A per-model models.json
		// baseUrl can route two models of the same provider to different
		// backends; swapping the id alone would keep firing requests at
		// the previous endpoint. The rpc server has no rebuild path, so
		// reject the swap and tell the client to restart the session.
		if cur, curErr := provider.FindModel(s.provider, s.model); curErr == nil && cur.BaseURL != next.BaseURL {
			s.writeError(id, cmd, fmt.Sprintf("model %q routes to a different endpoint; restart the rpc session to switch", req.Model))
			return
		}
		s.agent.SetModel(req.Model)
		s.model = req.Model
		s.writeResponse(id, cmd, map[string]any{"model": req.Model})

	case "get_models":
		out := []map[string]any{}
		for _, m := range provider.ModelsForProvider(s.provider) {
			out = append(out, map[string]any{
				"id":                     m.ID,
				"provider":               m.Provider,
				"context_window":         m.ContextWindow, // model max
				"desired_context_window": m.DesiredContextWindow,
				"context_surcharge_at":   m.ContextSurchargeAt,
				"max_output":             m.MaxOutput,
				"reasoning":              m.Reasoning,
			})
		}
		s.writeResponse(id, cmd, map[string]any{"models": out})

	case "ping":
		s.writeResponse(id, cmd, map[string]any{"pong": true})

	default:
		s.writeError(id, cmd, "unknown command")
	}
}

// awaitExtensions blocks until the background extension start has finished and
// its tools have been merged into the agent, or ctx is done. Returns at once
// on a fixture built without the async start.
func (s *rpcServer) awaitExtensions(ctx context.Context) {
	if s.extReady == nil {
		return
	}
	select {
	case <-s.extReady:
	case <-ctx.Done():
	}
}

// runPrompt executes a single prompt turn and streams events out.
// Holds turnMu so a second concurrent prompt blocks until this one
// finishes; the user can abort with the abort command.
func (s *rpcServer) runPrompt(id, message string, images []struct {
	MimeType string `json:"mime_type"`
	Data     []byte `json:"data"`
}) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()

	subCtx, cancel := context.WithCancel(s.ctx)
	s.setCancel(cancel)
	defer s.setCancel(nil)

	s.writeResponse(id, "prompt", map[string]any{"started": true})

	// Pay the background extension start's debt before the agent pins its tool
	// set for this turn. After the ack, so the driver knows its prompt was
	// accepted rather than watching a silent socket; on the dispatch goroutine,
	// so the read loop stays responsive and `abort` still works while we wait.
	s.awaitExtensions(subCtx)

	imgs := make([]provider.ImageBlock, 0, len(images))
	for _, im := range images {
		imgs = append(imgs, provider.ImageBlock{MimeType: im.MimeType, Data: im.Data})
	}

	// PromptWithPolicy adds the core turn policy: pre-turn compaction
	// for an over-threshold transcript and one compact-and-retry on
	// HTTP 413. Compaction surfaces on the stream as compact_start /
	// compact_end events.
	err := s.agent.PromptWithPolicy(subCtx, message, imgs, func(ev core.AgentEvent) {
		// EvDone is emitted by the agent loop and we re-emit our own
		// 'done' below; suppressing it here avoids duplicate frames.
		if _, ok := ev.(core.EvDone); ok {
			return
		}
		s.writeEvent(modes.EventToJSON(ev))
	})
	// Don't emit a stand-alone error event for cancellation; the prior
	// turn_end with stop=aborted already carries that signal.
	if err != nil && !errors.Is(err, context.Canceled) {
		// Canonical error-event shape (core.WireEvent): the message
		// lives under "error", matching --json and the SDK.
		s.writeEvent(map[string]any{"type": "error", "error": err.Error()})
	}
	// Post-turn housekeeping for this long-lived session: when the
	// finished turn pushed context past the auto-compact threshold,
	// condense now — inside the request lifecycle, before `done` — so
	// the NEXT prompt doesn't pay the latency or bounce off the
	// window. A failed auto-compact is non-fatal: the turn itself
	// succeeded, so the failure rides the compact_end event and the
	// client decides whether to /compact manually.
	if err == nil && subCtx.Err() == nil && s.agent.ShouldAutoCompact(core.AutoCompactThreshold) && s.agent.CanCompact(core.AutoCompactKeepTail) {
		start := core.EvCompactStart{Reason: "context near limit"}
		s.writeEvent(modes.EventToJSON(start))
		s.agent.EmitLifecycle(start) // reach extensions, not just the RPC client
		end := core.EvCompactEnd{}
		if _, cerr := s.agent.Compact(subCtx, core.AutoCompactKeepTail, nil); cerr != nil && !errors.Is(cerr, context.Canceled) && !errors.Is(cerr, core.ErrNothingToCompact) {
			end.Err = cerr.Error()
		}
		s.writeEvent(modes.EventToJSON(end))
		s.agent.EmitLifecycle(end)
	}
	s.writeEvent(map[string]any{"type": "done"})
}

// runCompact mirrors runPrompt's terminal-event contract for an explicit
// compact request: it emits at most one result event and then exactly one
// terminal "done" on every outcome, so a generic RPC loop can key on "done"
// for prompts and compactions alike. compact_done carries the (possibly empty)
// summary on success/no-op; a real failure rides the canonical "error" field
// (matching runPrompt, --json, and the SDK — not the old "message"); a
// cancellation emits no result event (the prior turn signal already covers it)
// but still terminates with "done".
func (s *rpcServer) runCompact(id string) {
	s.turnMu.Lock()
	defer s.turnMu.Unlock()

	subCtx, cancel := context.WithCancel(s.ctx)
	s.setCancel(cancel)
	defer s.setCancel(nil)

	s.writeResponse(id, "compact", map[string]any{"started": true})

	// Same barrier as runPrompt, in the same place: a compaction is a model
	// call too, and an extension's intercept hooks must be live before it goes
	// out.
	s.awaitExtensions(subCtx)
	// res.Summary, not res: the event's "summary" field is a string, and
	// map[string]any would have accepted the whole struct without a murmur
	// from the compiler — silently turning a documented string field into an
	// object for every --json consumer.
	res, err := s.agent.Compact(subCtx, core.AutoCompactKeepTail, nil)
	switch {
	case err == nil:
		// strategy/usage ride the event because RPC is the programmatic driver —
		// and unlike the TUI it persists no session, so without them there is no
		// way to observe WHICH summarizer ran or whether its cache actually hit.
		// That is the one thing a cache-aware compaction cannot tell you by
		// succeeding: a prefix match that missed produces the same summary and
		// the same transcript, and differs only in these numbers.
		ev := map[string]any{
			"type":     "compact_done",
			"summary":  res.Summary,
			"strategy": string(res.Strategy),
			"usage": map[string]any{
				"input":       res.Usage.InputTokens,
				"output":      res.Usage.OutputTokens,
				"cache_read":  res.Usage.CacheReadTokens,
				"cache_write": res.Usage.CacheWriteTokens,
				"cost_usd":    res.Usage.CostUSD,
			},
		}
		if res.FallbackReason != "" {
			ev["fallback_reason"] = res.FallbackReason
		}
		s.writeEvent(ev)
	case errors.Is(err, core.ErrNothingToCompact):
		// Explicit /compact with nothing to summarize — benign no-op.
		s.writeEvent(map[string]any{"type": "compact_done", "summary": ""})
	case errors.Is(err, context.Canceled):
		// Cancelled mid-compaction; terminate without a result event.
	default:
		s.writeEvent(map[string]any{"type": "error", "error": err.Error()})
	}
	s.writeEvent(map[string]any{"type": "done"})
}

// snapshotState builds the get_state response.
func (s *rpcServer) snapshotState() map[string]any {
	cum := s.agent.Cost()
	return map[string]any{
		"provider":      s.provider,
		"model":         s.model,
		"cwd":           s.args.CWD,
		"message_count": len(s.agent.Messages()),
		"busy":          s.busy(),
		"usage": map[string]any{
			"input":       cum.InputTokens,
			"output":      cum.OutputTokens,
			"cache_read":  cum.CacheReadTokens,
			"cache_write": cum.CacheWriteTokens,
			"cost_usd":    cum.CostUSD,
		},
	}
}

// ---- write helpers (single-line JSON, mutex-guarded) ----

func (s *rpcServer) writeResponse(id, cmd string, data any) {
	frame := map[string]any{
		"type":    "response",
		"command": cmd,
		"success": true,
	}
	if id != "" {
		frame["id"] = id
	}
	if data != nil {
		frame["data"] = data
	}
	s.write(frame)
}

func (s *rpcServer) writeError(id, cmd, msg string) {
	frame := map[string]any{
		"type":    "response",
		"command": cmd,
		"success": false,
		"error":   msg,
	}
	if id != "" {
		frame["id"] = id
	}
	s.write(frame)
}

func (s *rpcServer) writeEvent(payload map[string]any) {
	s.write(payload)
}

func (s *rpcServer) write(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.out.Write(b)
	_, _ = s.out.Write([]byte("\n"))
}

func (s *rpcServer) setCancel(c context.CancelFunc) {
	s.writeMu.Lock()
	s.activeCancel = c
	s.writeMu.Unlock()
}

func (s *rpcServer) takeCancel() context.CancelFunc {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	c := s.activeCancel
	s.activeCancel = nil
	return c
}

// busy reports whether a prompt/compact turn is in flight. Reads
// activeCancel under writeMu so it can't race the setCancel/takeCancel
// writes that run on the prompt goroutine.
func (s *rpcServer) busy() bool {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.activeCancel != nil
}

// Confirm implements core.Confirmer over the rpc wire. It emits an `ask` frame
// naming the tool and its preview, then BLOCKS until the driver answers with a
// matching `approve` command (or the server's context is cancelled). This is the
// fill for rpc.go's historical nil-inner gate — the Confirmer-shaped hole whose
// only behaviour was to refuse — and wiring it retires refuse-by-default for any
// embedder that opts in with --rpc-approvals.
//
// It runs on the prompt goroutine (a tool call inside PromptWithPolicy), never
// on the read loop, so blocking here leaves the read loop free to receive the
// `approve` command that unblocks it.
func (s *rpcServer) Confirm(toolName, preview string) core.ConfirmDecision {
	s.pendMu.Lock()
	s.askSeq++
	id := fmt.Sprintf("ask-%d", s.askSeq)
	s.pendMu.Unlock()
	ch, release, _ := s.asks.Park(id) // monotonic ids never collide
	defer release()
	s.write(map[string]any{
		"type":    "ask",
		"id":      id,
		"tool":    toolName,
		"preview": preview,
	})
	select {
	case d := <-ch:
		return d
	case <-s.ctx.Done():
		// The session is going away; deny so the tool call unwinds with a
		// model-readable reason rather than hanging the shutdown.
		return core.ConfirmDecision{Allow: false, Reason: "approval request cancelled (session ending)"}
	}
}

// messagesToJSON serialises a transcript using the same schema as the
// --json event mode for cross-format consistency.
func messagesToJSON(msgs []provider.Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, map[string]any{
			"role":    string(m.Role),
			"content": modes.ContentToJSON(m.Content),
			"time":    m.Time,
		})
	}
	return out
}
