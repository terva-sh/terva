package modes

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/agent/swarm"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// This file is the TUI's ctrlproto carrier path — the only shipping
// backend (docs/proposals/tui-on-ctrlproto.md; --tui-legacy is a
// deprecated no-op). Where the removed legacy path drove a *core.Agent
// directly and consumed typed core.AgentEvent through a synchronous
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

// carrierResubscribeDelay paces the pump's retry against a reconnecting
// carrier whose transport is down — a touch above the transport's own
// reconnect backoff so one retry usually lands on a live connection. A var
// so the pump test can compress the wait.
var carrierResubscribeDelay = 2 * time.Second

// runWorkspaceLoop is the pump's sibling: a reliable subscription to the
// WORKSPACE, feeding the facts that are true of the daemon rather than of any
// session — a workspace surface changing (tasks, mcp, chat, settings,
// permissions, raati), the locale changing, a restart notice — through the same
// handler.
//
// These used to ride the session stream, stamped with each live session's id by
// the workspace itself. They arrive once now, on their own address, which is
// what lets a client hear them while holding no session subscription at all.
//
// Unlike runCarrierLoop this is NOT torn down on a session switch: the workspace
// does not change when the session does. It exits only when the context ends, or
// when the carrier turns out to have no workspace stream at all — a replay
// carrier, or a daemon too old to know the address. Neither is a failure: an old
// daemon relays those events into the session subscription anyway, which is
// exactly the fallback this replaces.
func (i *Interactive) runWorkspaceLoop(ctx context.Context) {
	for ctx.Err() == nil {
		ch, err := i.cfg.Carrier.SubscribeReliable(ctx, ctrlproto.AddrWorkspace)
		if err != nil {
			// Transient, on an attached carrier whose transport is down: the
			// session pump owns the user-visible outage message, so just wait
			// and retry quietly.
			if rc, ok := i.cfg.Carrier.(ReconnectingCarrier); ok && rc.Reconnecting() {
				select {
				case <-ctx.Done():
					return
				case <-time.After(carrierResubscribeDelay):
				}
				continue
			}
			return // this carrier has no workspace stream; nothing to pump
		}
		for ev := range ch {
			i.handleCarrierEvent(ev)
			i.invalidate()
		}
		// Closed channel: the transport dropped (attached) or the workspace is
		// shutting down. Re-subscribing is free when it is the former; when it
		// is the latter, ctx ends and the loop exits.
		select {
		case <-ctx.Done():
			return
		case <-time.After(carrierResubscribeDelay):
		}
	}
}

// runCarrierLoop is the ctrlproto TUI's event pump: a reliable subscription to
// the CURRENT session's stream, feeding every event through
// handleCarrierEvent. It replaces the legacy path's synchronous per-turn sink.
// The loop is a supervisor: a session switch cancels just the subscription
// (carrierPumpCancel) and the loop re-subscribes to the new current session;
// only ctx ending (or an in-process subscribe failure) exits the pump itself.
//
// Attached (a [ReconnectingCarrier]), the same supervisor is the resync
// engine: a dropped connection closes the subscription channel, the loop
// re-subscribes — retrying with backoff while the transport reconnects — and
// the fresh snapshot that leads every subscription repaints the session.
func (i *Interactive) runCarrierLoop(ctx context.Context) {
	wasDown := false
	for ctx.Err() == nil {
		subCtx, cancel := context.WithCancel(ctx)
		i.mu.Lock()
		i.carrierPumpCancel = cancel
		sess := i.cfg.CarrierSession
		i.mu.Unlock()
		if sess == "" {
			// Credential-less boot: no session to subscribe to yet. Idle
			// until the post-login switch commits a binding and kicks the
			// pump (finishCarrierLogin → SwitchCarrierSession).
			<-subCtx.Done()
			cancel()
			continue
		}
		ch, err := i.cfg.Carrier.SubscribeReliable(subCtx, sess)
		if err != nil {
			cancel()
			// A reconnecting carrier (terva attach) fails transiently while
			// its transport is down: show the outage, back off, retry. An
			// in-process carrier error is a real bug — keep the fail-stop.
			if rc, ok := i.cfg.Carrier.(ReconnectingCarrier); ok && rc.Reconnecting() {
				wasDown = true
				i.setStatusErr(i18n.T("connection lost — reconnecting…"))
				i.invalidate()
				select {
				case <-ctx.Done():
				case <-time.After(carrierResubscribeDelay):
				}
				continue
			}
			i.setStatusErr(i18n.T("control-plane subscribe failed: %s", err.Error()))
			i.invalidate()
			return
		}
		if wasDown {
			wasDown = false
			i.setStatusOK(i18n.T("reconnected — session resynced"))
			i.invalidate()
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
// materializes the target through the service, drops state owned by the old
// session (pending dialog round-trips, the local turn slot — the old session's
// turn keeps running daemon-side; this TUI just stops watching it), commits the
// new binding, and kicks the pump so it re-subscribes. The new binding's FIRST
// snapshot then refills the transcript and the queue, decides the first-paint
// tail cap, and seeds the cost and context gauges. Nothing is read off an agent
// — the TUI holds none (plan 4.1).
func (i *Interactive) SwitchCarrierSession(id string) error {
	c := i.cfg.Carrier
	if c == nil {
		return errors.New("not running on a ctrlproto carrier")
	}
	info, err := c.ResumeSession(context.Background(), id)
	if err != nil {
		return err
	}
	// info.ID is the canonical resolved session id (ResumeSession materialized
	// it). The TUI holds no agent to swap — the daemon owns it — so binding to
	// the session is committing the id and re-subscribing the pump.
	id = info.ID
	// Old-session teardown: refuse pending dialogs (their forward goroutines
	// answer the OLD session — captured at enqueue — where first-answer-wins
	// makes a late refusal harmless) and release the local slot.
	i.confirmDialog.CancelAll("session switched")
	i.questionDialog.CancelAll()
	i.turns.releaseCarrier()
	i.mu.Lock()
	i.carrierPerm = map[string]*dialogs.ConfirmRequest{}
	i.carrierAsk = map[string]*dialogs.QuestionRequest{}
	i.cfg.CarrierSession = id
	// Drop the old session's transcript and queue now; the new binding's
	// snapshot refills both (a beat of empty beats a beat of foreign
	// history), and decides that snapshot's first-paint tail cap.
	i.carrierMessages = nil
	i.carrierMessagesRev++
	i.carrierQueued = nil
	// The recall log joins to THIS thread's tail (inputHistory counts its
	// in-thread entries off the transcript). Against another session's
	// transcript that count is meaningless and would hide the tail of the
	// new session's own prompts, so the log goes with the thread.
	i.inputLog = nil
	i.carrierUsage = ctrlproto.UsageInfo{}
	i.carrierChat = ctrlproto.ChatView{}
	// Drop the old session's task board now so the /tasks panel and status glance
	// don't flash the previous session's tasks; the new binding's refresh
	// (surface_updated("taskboard") or the resync) refills it under the new id.
	i.carrierTaskBoard = nil
	i.carrierTaskBoardSession = id
	i.carrierMemory = nil
	i.carrierMemorySession = id
	i.armCarrierBind()
	if info.Provider != "" {
		i.cfg.Provider = info.Provider
	}
	if info.Model != "" {
		i.cfg.Model = info.Model
	}
	i.noteSessionMetaLocked(info)
	kick := i.carrierPumpCancel
	// Binding to a session that resolved means prompting is possible again,
	// even if a /logout had closed the gate on the old one.
	i.setReadyLocked(true)
	i.mu.Unlock()
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
		// turn no-op inside reclaimCarrier. The claim happens here (the engine
		// has its own lock) but the UI reset is marshalled: this handler runs
		// on the pump goroutine, and resetTurnUI clears main-loop-only render
		// state (view.TailLimit, scroll offsets) the render path reads
		// without i.mu.
		if i.turns.reclaimCarrier(i.carrierCancel()) {
			i.runOnMain(i.resetTurnUI)
		}
	case "done":
		// The workspace's guaranteed turn-over signal (every path, including
		// errors and cancels; duplicates possible — releaseCarrier is
		// idempotent). Status/queue/persistence are daemon-side or handled by
		// the accompanying error/turn_end events.
		i.turns.releaseCarrier()
		// A turn may have moved the provider's plan windows (codex reports them
		// in response headers). Cached read, no network.
		i.refreshUsageAsync(false)
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
	case "user_message_rejected":
		// An extension's intercept (BeforeUserMessage) refused the prompt:
		// it never reached the model or the transcript, so without this
		// banner the message would simply vanish — including a QUEUED one
		// rejected at drain time, long after it was sent. Quote a stub of
		// what was blocked; the reason alone stops meaning anything once the
		// exact words are forgotten.
		if quote := clipQuote(ev.Rejected, 80); quote != "" {
			i.setStatusErr(i18n.T("message blocked: %s — “%s”", ev.Text, quote))
		} else {
			i.setStatusErr(i18n.T("message blocked: %s", ev.Text))
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
			// Marshalled: this handler runs on the pump goroutine and the
			// render cache is main-loop-only. A one-frame delay is safe —
			// entries are keyed by content hash, so the drop is memory
			// hygiene after the transcript swap, not a correctness barrier.
			i.runOnMain(i.view.InvalidateRenderCache)
		}
		i.pendingPostCompactNote = ""
		i.spin.Start() // back to the normal spinner for the turn that follows
		i.mu.Unlock()
	case "stall":
		// Rung 1 of the stuck-loop hatch: the detector caught a repeating model
		// and nudged it. Coalesce into ONE line that counts up rather than
		// appending a note per nudge — a wedged run fires many, and a growing
		// stack of identical notes is noise. The turn continues on the same model,
		// so no spinner/status change.
		if ev.Stall == nil {
			break
		}
		th := i.cfg.Theme
		i.mu.Lock()
		hadOld := false
		kept := i.extNotes[:0:0]
		for _, note := range i.extNotes {
			if strings.Contains(note, stallNudgeGlyph) {
				hadOld = true // drop the previous coalesced line; a fresh one replaces it
				continue
			}
			kept = append(kept, note)
		}
		if hadOld {
			i.stallNudges++
		} else {
			i.stallNudges = 1 // self-resets when the line is gone (notes clear on the next prompt)
		}
		msg := i18n.T("loop detected — nudged the model %d× this turn to break out (latest tool: %s)", i.stallNudges, orDash(ev.Stall.Tool))
		i.extNotes = append(kept, hatchNoteLine(th, th.Warning, stallNudgeGlyph, msg))
		i.mu.Unlock()
	case "escalation":
		// Rung 3: the harness swapped (or tried to) to a stronger model. Word the
		// note by disposition so a switch, a decline, and a failed swap read
		// distinctly; a switch also changes the model shown in the header, and
		// this says why. Rare (once per turn), so dedup identical rather than
		// coalesce — distinct dispositions stay as their own lines.
		if ev.Escalation == nil {
			break
		}
		th := i.cfg.Theme
		e := ev.Escalation
		var line string
		switch e.Disposition {
		case "switched":
			line = hatchNoteLine(th, th.Accent, "⇗", i18n.T("escalated to %s (%s) to break the loop", orDash(e.ToModel), e.ToProvider))
		case "failed":
			line = hatchNoteLine(th, th.Error, "⚠", i18n.T("escalation to %s failed: %s", orDash(e.ToModel), e.Detail))
		default: // declined | stopped: the user chose not to swap (they saw the ask)
			line = hatchNoteLine(th, th.Muted, "•", i18n.T("escalation declined — staying on %s", orDash(e.FromModel)))
		}
		i.mu.Lock()
		dup := false
		for _, note := range i.extNotes {
			if note == line {
				dup = true
				break
			}
		}
		if !dup {
			i.extNotes = append(i.extNotes, line)
		}
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
		i.setCarrierQueue(ev.Snapshot.Queued)
		i.seedSessionMeters(ev.Snapshot.Session)
		i.noteSessionMeta(ev.Snapshot.Session)
		i.seedCarrierChat()
		// The usage picture belongs to the provider client, not the session, so
		// it does not ride the snapshot. Pull the cached one for this binding.
		i.refreshUsageAsync(false)
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
		// Resync the task-board cache so the status glance is populated on
		// (re)subscribe/resume/session-switch without waiting for the next turn.
		go i.refreshCarrierTaskBoard()
		go i.refreshCarrierMemory()
		// Same for the worktree cache — its only other fill points are the
		// /worktree panel's open and refresh (no push event exists for it).
		go i.refreshCarrierWorktrees()
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
		i.noteSessionMetaLocked(*ev.Info)
		i.mu.Unlock()
	case ctrlproto.EventAuthState:
		// A provider login moved — possibly one this TUI did not start, and
		// possibly finished in a browser on another device entirely.
		if ev.Auth != nil {
			i.handleAuthState(*ev.Auth)
		}
	case ctrlproto.EventSurfaceUpdated:
		// The settings surface carries the approval-mode badge; the tasks
		// surface backs the /swarm dashboard's cached snapshot (and the
		// status bar's swarm segment); other panes re-fetch on open (no
		// pinned pane in the TUI yet).
		switch {
		case ev.SurfaceID == "settings":
			i.refreshCarrierApprovalMode()
		case ev.SurfaceID == "tasks":
			i.invalidateCarrierTasks()
		case ev.SurfaceID == "taskboard":
			// The per-session task board changed (once per turn end). Refresh the
			// cache off the pump so the status glance and an open /tasks panel
			// re-render — but never auto-open the panel.
			go i.refreshCarrierTaskBoard()
		case ev.SurfaceID == "memory":
			// A fact was saved, pruned, or reloaded — by this client, another, or
			// the model. Refresh off the pump so the glance and an open /memory
			// panel re-render; never auto-open the panel.
			go i.refreshCarrierMemory()
		case ev.SurfaceID == "chat":
			// connect/disconnect/rebind, from this client or another.
			go i.fetchCarrierChat()
		case strings.HasPrefix(ev.SurfaceID, carrierExtPanelPrefix):
			// An extension opened/updated a panel proactively (host-hook, not a
			// command result): mirror it into the TUI overlay — the carrier twin
			// of the legacy interactiveExtHooks OpenPanel/UpdatePanel path.
			i.syncCarrierExtPanel(ev.SurfaceID)
		}
	case ctrlproto.EventSurfacesChanged:
		// A surface appeared or disappeared. If a proactively-opened extension
		// panel is mirrored in the overlay, it may have been the one removed
		// (paneClose broadcasts only this event) — re-check and close if so.
		i.checkCarrierExtPanelClosed()
	case ctrlproto.EventSessionsChanged:
		// The session SET changed on the daemon — another client created,
		// renamed, or deleted one, or a cold session materialized. If the
		// /sessions picker is open, re-list it live so it never shows a stale
		// set. Dialog state isn't mutex-guarded, so mutate it on the main loop.
		i.runOnMain(func() {
			if !i.sessionDialog.Active() {
				return
			}
			i.sessionDialog.List = i.cfg.ListSessions
			i.sessionDialog.ListArchived = i.cfg.ListArchivedSessions
			i.sessionDialog.Refresh(i.cfg.TervaHome, i.cfg.CWD)
			i.invalidate()
		})
	case ctrlproto.EventQueueUpdated:
		// The daemon broadcasts the whole list after every mutation — its own
		// enqueue at a turn boundary, the post-turn shift, a drop on failure,
		// and any client's SetQueue. Mirror it; the pump's invalidate repaints
		// the "sliding in" chips.
		i.setCarrierQueue(ev.Queued)
	case ctrlproto.EventLocaleChanged:
		// A Stage-3 consumer. The pump's invalidate already repaints.
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
		if err := c.Prompt(parent, sess, ctrlproto.PromptParams{Text: prompt, Images: toCtrlImages(images)}); err != nil {
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
			// Marshalled: this completion closure is the compact's own
			// goroutine and the render cache is main-loop-only (see the
			// pump's compact_end twin for why the delay is safe).
			i.runOnMain(i.view.InvalidateRenderCache)
		}
		i.mu.Unlock()
		i.invalidate()
	}()
}

// swapModelCarrier routes a /model (or rescue) selection through the
// service's SwitchModel. The workspace hot-swaps the session agent in place,
// persists the session meta, and broadcasts session_updated. Synchronous like
// the legacy swapModel — the rescue path re-fires the failed prompt right
// after, which must not race the swap (the in-process call does the same
// Resolve work legacy did here).
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
	cr := &dialogs.ConfirmRequest{
		ToolName: req.Tool,
		Preview:  req.Preview,
		Scopes:   ctrlproto.CoreGrantScopes(req.Scopes),
		Resp:     resp,
	}
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
	resp := make(chan []core.UserAnswer, 1)
	// Set() falls back to the singular mirror fields, so an ask from a
	// daemon built before question sets still arrives as one question.
	qr := &dialogs.QuestionRequest{Questions: req.Set(), Resp: resp}
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
// settings surface into the status-bar cache — and, for a REMOTE carrier,
// the daemon's reasoning level too (in-process, cfg.Reasoning was seeded
// from the resolved runtime, which a --reasoning flag may have set; the
// surface only knows the config value, so adopting it there would stomp the
// override the agent actually runs with). Async — called from the pump on
// snapshot and on surface_updated("settings").
func (i *Interactive) refreshCarrierApprovalMode() {
	c, sess := i.cfg.Carrier, i.carrierSession()
	adoptReasoning := i.cfg.CarrierRemote
	go func() {
		sf, err := c.Surface(context.Background(), sess, "settings")
		if err != nil || sf.Settings == nil {
			return
		}
		changed := false
		i.mu.Lock()
		for _, it := range sf.Settings.Items {
			switch it.Key {
			case "approval":
				changed = changed || i.carrierApprovalMode != it.Value
				i.carrierApprovalMode = it.Value
			case "reasoning":
				if adoptReasoning {
					changed = changed || i.cfg.Reasoning != it.Value
					i.cfg.Reasoning = it.Value
				}
			}
		}
		i.mu.Unlock()
		if changed {
			i.invalidate()
		}
	}()
}

// renderTrustPosture is the Workspace Trust block of the /permissions
// inspector: whether this directory may load project-supplied extensions,
// skills, and context, and where to change it.
//
// The pane listed approval rules and said nothing about trust, which reads as a
// complete answer to "what may this session do" when it is half of one. An
// empty CWD means a daemon that predates the field — render nothing rather
// than assert an untrusted workspace on missing data.
func renderTrustPosture(th tui.Theme, pv ctrlproto.PermissionsView) []string {
	if pv.CWD == "" {
		return nil
	}
	state := th.FG256(th.Warning, i18n.T("restricted"))
	hint := i18n.T("project extensions, skills, and context files are NOT loaded here. /trust to load them.")
	if pv.Trusted {
		state = th.FG256(th.Accent, i18n.T("trusted"))
		hint = i18n.T("project extensions, skills, and context files load here. /untrust to stop.")
	}
	return []string{
		"",
		th.FG256(th.Accent, tui.Bold(i18n.T("workspace trust"))) + "  " + state + th.FG256(th.Muted, "  "+pv.CWD),
		th.FG256(th.Muted, "  "+hint),
	}
}

// renderPermissionsWireView paints the /permissions inspector from the wire
// PermissionsView — the ctrlproto twin of the legacy gate-reading renderer,
// producing the same info lines + selectable grants.
func renderPermissionsWireView(th tui.Theme, pv ctrlproto.PermissionsView) (info []string, grants []dialogs.PermGrant) {
	if pv.Mode == "yolo" && len(pv.Rules) == 0 && !pv.AllowAll && len(pv.Grants) == 0 {
		// Even with no gate there is still a trust verdict, and it is the only
		// thing left holding a cloned repo back — so it is the one line worth
		// printing here rather than a bare "nothing to inspect".
		return append([]string{th.FG256(th.Muted, i18n.T("no permission gate (yolo): every tool runs without asking."))},
			renderTrustPosture(th, pv)...), nil
	}
	var out []string
	out = append(out, th.FG256(th.Accent, tui.Bold(i18n.T("approval mode")))+"  "+pv.Mode)
	out = append(out, th.FG256(th.Muted, "  "+i18n.T("change it in /settings; flags --approval / --no-yolo set the startup default.")))
	out = append(out, renderTrustPosture(th, pv)...)
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
		grants = append(grants, dialogs.PermGrant{AllowAll: true})
	}
	for _, t := range pv.Grants {
		grants = append(grants, dialogs.PermGrant{Tool: t})
	}
	if len(grants) == 0 {
		out = append(out, "  "+th.FG256(th.Muted, i18n.T("no session grants yet.")))
	}
	return out, grants
}

// carrierPermissionRevoke resolves one inspector revoke through the surface
// action vocabulary.
func (i *Interactive) carrierPermissionRevoke(g dialogs.PermGrant) {
	c, sess := i.cfg.Carrier, i.carrierSession()
	if c == nil {
		return
	}
	if g.AllowAll {
		_ = c.SurfaceAction(context.Background(), sess, "permissions", "revoke_all", nil)
		return
	}
	_ = c.SurfaceAction(context.Background(), sess, "permissions", "revoke", map[string]string{"tool": g.Tool})
}

// carrierPermissionsReset composes the inspector's clear-all (legacy
// gate.Reset: blanket allow + every per-tool grant) from the wire verbs.
func (i *Interactive) carrierPermissionsReset() {
	c, sess := i.cfg.Carrier, i.carrierSession()
	if c == nil {
		return
	}
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
// the log/config affordance flags ride the wire, and their dialogs work when
// the host wired the closures (the in-process entry does — logs and the form
// are local reads; applying a saved config rides the surface's config action).
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
			HasConfig:          e.HasConfig,
			HasLog:             e.HasLog,
		})
	}
	return out
}

// carrierExtConfigFields pulls one extension's config form off the extensions
// surface.
//
// The form used to be built here from local disk, which is correct only when
// the terminal and the daemon are the same process. Attached to a remote
// daemon it read THIS machine's manifests and THIS machine's config.json —
// the wrong filesystem, answering about the wrong extensions — so the hooks
// were simply left unwired and the form was unavailable. Now the host builds
// it and this renders what it is handed, which is the same thing the browser
// gets.
func (i *Interactive) carrierExtConfigFields(name string) []dialogs.ConfigField {
	sf, err := i.cfg.Carrier.Surface(context.Background(), i.carrierSession(), "extensions")
	if err != nil || sf.Extensions == nil {
		return nil
	}
	for _, e := range sf.Extensions.Extensions {
		if e.Name != name {
			continue
		}
		out := make([]dialogs.ConfigField, 0, len(e.Config))
		for _, f := range e.Config {
			out = append(out, dialogs.ConfigField{
				Key:         f.Key,
				Label:       f.Label,
				Type:        f.Type,
				Description: f.Description,
				Required:    f.Required,
				Secret:      f.Secret,
				Options:     f.Options,
				Saved:       f.Saved,
				Default:     f.Default,
				HasSaved:    f.HasSaved,
			})
		}
		return out
	}
	return nil
}

// applyCarrierExtConfig submits a form through the extensions surface. The
// daemon types the values against the schema and persists them, then pushes
// them to the running extension — one write, by the process that owns the file.
func (i *Interactive) applyCarrierExtConfig(name string, values map[string]string) error {
	raw, err := json.Marshal(values)
	if err != nil {
		return err
	}
	return i.cfg.Carrier.SurfaceAction(context.Background(), i.carrierSession(), "extensions", "set_config",
		map[string]string{"name": name, "values": string(raw)})
}

// applyCarrierExtensionToggle persists + applies an extension toggle through
// the extensions surface (scope global = manifest, project = config disable
// list; the daemon starts/stops the subprocess and rebuilds the tool set).
func (i *Interactive) applyCarrierExtensionToggle(act dialogs.ExtensionsAction) {
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
func (i *Interactive) applyCarrierMCPToggle(act dialogs.MCPAction) {
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
	if stale {
		// Fill off the render path: the status bar's swarm glance and the
		// open dashboard read this per frame, and on an attached carrier the
		// surface read is a network round-trip a frame must not block on.
		// The fill's invalidate repaints with the fresh rows.
		go i.fetchCarrierTasks()
	}
	return rows
}

// fetchCarrierTasks fills the tasks cache from the surface; at most one fill
// runs at a time (concurrent calls no-op). On failure the cache settles on
// the last good view — or an empty (non-nil) one, so a daemon that doesn't
// serve the surface isn't re-fetched every frame; the next change signal or
// dialog open marks the cache stale and retries.
func (i *Interactive) fetchCarrierTasks() {
	i.mu.Lock()
	if i.carrierTasksFetching {
		i.mu.Unlock()
		return
	}
	i.carrierTasksFetching = true
	i.mu.Unlock()

	sf, err := i.cfg.Carrier.Surface(context.Background(), i.carrierSession(), "tasks")
	fetched := err == nil && sf.Tasks != nil
	fresh := []swarm.AgentSnapshot{}
	if fetched {
		for _, t := range sf.Tasks.Tasks {
			fresh = append(fresh, taskInfoSnapshot(t))
		}
	}
	i.mu.Lock()
	i.carrierTasksFetching = false
	i.carrierTasksStale = false
	if fetched || i.carrierTaskRows == nil {
		i.carrierTaskRows = fresh
	}
	i.mu.Unlock()
	if fetched {
		i.invalidate()
	}
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
		// Reconstruct the worker-shaped fields too, or an ATTACHED /swarm dialog
		// lies by omission relative to a local one: the dialog already renders
		// cost, and backend is what tells a Claude worker apart from a native
		// child. Both ride the wire (TaskInfo) — dropping them here was the
		// attach-fidelity gap the reverse mapping must not reopen.
		Backend: t.Backend,
		CostUSD: t.CostUSD,
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

// carrierExtPanelPrefix is the surface-id namespace the daemon uses for
// extension-opened panels (workspace_surfaces.go panelSurfaceID: "ext:<ext>:<id>").
const carrierExtPanelPrefix = "ext:"

// syncCarrierExtPanel mirrors a daemon-side extension panel surface into the
// TUI's single panel overlay. Async (called off the pump) because it fetches
// the panel content; opens the overlay, or updates it in place when it is
// already the active panel so a fast-updating panel doesn't reset scroll.
// carrierPanelSurface records the mirrored surface id so the close check knows
// this overlay is daemon-backed (vs a command-result panel, which has none).
func (i *Interactive) syncCarrierExtPanel(sid string) {
	c, sess := i.cfg.Carrier, i.carrierSession()
	go func() {
		sf, err := c.Surface(context.Background(), sess, sid)
		if err != nil || sf.Panel == nil {
			return
		}
		ext := sf.Panel.Ext
		id := strings.TrimPrefix(sid, carrierExtPanelPrefix+ext+":")
		i.mu.Lock()
		if i.extPanel.Active() && i.extPanel.Ext() == ext && i.extPanel.ID() == id {
			i.extPanel.Update(sf.Title, sf.Panel.Lines, sf.Panel.Footer)
		} else {
			i.extPanel.Open(ext, id, sf.Title, sf.Panel.Lines, sf.Panel.Footer)
		}
		i.carrierPanelSurface = sid
		i.mu.Unlock()
		i.invalidate()
	}()
}

// checkCarrierExtPanelClosed closes the overlay when the daemon removed the
// extension panel it mirrors — paneClose broadcasts only surfaces_changed, no
// per-panel event. A no-op unless the overlay is showing a daemon-backed panel
// (carrierPanelSurface set); command-result panels have no surface and are left
// alone. Async (re-lists surfaces).
func (i *Interactive) checkCarrierExtPanelClosed() {
	i.mu.Lock()
	sid := i.carrierPanelSurface
	i.mu.Unlock()
	if sid == "" {
		return
	}
	c, sess := i.cfg.Carrier, i.carrierSession()
	go func() {
		metas, err := c.Surfaces(context.Background(), sess)
		if err != nil {
			return
		}
		for _, m := range metas {
			if m.ID == sid {
				return // still open
			}
		}
		// The extension closed it daemon-side; drop the overlay (no SendPanelClose
		// — the ext already closed it). Guard against a racing re-open.
		i.mu.Lock()
		if i.carrierPanelSurface == sid {
			i.extPanel.Close()
			i.carrierPanelSurface = ""
		}
		i.mu.Unlock()
		i.invalidate()
	}()
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
	// Re-validate the history the user paged in behind the compaction divider. A
	// snapshot lands at the end of EVERY turn, not just on open, so without this a
	// user would reveal an hour of scrollback, send one message, and watch it go.
	// Kept when the divider is still the same checkpoint; dropped when it is not,
	// because auto-compact fires at turn end — exactly when a snapshot arrives.
	i.resetRevealForTranscript(out)
	// This binding's first snapshot resolves the tail cap: a resumed
	// multi-thousand-message session must not block the first paint on
	// rendering every prior turn. Only the first — a later snapshot
	// (compact, clear, a reconnect's resubscribe) must not re-cap a
	// transcript the user has since scrolled open.
	if i.carrierTailArmed {
		i.carrierTailArmed = false
		i.carrierTailPending = 0
		if len(out) > initialResumeTailLimit {
			i.carrierTailPending = initialResumeTailLimit
		}
	}
	i.mu.Unlock()
}

// armCarrierBind arms everything a fresh binding's FIRST snapshot must decide:
// the first-paint tail cap, and the seed for the cost/context meters. Later
// snapshots (compact, clear, a reconnect's resubscribe) must do neither — one
// would collapse a transcript the user scrolled open, the other would re-anchor
// the $/hr epoch mid-session. Caller holds i.mu.
func (i *Interactive) armCarrierBind() {
	i.carrierTailArmed = true
	i.carrierTailPending = -1
	i.carrierSeedArmed = true
	i.carrierChatArmed = true
}

// noteSessionMeta captures the per-frame session metadata off a full
// SessionInfo (see the carrierSessPath field group). Unlike seedSessionMeters
// it is NOT bind-armed: every snapshot and session_updated refreshes it, so a
// model switch or rename that happened while this client was disconnected
// lands with the resubscribe's snapshot.
func (i *Interactive) noteSessionMeta(info ctrlproto.SessionInfo) {
	i.mu.Lock()
	i.noteSessionMetaLocked(info)
	i.mu.Unlock()
}

// noteSessionMetaLocked is noteSessionMeta for callers already holding i.mu.
// Every producer broadcasts the whole s.info(), never a partial — adopt
// unconditionally (ContextWindow 0 means the daemon doesn't know the model;
// the render falls back to the local catalog).
func (i *Interactive) noteSessionMetaLocked(info ctrlproto.SessionInfo) {
	if info.Path != "" {
		i.carrierSessPath = info.Path
	}
	i.carrierCtxWindow = info.ContextWindow
	i.carrierSubscription = info.Subscription
}

// CarrierSessionPath is the bound session's transcript path as the wire last
// reported it — the attach entry point's CurrentSessionPath reads this cache
// instead of a resume round-trip per status-bar frame.
func (i *Interactive) CarrierSessionPath() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.carrierSessPath
}

// SetCarrierJailed mirrors the daemon's sandbox lock into the status bar's
// jailed badge. Called by the attach entry point from each server hello, so a
// daemon restarted with a different jail flag re-seeds on reconnect.
func (i *Interactive) SetCarrierJailed(v bool) {
	i.mu.Lock()
	changed := i.carrierJailed != v
	i.carrierJailed = v
	i.mu.Unlock()
	if changed {
		i.invalidate()
	}
}

// seedSessionMeters hydrates the cumulative-cost and context-token gauges from
// the binding's first snapshot. Both used to be read off the crutch agent
// (Cost(), LastTurnUsage()) at construction and on session load; the wire has
// carried them on SessionInfo all along, and the TUI discarded it. Per-turn
// updates already ride the usage events.
func (i *Interactive) seedSessionMeters(info ctrlproto.SessionInfo) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if !i.carrierSeedArmed {
		return
	}
	i.carrierSeedArmed = false
	i.cumUsage = usageFromWire(&info.Usage)
	i.lastCtxInput = info.ContextTokens
	// The bound session's historical cost is the new burn-rate base; only
	// spend from this instant counts toward $/hr.
	i.costBase = i.cumUsage.CostUSD
	i.costBaseAt = time.Now()
}

// ---- prompt-readiness gate ----

// ready reports whether the bound session can accept a prompt.
func (i *Interactive) ready() bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.carrierReady
}

// setReady opens or closes the prompt gate (login, logout, session switch).
func (i *Interactive) setReady(v bool) {
	i.mu.Lock()
	i.carrierReady = v
	i.mu.Unlock()
}

// setReadyLocked is setReady for callers already holding i.mu.
func (i *Interactive) setReadyLocked(v bool) { i.carrierReady = v }

// ---- queue mirror ----
//
// The daemon owns the pending queue and broadcasts the whole list on every
// mutation (queue_updated) and in every snapshot, so the TUI mirrors rather
// than tracks. Mutations go back through SetQueue and return as a broadcast.

// setCarrierQueue replaces the mirrored queue from a snapshot or a
// queue_updated event.
func (i *Interactive) setCarrierQueue(texts []string) {
	i.mu.Lock()
	i.carrierQueued = append([]string(nil), texts...)
	i.mu.Unlock()
}

// carrierQueuedList copies the mirrored queue for the render pass.
func (i *Interactive) carrierQueuedList() []string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]string(nil), i.carrierQueued...)
}

// carrierQueuedCount reports how many prompts wait for a turn slot.
func (i *Interactive) carrierQueuedCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return len(i.carrierQueued)
}

// clearCarrierQueue drops every queued prompt (ctrl+c on an idle session).
// The daemon's broadcast is what updates the mirror.
func (i *Interactive) clearCarrierQueue() {
	if i.carrierQueuedCount() == 0 {
		return
	}
	if err := i.cfg.Carrier.SetQueue(context.Background(), i.carrierSession(), nil); err != nil {
		i.setStatusErr(err.Error())
	}
}

// popCarrierQueue peels the most recently queued prompt off the tail and
// returns it, so alt+up can put it back in the editor. The mirror gives the
// text; SetQueue commits the shortened list, and its broadcast reconciles.
func (i *Interactive) popCarrierQueue() (string, bool) {
	i.mu.Lock()
	n := len(i.carrierQueued)
	if n == 0 {
		i.mu.Unlock()
		return "", false
	}
	text := i.carrierQueued[n-1]
	rest := append([]string(nil), i.carrierQueued[:n-1]...)
	i.mu.Unlock()

	if err := i.cfg.Carrier.SetQueue(context.Background(), i.carrierSession(), rest); err != nil {
		i.setStatusErr(err.Error())
		return "", false
	}
	return text, true
}

// takeCarrierTailLimit hands the resolved tail cap to the render pass, once.
// i.view belongs to the main goroutine; the pump only ever computes the value.
func (i *Interactive) takeCarrierTailLimit() (limit int, ok bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.carrierTailPending < 0 {
		return 0, false
	}
	limit, i.carrierTailPending = i.carrierTailPending, -1
	return limit, true
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
	return i.carrierTranscriptLocked()
}

// carrierTranscriptLocked is carrierTranscript for callers already holding
// i.mu — the session-swap paths mirror the transcript into the view under the
// same hold that resets the rest of the per-session state.
func (i *Interactive) carrierTranscriptLocked() []provider.Message {
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
		// hide-the-last-message logic keeps its semantics. Persistence and the
		// chat mirror are both Workspace-owned.
		if ev.Message != nil {
			i.appendCarrierMessageLocked(core.MessageFromWire(*ev.Message))
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
			// Non-streaming path (a full tool_call with no prior arg deltas):
			// seed RawJSONBuf so the live body preview (e.g. the bash command)
			// has the args to render.
			if tc.RawJSONBuf == "" {
				tc.RawJSONBuf = string(ev.Args)
			}
			tc.Streaming = false
		} else {
			i.toolCalls[ev.ID] = &tui.ToolCallView{ID: ev.ID, Name: ev.Name, Args: tui.ShortArgs(ev.Name, ev.Args), RawJSONBuf: string(ev.Args)}
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
		// The provider client re-captures its subscription windows on every
		// call inside the turn; pull them so the meters appear while a long
		// turn streams instead of only at its end.
		i.refreshUsageThrottledLocked()
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

// clipQuote flattens a quoted stub to one line and caps it at max runes,
// with an ellipsis when it cut anything — for banners that quote what a
// guard refused without letting a pasted file take over the status line.
func clipQuote(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
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
