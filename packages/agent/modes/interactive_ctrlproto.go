package modes

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// This file is the TUI's ctrlproto carrier path (--tui-ctrlproto;
// docs/proposals/tui-on-ctrlproto.md). Where the legacy path drives a
// *core.Agent directly and consumes typed core.AgentEvent through a synchronous
// sink, this path drives the in-process ctrlproto WorkspaceService and consumes
// the wire event stream (core.WireEvent + control events) off a reliable
// subscription. handleWireEvent is the wire twin of handleEvent: it makes the
// same rendering-state mutations, keyed on the string WireEvent.Type instead of
// the Go event type, reconstructing from the wire what the typed events carried.
//
// It consumes the actual wire vocabulary (not an in-process-only typed channel)
// deliberately: that is what lets the same TUI later drive a *remote* daemon over
// a serialized carrier. Any field the wire drops is closed in the wire vocabulary
// (Stage 0), not smuggled through a side channel.

// carrierSession returns the id of the workspace session the TUI is currently
// bound to. Mutable state (SwitchCarrierSession re-points it), so every read
// goes through here rather than touching cfg.CarrierSession directly.
func (i *Interactive) carrierSession() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.cfg.CarrierSession
}

// CarrierSessionID is the exported read for the host's session-op closures
// (e.g. CurrentSessionPath in the ctrlproto entry point).
func (i *Interactive) CarrierSessionID() string { return i.carrierSession() }

// runCarrierLoop is the ctrlproto TUI's event pump: a reliable subscription to
// the CURRENT session's stream, feeding every event through
// handleCarrierEvent. It replaces the legacy path's synchronous per-turn sink.
// The loop is a supervisor: a session switch cancels just the subscription
// (carrierPumpCancel) and the loop re-subscribes to the new current session;
// only ctx ending (or a subscribe failure) exits the pump itself.
func (i *Interactive) runCarrierLoop(ctx context.Context) {
	for ctx.Err() == nil {
		subCtx, cancel := context.WithCancel(ctx)
		i.mu.Lock()
		i.carrierPumpCancel = cancel
		sess := i.cfg.CarrierSession
		i.mu.Unlock()
		ch, err := i.cfg.Carrier.SubscribeReliable(subCtx, sess)
		if err != nil {
			cancel()
			i.setStatusErr(i18n.T("control-plane subscribe failed: %s", err.Error()))
			i.invalidate()
			return
		}
		for ev := range ch {
			if i.carrierSession() != sess {
				// A switch was committed while events were still buffered on
				// the old subscription: the channel drains to EOF, but its
				// tail belongs to the previous binding — don't paint it into
				// the fresh session's view.
				continue
			}
			i.handleCarrierEvent(ev)
			i.invalidate()
		}
		cancel()
	}
}

// carrierCancel returns the cancel func a carrier-mode turn slot stores: it
// routes the existing esc/ctrl+c plumbing (cancelActive) to the service's
// Cancel verb instead of a local turn context. The session is captured NOW so
// a later switch can't misdirect the cancel.
func (i *Interactive) carrierCancel() context.CancelFunc {
	c, sess := i.cfg.Carrier, i.carrierSession()
	return func() { _ = c.Cancel(context.Background(), sess) }
}

// SwitchCarrierSession re-points the TUI at another workspace session: the
// session-group half of the migration (tui-on-ctrlproto.md Stage 2). It
// materializes the target through the service, swaps the crutch agent, drops
// state owned by the old session (pending dialog round-trips, the local turn
// slot — the old session's turn keeps running daemon-side; this TUI just
// stops watching it), commits the new binding, and kicks the pump so it
// re-subscribes. The caller (startNewSession / applySessionSelection) resets
// the per-session view state afterwards by reading the swapped-in agent.
func (i *Interactive) SwitchCarrierSession(id string) error {
	c := i.cfg.Carrier
	if c == nil {
		return errors.New("not running on a ctrlproto carrier")
	}
	info, err := c.ResumeSession(context.Background(), id)
	if err != nil {
		return err
	}
	ag, id, err := c.AgentFor(id)
	if err != nil {
		return err
	}
	// Old-session teardown: refuse pending dialogs (their forward goroutines
	// answer the OLD session — captured at enqueue — where first-answer-wins
	// makes a late refusal harmless) and release the local slot.
	i.confirmDialog.CancelAll("session switched")
	i.questionDialog.CancelAll()
	i.turns.releaseCarrier()
	i.mu.Lock()
	i.carrierPerm = map[string]*confirmRequest{}
	i.carrierAsk = map[string]*questionRequest{}
	i.cfg.CarrierSession = id
	// Drop the old session's transcript now; the new binding's snapshot
	// refills it (a beat of empty beats a beat of foreign history).
	i.carrierMessages = nil
	i.carrierMessagesRev++
	if info.Provider != "" {
		i.cfg.Provider = info.Provider
	}
	if info.Model != "" {
		i.cfg.Model = info.Model
	}
	kick := i.carrierPumpCancel
	i.mu.Unlock()
	i.turns.SetAgent(ag)
	if kick != nil {
		kick() // the supervisor re-subscribes to the new binding
	}
	return nil
}

// handleCarrierEvent applies one stream event: turn lifecycle and control
// events are handled here; everything else is a conversation event and falls
// through to handleWireEvent (the rendering twin of handleEvent).
func (i *Interactive) handleCarrierEvent(ev ctrlproto.Event) {
	switch ev.Type {
	case "turn_start":
		// A turn this client didn't dispatch (a daemon queue restart, another
		// device): claim the local slot and reset the per-turn UI, the same
		// state our own dispatch path arms. Per-step turn_starts of a running
		// turn no-op inside reclaimCarrier.
		if i.turns.reclaimCarrier(i.carrierCancel()) {
			i.resetTurnUI()
		}
	case "done":
		// The workspace's guaranteed turn-over signal (every path, including
		// errors and cancels; duplicates possible — releaseCarrier is
		// idempotent). Status/queue/persistence are daemon-side or handled by
		// the accompanying error/turn_end events.
		i.turns.releaseCarrier()
	case "user_message":
		// The daemon transcript grew: mirror it into the pump transcript
		// (synthetic host injections included — they're transcript rows too).
		i.appendCarrierMessage(ev.Message)
		// Track the running turn's prompt for the rescue picker — covers
		// turns this client didn't dispatch (daemon queue restarts, another
		// device). A different text than the local stash means a foreign
		// turn; its images ride the event too when the carrier delivers the
		// full wire form (in-process always; serialized ones per the
		// image-data feature), so a rescue retry keeps the attachments.
		if ev.Message != nil && !ev.Message.Synthetic {
			if text := wireBlocksText(ev.Message.Content); text != "" {
				i.mu.Lock()
				if text != i.carrierLastPrompt {
					i.carrierLastPrompt = text
					i.carrierLastImages = wireImageBlocks(ev.Message.Content)
				}
				i.mu.Unlock()
			}
		}
	case "error":
		// The turn failed (broadcast before the trailing done). Recoverable
		// provider failures open the rescue picker — model-switch-and-retry,
		// reconstructed from the wire error text (ClassifyRecoverable's prose
		// heuristics) — everything else lands on the status banner.
		err := errors.New(ev.Error)
		if ok, reason := core.ClassifyRecoverable(err); ok {
			i.mu.Lock()
			prompt, images := i.carrierLastPrompt, i.carrierLastImages
			prov, model := i.cfg.Provider, i.cfg.Model
			// Suppress the red banner — the rescue dialog surfaces the failure.
			i.statusErr = ""
			i.mu.Unlock()
			fprov := core.ExtractFailedProvider(err)
			if fprov == "" {
				fprov = prov
			}
			i.openRescueDialog(prov, fprov, model, reason, prompt, images)
			return
		}
		i.mu.Lock()
		i.statusErr = ev.Error
		i.statusOK = ""
		i.mu.Unlock()
	case "compact_start":
		// A daemon-side policy compaction (pre-turn threshold or a 413 retry;
		// PromptWithPolicy). Fires while our dispatch holds the local slot.
		i.turns.markCompacting(true)
		note, done := noteCondensingBeforeSend, i18n.T("context auto-compacted; sending your last message")
		if strings.Contains(ev.Text, "too large") {
			note, done = noteCondensingBeforeRetry, i18n.T("context auto-compacted; retrying your last message")
		}
		i.mu.Lock()
		i.spin.StartFixed(i18n.T("condensing history"))
		i.statusErr = ""
		i.extNotes = append(i.extNotes, autoCompactNoteLine(i.cfg.Theme, i18n.T(note)))
		i.pendingPostCompactNote = done
		i.mu.Unlock()
	case "compact_end":
		i.turns.markCompacting(false)
		i.mu.Lock()
		if ev.Error != "" {
			i.statusErr = i18n.T("compaction failed: %s", ev.Error)
			i.statusOK = ""
		} else {
			// The transcript was replaced under the renderer: drop the tool
			// overlay + caches like the legacy post-compact cleanup.
			i.statusErr = ""
			i.statusOK = i.pendingPostCompactNote
			i.extNotes = stripAutoCompactNotes(i.extNotes)
			i.lastCtxInput = 0
			i.toolCalls = map[string]*tui.ToolCallView{}
			i.toolOrder = nil
			i.turns.ResetGates()
			i.view.InvalidateRenderCache()
		}
		i.pendingPostCompactNote = ""
		i.spin.Start() // back to the normal spinner for the turn that follows
		i.mu.Unlock()
	case ctrlproto.EventPermissionRequest:
		if ev.Permission != nil {
			i.enqueueCarrierPermission(*ev.Permission)
		}
	case ctrlproto.EventPermissionResolved:
		if ev.Resolved != nil {
			i.dismissCarrierPermission(ev.Resolved.CallID)
		}
	case ctrlproto.EventAskRequest:
		if ev.Ask != nil {
			i.enqueueCarrierAsk(*ev.Ask)
		}
	case ctrlproto.EventAskResolved:
		if ev.Resolved != nil {
			i.dismissCarrierAsk(ev.Resolved.AskID)
		}
	case ctrlproto.EventReplayState:
		// A replay session's transport changed (position/playing/speed).
		// Stash it for the transport keys and the status-bar scrubber.
		if ev.Replay != nil {
			i.mu.Lock()
			i.replayState = *ev.Replay
			i.mu.Unlock()
			i.invalidate()
		}
	case ctrlproto.EventSnapshot:
		// First event on every (re)subscribe, and the daemon re-broadcasts
		// one whenever the transcript is replaced under every client
		// (compact, auto-compact, clear). It is the transcript's
		// authoritative resync point — replace the pump copy wholesale —
		// plus the live-turn restore: the busy slot and any round-trips
		// parked mid-turn, which matters when switching to a busy session.
		if ev.Snapshot == nil {
			return
		}
		i.setCarrierTranscript(ev.Snapshot.Messages)
		if ev.Snapshot.Busy {
			i.turns.reclaimCarrier(i.carrierCancel())
		}
		for _, p := range ev.Snapshot.Permissions {
			i.enqueueCarrierPermission(p)
		}
		for _, a := range ev.Snapshot.Asks {
			i.enqueueCarrierAsk(a)
		}
		i.refreshCarrierApprovalMode()
	case ctrlproto.EventNotice:
		if ev.Notice == nil {
			return
		}
		i.mu.Lock()
		if ev.Notice.Level == "error" {
			i.statusErr = ev.Notice.Text
			i.statusOK = ""
		} else {
			i.statusOK = ev.Notice.Text
			i.statusErr = ""
		}
		i.mu.Unlock()
	case ctrlproto.EventSessionUpdated:
		// The session's metadata moved (a model switch from another client, a
		// settled auto-title): keep the status bar's provider/model truthful.
		if ev.Info == nil || ev.Info.ID != i.carrierSession() {
			return
		}
		i.mu.Lock()
		if ev.Info.Provider != "" {
			i.cfg.Provider = ev.Info.Provider
		}
		if ev.Info.Model != "" {
			i.cfg.Model = ev.Info.Model
		}
		i.mu.Unlock()
	case ctrlproto.EventSurfaceUpdated:
		// The settings surface carries the approval-mode badge; the tasks
		// surface backs the /swarm dashboard's cached snapshot (and the
		// status bar's swarm segment); other panes re-fetch on open (no
		// pinned pane in the TUI yet).
		switch ev.SurfaceID {
		case "settings":
			i.refreshCarrierApprovalMode()
		case "tasks":
			i.invalidateCarrierTasks()
		}
	case ctrlproto.EventQueueUpdated,
		ctrlproto.EventSurfacesChanged,
		ctrlproto.EventLocaleChanged:
		// Queue chips render from the in-process agent for now (the crutch);
		// surfaces/locale are Stage-3 consumers. The pump's invalidate
		// already repaints.
	default:
		i.handleWireEvent(ev)
	}
}

// startTurnCarrier dispatches a prompt through the WorkspaceService. The
// wsSession is the busy arbiter; the local slot is claimed for UI state
// (spinner, input gating, stream arming) and released by the stream's "done".
// Queueing goes through the service so every client's queued view converges.
func (i *Interactive) startTurnCarrier(parent context.Context, prompt string, images []provider.ImageBlock) {
	c, sess := i.cfg.Carrier, i.carrierSession()
	if !i.turns.claimCarrier(i.carrierCancel()) {
		if err := c.Queue(parent, sess, prompt); err != nil {
			i.setStatusErr(err.Error())
		}
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.carrierLastPrompt = prompt
	i.carrierLastImages = images
	i.mu.Unlock()
	i.resetTurnUI()
	go func() {
		if err := c.Prompt(parent, sess, prompt, toCtrlImages(images)); err != nil {
			// The daemon refused the dispatch (lost a busy race to another
			// producer, or an internal failure). Release the never-started
			// local slot; on busy, queue instead so the text isn't lost.
			i.turns.releaseCarrier()
			var ce *ctrlproto.Error
			if errors.As(err, &ce) && ce.Code == ctrlproto.CodeBusy {
				_ = c.Queue(parent, sess, prompt)
			} else {
				i.setStatusErr(err.Error())
			}
			i.invalidate()
		}
	}()
}

// runCarrierCompact routes a manual /compact through the service. The local
// slot is claimed so the spinner shows and new prompts queue while the daemon
// summarizes; esc cancels through the compact context. The success/no-op
// detail arrives as the service's notice event via the pump.
func (i *Interactive) runCarrierCompact(parent context.Context) {
	c, sess := i.cfg.Carrier, i.carrierSession()
	ctx, cancel := context.WithCancel(parent)
	if !i.turns.claimSlot(cancel) {
		cancel()
		i.setStatusErr(i18n.T("a turn is already running"))
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.spin.StartFixed(i18n.T("compacting"))
	i.statusErr = ""
	i.statusOK = ""
	i.scrollOffset = 0
	i.helpBlock = nil
	i.mu.Unlock()
	i.invalidate()
	go func() {
		err := c.Compact(ctx, sess)
		cancel()
		i.turns.releaseCarrier()
		i.mu.Lock()
		switch {
		case err != nil && ctx.Err() != nil:
			i.statusErr = ""
			i.statusOK = i18n.T("compaction cancelled")
		case err != nil:
			i.statusErr = i18n.T("compaction failed: %s", err.Error())
			i.statusOK = ""
		default:
			// The transcript was replaced under the renderer (the crutch
			// renders from the same agent): the legacy post-compact cleanup.
			i.lastCtxInput = 0
			i.toolCalls = map[string]*tui.ToolCallView{}
			i.toolOrder = nil
			i.turns.ResetGates()
			i.view.InvalidateRenderCache()
		}
		i.mu.Unlock()
		i.invalidate()
	}()
}

// swapModelCarrier routes a /model (or rescue) selection through the
// service's SwitchModel. The workspace hot-swaps the session agent in place
// (same pointer — the crutch stays valid), persists the session meta, and
// broadcasts session_updated. Synchronous like the legacy swapModel — the
// rescue path re-fires the failed prompt right after, which must not race the
// swap (the in-process call does the same Resolve work legacy did here).
func (i *Interactive) swapModelCarrier(prov, model string, rescue bool) {
	c, sess := i.cfg.Carrier, i.carrierSession()
	if err := c.SwitchModel(context.Background(), sess, prov, model); err != nil {
		i.setStatusErr(err.Error())
		i.invalidate()
		return
	}
	// Read the authoritative post-swap identity from the service rather than
	// racing the pump's session_updated handling.
	info, ierr := c.ResumeSession(context.Background(), sess)
	i.mu.Lock()
	if ierr == nil {
		if info.Provider != "" {
			i.cfg.Provider = info.Provider
		}
		if info.Model != "" {
			i.cfg.Model = info.Model
		}
	}
	p, m := i.cfg.Provider, i.cfg.Model
	if rescue {
		i.statusOK = i18n.T("rescue retry: switched to %s / %s (ignored --api-key / --base-url)", p, m)
	} else {
		i.statusOK = i18n.T("switched to %s / %s", p, m)
	}
	i.statusErr = ""
	i.mu.Unlock()
	i.invalidate()
}

// enqueueCarrierPermission drives the existing confirm dialog from a wire
// permission request: the Confirmer inversion. The webConfirmer parks the turn
// goroutine daemon-side; the user's decision resolves it via Approve. First
// answer wins; a late answer for a settled call is ignored by the daemon.
func (i *Interactive) enqueueCarrierPermission(req ctrlproto.PermissionRequest) {
	sess := i.carrierSession() // capture: the answer must reach the session that asked
	resp := make(chan core.ConfirmDecision, 1)
	cr := &confirmRequest{toolName: req.Tool, preview: req.Preview, resp: resp}
	i.mu.Lock()
	i.carrierPerm[req.CallID] = cr
	i.mu.Unlock()
	i.confirmDialog.Enqueue(cr)
	i.invalidate()
	go func() {
		d := <-resp
		i.mu.Lock()
		delete(i.carrierPerm, req.CallID)
		i.mu.Unlock()
		_ = i.cfg.Carrier.Approve(context.Background(), sess, req.CallID, d)
		i.invalidate()
	}()
}

// dismissCarrierPermission drops a still-pending dialog entry for a request
// the daemon reports resolved (another client answered; the turn's
// cancellation failed it closed). A no-op when it was answered locally.
func (i *Interactive) dismissCarrierPermission(callID string) {
	i.mu.Lock()
	cr := i.carrierPerm[callID]
	delete(i.carrierPerm, callID)
	i.mu.Unlock()
	if cr != nil {
		i.confirmDialog.Remove(cr)
	}
}

// enqueueCarrierAsk mirrors enqueueCarrierPermission for the Asker seam.
func (i *Interactive) enqueueCarrierAsk(req ctrlproto.AskRequest) {
	sess := i.carrierSession() // capture: the answer must reach the session that asked
	resp := make(chan core.UserAnswer, 1)
	qr := &questionRequest{question: req.Question, options: req.Options, allowCustom: req.AllowCustom, resp: resp}
	i.mu.Lock()
	i.carrierAsk[req.AskID] = qr
	i.mu.Unlock()
	i.questionDialog.Enqueue(qr)
	i.invalidate()
	go func() {
		a := <-resp
		i.mu.Lock()
		delete(i.carrierAsk, req.AskID)
		i.mu.Unlock()
		_ = i.cfg.Carrier.Answer(context.Background(), sess, req.AskID, a)
		i.invalidate()
	}()
}

// dismissCarrierAsk mirrors dismissCarrierPermission.
func (i *Interactive) dismissCarrierAsk(askID string) {
	i.mu.Lock()
	qr := i.carrierAsk[askID]
	delete(i.carrierAsk, askID)
	i.mu.Unlock()
	if qr != nil {
		i.questionDialog.Remove(qr)
	}
}

// refreshCarrierApprovalMode re-reads the daemon-side gate's mode off the
// settings surface into the status-bar cache. Async — called from the pump on
// snapshot and on surface_updated("settings").
func (i *Interactive) refreshCarrierApprovalMode() {
	c, sess := i.cfg.Carrier, i.carrierSession()
	go func() {
		sf, err := c.Surface(context.Background(), sess, "settings")
		if err != nil || sf.Settings == nil {
			return
		}
		for _, it := range sf.Settings.Items {
			if it.Key != "approval" {
				continue
			}
			i.mu.Lock()
			changed := i.carrierApprovalMode != it.Value
			i.carrierApprovalMode = it.Value
			i.mu.Unlock()
			if changed {
				i.invalidate()
			}
			return
		}
	}()
}

// renderPermissionsWireView paints the /permissions inspector from the wire
// PermissionsView — the ctrlproto twin of the legacy gate-reading renderer,
// producing the same info lines + selectable grants.
func renderPermissionsWireView(th tui.Theme, pv ctrlproto.PermissionsView) (info []string, grants []permGrant) {
	if pv.Mode == "yolo" && len(pv.Rules) == 0 && !pv.AllowAll && len(pv.Grants) == 0 {
		return []string{th.FG256(th.Muted, i18n.T("no permission gate (yolo): every tool runs without asking."))}, nil
	}
	var out []string
	out = append(out, th.FG256(th.Accent, tui.Bold(i18n.T("approval mode")))+"  "+pv.Mode)
	out = append(out, th.FG256(th.Muted, "  "+i18n.T("change it in /settings; flags --approval / --no-yolo set the startup default.")))
	out = append(out, "")

	decColor := func(d string) string {
		switch d {
		case string(core.RuleAllow):
			return th.FG256(th.Accent, d)
		case string(core.RuleDeny):
			return th.FG256(th.Error, d)
		default:
			return th.FG256(th.Warning, d)
		}
	}
	if len(pv.Rules) == 0 {
		out = append(out, th.FG256(th.Muted, i18n.T("no permission rules in effect.")))
	} else {
		out = append(out, th.FG256(th.Accent, tui.Bold(i18n.T("rules")))+th.FG256(th.Muted, "  "+i18n.T("(first match wins; user → project → extension)")))
		lastSrc := ""
		for _, r := range pv.Rules {
			if r.Source != lastSrc {
				out = append(out, th.FG256(th.Muted, "  ["+r.Source+"]"))
				lastSrc = r.Source
			}
			line := "    " + decColor(r.Decision) + "  " + r.Tool
			if r.Args != "" {
				line += th.FG256(th.Muted, "  args~/"+r.Args+"/")
			}
			if r.Reason != "" {
				line += th.FG256(th.Muted, "  — "+r.Reason)
			}
			out = append(out, line)
		}
	}
	out = append(out, "")

	out = append(out, th.FG256(th.Accent, tui.Bold(i18n.T("this session"))))
	if pv.AllowAll {
		grants = append(grants, permGrant{allowAll: true})
	}
	for _, t := range pv.Grants {
		grants = append(grants, permGrant{tool: t})
	}
	if len(grants) == 0 {
		out = append(out, "  "+th.FG256(th.Muted, i18n.T("no session grants yet.")))
	}
	return out, grants
}

// carrierPermissionRevoke resolves one inspector revoke through the surface
// action vocabulary.
func (i *Interactive) carrierPermissionRevoke(g permGrant) {
	c, sess := i.cfg.Carrier, i.carrierSession()
	if g.allowAll {
		_ = c.SurfaceAction(context.Background(), sess, "permissions", "revoke_all", nil)
		return
	}
	_ = c.SurfaceAction(context.Background(), sess, "permissions", "revoke", map[string]string{"tool": g.tool})
}

// carrierPermissionsReset composes the inspector's clear-all (legacy
// gate.Reset: blanket allow + every per-tool grant) from the wire verbs.
func (i *Interactive) carrierPermissionsReset() {
	c, sess := i.cfg.Carrier, i.carrierSession()
	sf, err := c.Surface(context.Background(), sess, "permissions")
	if err != nil || sf.Permissions == nil {
		return
	}
	if sf.Permissions.AllowAll {
		_ = c.SurfaceAction(context.Background(), sess, "permissions", "revoke_all", nil)
	}
	for _, t := range sf.Permissions.Grants {
		_ = c.SurfaceAction(context.Background(), sess, "permissions", "revoke", map[string]string{"tool": t})
	}
}

// carrierListExtensions fetches the extensions surface and inverts the wire
// rollup back into the dialog's ExtInfo shape. Running/gated derive from
// Status (the rollup is exact: running wins, so status "running" ⇔ Running);
// the log/config affordances are off — the wire carries neither a log path
// nor a config schema yet.
func (i *Interactive) carrierListExtensions() []ExtInfo {
	sf, err := i.cfg.Carrier.Surface(context.Background(), i.carrierSession(), "extensions")
	if err != nil || sf.Extensions == nil {
		return nil
	}
	out := make([]ExtInfo, 0, len(sf.Extensions.Extensions))
	for _, e := range sf.Extensions.Extensions {
		out = append(out, ExtInfo{
			Name:               e.Name,
			Version:            e.Version,
			Language:           e.Language,
			Description:        e.Description,
			Scope:              e.Scope,
			GlobalEnabled:      e.GlobalEnabled,
			ProjectDisabled:    e.ProjectDisabled,
			UserConfigDisabled: e.UserConfigDisabled,
			ProjectGated:       e.Status == "gated",
			Effective:          e.Enabled,
			Running:            e.Status == "running",
			Tools:              e.Tools,
			Commands:           e.Commands,
			LastLog:            e.Note,
		})
	}
	return out
}

// applyCarrierExtensionToggle persists + applies an extension toggle through
// the extensions surface (scope global = manifest, project = config disable
// list; the daemon starts/stops the subprocess and rebuilds the tool set).
func (i *Interactive) applyCarrierExtensionToggle(act extensionsAction) {
	scope := "project"
	if act.ToggleGlobal {
		scope = "global"
	}
	err := i.cfg.Carrier.SurfaceAction(context.Background(), i.carrierSession(), "extensions", "toggle",
		map[string]string{"name": act.Name, "enabled": strconv.FormatBool(act.On), "scope": scope})
	if err != nil {
		i.setStatusErr(err.Error())
	} else {
		where := "globally"
		if scope == "project" {
			where = "for this project"
		}
		state := "enabled"
		if !act.On {
			state = "disabled"
		}
		i.setStatusOK(act.Name + " " + state + " " + where)
	}
	if i.extensionsDialog != nil {
		i.extensionsDialog.SetItems(i.carrierListExtensions())
	}
	i.invalidate()
}

// carrierListMCP mirrors carrierListExtensions for the MCP pane.
func (i *Interactive) carrierListMCP() []MCPInfo {
	sf, err := i.cfg.Carrier.Surface(context.Background(), i.carrierSession(), "mcp")
	if err != nil || sf.MCP == nil {
		return nil
	}
	out := make([]MCPInfo, 0, len(sf.MCP.Servers))
	for _, m := range sf.MCP.Servers {
		out = append(out, MCPInfo{
			Name:            m.Name,
			Scope:           m.Scope,
			Description:     m.Description,
			UserDisabled:    m.UserDisabled,
			ProjectDisabled: m.ProjectDisabled,
			ProjectGated:    m.Status == "gated",
			Effective:       m.Enabled,
			Connected:       m.Connected,
			Tools:           m.Tools,
			StartupError:    m.Note,
		})
	}
	return out
}

// applyCarrierMCPToggle persists + applies an MCP toggle through the mcp
// surface (the daemon serializes StartOne/StopOne and rebuilds every live
// session's tools — servers are workspace-global).
func (i *Interactive) applyCarrierMCPToggle(act mcpAction) {
	scope := "global"
	if act.ToggleProject {
		scope = "project"
	}
	err := i.cfg.Carrier.SurfaceAction(context.Background(), i.carrierSession(), "mcp", "toggle",
		map[string]string{"name": act.Name, "enabled": strconv.FormatBool(act.On), "scope": scope})
	if err != nil {
		i.setStatusErr(err.Error())
	} else {
		where := "globally"
		if scope == "project" {
			where = "for this project"
		}
		state := "enabled"
		if !act.On {
			state = "disabled"
		}
		i.setStatusOK(act.Name + " " + state + " " + where)
	}
	if i.mcpDialog != nil {
		i.mcpDialog.SetItems(i.carrierListMCP())
	}
	i.invalidate()
}

// carrierTaskSnapshot serves the /swarm dashboard's per-frame snapshot reads
// from the cached tasks surface, fetching only when the daemon signalled a
// change (surface_updated "tasks") or the cache was invalidated (dialog open,
// after an action). The dialog consumes swarm.AgentSnapshot, so the wire view
// maps back onto it — one renderer, two data sources, like /context.
func (i *Interactive) carrierTaskSnapshot() []swarm.AgentSnapshot {
	i.mu.Lock()
	stale := i.carrierTasksStale || i.carrierTaskRows == nil
	rows := i.carrierTaskRows
	i.mu.Unlock()
	if !stale {
		return rows
	}
	sf, err := i.cfg.Carrier.Surface(context.Background(), i.carrierSession(), "tasks")
	if err != nil || sf.Tasks == nil {
		return rows // keep the last good view; the next change signal retries
	}
	fresh := make([]swarm.AgentSnapshot, 0, len(sf.Tasks.Tasks))
	for _, t := range sf.Tasks.Tasks {
		fresh = append(fresh, taskInfoSnapshot(t))
	}
	i.mu.Lock()
	i.carrierTaskRows, i.carrierTasksStale = fresh, false
	i.mu.Unlock()
	return fresh
}

// taskInfoSnapshot maps the wire task view back onto the dialog's native row
// type. Times ride RFC 3339; a parse failure leaves the zero time, which the
// dialog renders as "no age" rather than a wrong one.
func taskInfoSnapshot(t ctrlproto.TaskInfo) swarm.AgentSnapshot {
	started, _ := time.Parse(time.RFC3339, t.Started)
	finished, _ := time.Parse(time.RFC3339, t.Finished)
	return swarm.AgentSnapshot{
		ID:       t.ID,
		Task:     t.Task,
		Dir:      t.Dir,
		Status:   swarm.Status(t.Status),
		Activity: t.Activity,
		Started:  started,
		Finished: finished,
		Err:      t.Err,
		Tail:     t.Tail,
		Lines:    t.Lines,
		Model:    t.Model,
		Provider: t.Provider,
		Persona:  t.Persona,
	}
}

// invalidateCarrierTasks marks the tasks cache stale; the next snapshot read
// re-fetches from the surface.
func (i *Interactive) invalidateCarrierTasks() {
	i.mu.Lock()
	i.carrierTasksStale = true
	i.mu.Unlock()
}

// carrierTaskAction routes one /swarm verb (spawn/stop/remove/send/resume)
// through the tasks surface.
func (i *Interactive) carrierTaskAction(action string, args map[string]string) error {
	err := i.cfg.Carrier.SurfaceAction(context.Background(), i.carrierSession(), "tasks", action, args)
	if err == nil {
		i.invalidateCarrierTasks()
		return nil
	}
	// The wire flattens error values to text; recover the one sentinel the
	// dialog branches on (swarm.ErrNotReady → the "press R to resume" hint).
	if strings.Contains(err.Error(), swarm.ErrNotReady.Error()) {
		return swarm.ErrNotReady
	}
	return err
}

// --- pump-owned transcript (Stage 4: rendering without the crutch) ---

// setCarrierTranscript replaces the pump transcript from a snapshot — the
// wire's authoritative resync points (every subscribe; the daemon re-
// broadcasts one after compact, auto-compact, and clear).
func (i *Interactive) setCarrierTranscript(msgs []core.WireMessage) {
	out := make([]provider.Message, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, core.MessageFromWire(m))
	}
	i.mu.Lock()
	i.carrierMessages = out
	i.carrierMessagesRev++
	i.mu.Unlock()
}

// appendCarrierMessage appends one full transcript message (the user_message
// event carries the whole WireMessage). Takes i.mu — for handleCarrierEvent
// call sites; handleWireEvent (which holds i.mu for its body) uses the Locked
// variant directly.
func (i *Interactive) appendCarrierMessage(w *core.WireMessage) {
	if w == nil {
		return
	}
	m := core.MessageFromWire(*w)
	i.mu.Lock()
	i.appendCarrierMessageLocked(m)
	i.mu.Unlock()
}

// appendCarrierMessageLocked appends one transcript message. Caller holds i.mu.
func (i *Interactive) appendCarrierMessageLocked(m provider.Message) {
	i.carrierMessages = append(i.carrierMessages, m)
	i.carrierMessagesRev++
}

// appendCarrierToolResultLocked folds one tool_result event into the trailing
// RoleTool message, reproducing core.Agent's per-step batching (executeTools
// appends ONE tool-results message per step): consecutive results extend the
// trailing RoleTool message; the first result after an assistant message
// starts a fresh one, and any message append seals it. Caller holds i.mu
// (handleWireEvent).
func (i *Interactive) appendCarrierToolResultLocked(ev ctrlproto.Event) {
	block := provider.ToolResultBlock{
		CallID:  ev.ID,
		IsError: ev.IsError,
		Content: core.ContentFromWire(ev.Result),
	}
	if n := len(i.carrierMessages); n > 0 && i.carrierMessages[n-1].Role == provider.RoleTool {
		i.carrierMessages[n-1].Content = append(i.carrierMessages[n-1].Content, block)
	} else {
		i.carrierMessages = append(i.carrierMessages, provider.Message{
			Role:    provider.RoleTool,
			Content: []provider.Content{block},
		})
	}
	i.carrierMessagesRev++
}

// carrierTranscript returns the pump transcript for the render pass. The
// message headers are copied under the lock because the tool-result fold
// mutates the trailing message's Content field in place — a frame must hold
// its own struct copies, same contract as the legacy path's agent.Messages().
func (i *Interactive) carrierTranscript() []provider.Message {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]provider.Message(nil), i.carrierMessages...)
}

// carrierTranscriptRev returns just the transcript revision (the render
// cache key reads it every frame; no copy needed).
func (i *Interactive) carrierTranscriptRev() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.carrierMessagesRev
}

// toCtrlImages converts attached images to the wire's inbound form (the
// inverse of the workspace's toImageBlocks).
func toCtrlImages(imgs []provider.ImageBlock) []ctrlproto.Image {
	if len(imgs) == 0 {
		return nil
	}
	out := make([]ctrlproto.Image, 0, len(imgs))
	for _, im := range imgs {
		out = append(out, ctrlproto.Image{MimeType: im.MimeType, Data: im.Data})
	}
	return out
}

// handleWireEvent applies one ctrlproto event to the TUI's live render state,
// mirroring handleEvent. Conversation events (streaming, tool display, usage,
// turn boundaries) are handled here; handleCarrierEvent routes lifecycle and
// control events before falling through to this switch.
func (i *Interactive) handleWireEvent(ev ctrlproto.Event) {
	i.mu.Lock()
	defer i.mu.Unlock()
	switch ev.Type {
	case "assistant_start":
		// Fires at the top of every turn (incl. follow-ups after tool use):
		// arm the typewriter and clear the live tool overlay, whose tools are
		// now folded into the finalized transcript.
		i.turns.BeginAssistant()
		i.toolCalls = map[string]*tui.ToolCallView{}
		i.toolOrder = nil
	case "text_delta":
		i.turns.AppendDelta(ev.Delta)
	case "assistant_message":
		// The daemon transcript gained the finalized reply (with its tool_use
		// and reasoning blocks): mirror it into the pump transcript — the
		// same instant the legacy path's agent promotes it, so the pacer's
		// hide-the-last-message logic keeps its semantics. Persistence stays
		// Workspace-owned; side effects drain the pacer and mirror text to an
		// active chat bridge.
		if ev.Message != nil {
			i.appendCarrierMessageLocked(core.MessageFromWire(*ev.Message))
			i.assistantWireSideEffects(ev.Message)
		}
		if i.turns.FinishMessage() {
			return
		}
	case "tool_use_start":
		if _, exists := i.toolCalls[ev.ID]; !exists {
			i.toolCalls[ev.ID] = &tui.ToolCallView{ID: ev.ID, Name: ev.Name, Streaming: true}
			i.toolOrder = append(i.toolOrder, ev.ID)
			i.turns.GateTool(ev.ID)
		}
	case "tool_use_args":
		if tc, ok := i.toolCalls[ev.ID]; ok {
			tc.RawJSONBuf += ev.Delta
			if p, pok, _ := tui.ExtractPartialStringField(tc.RawJSONBuf, "path"); pok {
				tc.LivePath = p
			} else if p, pok, _ := tui.ExtractPartialStringField(tc.RawJSONBuf, "file_path"); pok {
				tc.LivePath = p
			}
		}
	case "tool_use_end":
		if tc, ok := i.toolCalls[ev.ID]; ok {
			tc.Streaming = false
		}
	case "tool_call":
		if tc, ok := i.toolCalls[ev.ID]; ok {
			tc.Args = tui.ShortArgs(ev.Name, ev.Args)
			tc.Streaming = false
		} else {
			i.toolCalls[ev.ID] = &tui.ToolCallView{ID: ev.ID, Name: ev.Name, Args: tui.ShortArgs(ev.Name, ev.Args)}
			i.toolOrder = append(i.toolOrder, ev.ID)
			i.turns.GateTool(ev.ID)
		}
	case "tool_result":
		// Fold into the pump transcript's per-step RoleTool message —
		// unconditionally, even when the live overlay never saw the call
		// (a mid-turn subscribe): the transcript row must exist either way.
		i.appendCarrierToolResultLocked(ev)
		if tc, ok := i.toolCalls[ev.ID]; ok {
			tc.Done = true
			tc.Error = ev.IsError
			tc.Result = wireBlocksText(ev.Result)
			// Tally the agent's line changes for the status bar's Δ segment —
			// the counts are first-class on the wire (lines_added/removed).
			if !ev.IsError {
				i.editsAdded += ev.LinesAdded
				i.editsRemoved += ev.LinesRemoved
			}
		}
	case "tool_progress":
		if tc, ok := i.toolCalls[ev.ID]; ok {
			tc.Progress = ev.Text
		}
	case "usage":
		if ev.Cumulative != nil {
			i.cumUsage = usageFromWire(ev.Cumulative)
		}
		if ev.Usage != nil && ev.Usage.Input > 0 {
			i.lastCtxInput = ev.Usage.Input + ev.Usage.CacheRead + ev.Usage.CacheWrite
		}
	case "user_message_rejected":
		// The rejection reason rides Text on the wire (Stage 0 enrichment).
		reason := ev.Text
		if reason == "" {
			reason = i18n.T("message blocked by extension")
		}
		i.statusErr = reason
		i.statusOK = ""
	case "turn_end":
		i.pokeGitProber()
		i.pokeStatusScripts()
		switch ev.Stop {
		case string(provider.StopAborted):
			i.turns.ResetStream()
			i.statusErr = ""
			i.statusOK = i18n.T("cancelled")
		case string(provider.StopLength):
			i.statusErr = i18n.T("response hit the model's output-token limit and was cut off, ask it to continue")
			i.statusOK = ""
		}
	}
}

// assistantWireSideEffects mirrors an assistant message's non-persistence side
// effects for the wire path: text mirroring to an active chat bridge. Persistence
// (cfg.OnAssistant in the legacy path) is Workspace-owned here. Called under i.mu.
func (i *Interactive) assistantWireSideEffects(m *core.WireMessage) {
	if i.chatBridge == nil || !i.chatBridge.Active() {
		return
	}
	if text := strings.TrimSpace(wireBlocksText(m.Content)); text != "" {
		go i.chatBridge.OnAssistantText(text)
	}
}

// wireBlocksText concatenates the text of every text block, newline-joined —
// the wire twin of the TextBlock loop the typed handlers run.
func wireBlocksText(blocks []core.WireBlock) string {
	var sb strings.Builder
	for _, c := range blocks {
		if c.Type != "text" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(c.Text)
	}
	return sb.String()
}

// wireImageBlocks extracts the image attachments a full-form wire message
// carries (for the rescue picker's retry stash). A lean carrier delivers
// size-only image blocks — those are skipped, so the result is nil and the
// retry simply goes without attachments, as before.
func wireImageBlocks(blocks []core.WireBlock) []provider.ImageBlock {
	var out []provider.ImageBlock
	for _, c := range blocks {
		if c.Type == "image" && len(c.Data) > 0 {
			out = append(out, provider.ImageBlock{MimeType: c.MimeType, Data: c.Data})
		}
	}
	return out
}

// usageFromWire reconstructs provider.Usage from the wire form (the inverse of
// core's usageToWire), so the status bar's usage picture is fed off the stream.
func usageFromWire(w *core.WireUsage) provider.Usage {
	return provider.Usage{
		InputTokens:      w.Input,
		OutputTokens:     w.Output,
		CacheReadTokens:  w.CacheRead,
		CacheWriteTokens: w.CacheWrite,
		CostUSD:          w.CostUSD,
	}
}
