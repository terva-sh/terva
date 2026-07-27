package modes

// The overlay registry is the single source of truth for modal
// dialogs (TUI plan Phase 1.1). Priority, key routing, rendering,
// cursor ownership, and tick animation all derive from one ordered
// slice, replacing the three hand-maintained parallel structures that
// used to live in interactive.go (an 18-way render switch, an
// 18-block key-dispatch chain, and a per-dialog cursor chain) — where
// adding dialog #19 meant five edits at implicit positions and
// forgetting one compiled fine but silently misbehaved.
//
// Adding a dialog now means: implement the dialog type, give it a
// field on Interactive, and append ONE overlayEntry in buildOverlays
// at the right priority position.

import (
	"terva.sh/terva/packages/tui"
)

// overlayEntry registers one modal overlay.
type overlayEntry struct {
	// active reports whether this overlay currently owns the screen
	// and the keyboard. The first active entry in registry order wins
	// everything: keys, rendering, cursor.
	active func() bool

	// ctrlC, if non-nil, handles Ctrl+C while the overlay is active
	// and reports whether it consumed the key (the usual "close the
	// dialog" behavior). Entries that want Ctrl+C delivered to
	// handleKey (confirm, changelog) leave it nil; entries that close
	// conditionally (migrate while copying) return false to decline.
	ctrlC func() bool

	// handleKey consumes every other key while active. It returns
	// done=true to exit the interactive loop entirely (used by
	// /migrate's remove-and-exit step).
	handleKey func(k tui.Key) (done bool)

	// render returns the overlay's lines for the bottom band.
	render func(cols int) []string

	// cursor, if non-nil, reports the caret position within the
	// rendered block, or row < 0 when the overlay has no caret right
	// now. With a nil cursor (or row < 0), the caret falls back to
	// the main editor — unless hideCaretFallback is set, in which
	// case it is hidden (dialogs that cover the editor with no input
	// of their own, e.g. the swarm dashboard list).
	cursor            func(cols int) (row, col int)
	hideCaretFallback bool

	// animating, if non-nil, reports that the overlay is doing
	// background work (loading, live dashboard) and wants the 120ms
	// tick to repaint it without user input.
	animating func() bool
}

// activeOverlay returns the highest-priority active overlay, or nil.
//
// Note: registry order is the old key-dispatch priority. The render
// switch it replaced used a slightly different order, so in the rare
// state where two overlays were active at once (e.g. a tool-confirm
// arriving while the login dialog was open) keys went to one dialog
// while the screen showed the other. With a single order that
// mismatch can no longer happen.
func (i *Interactive) activeOverlay() *overlayEntry {
	for idx := range i.overlays {
		if i.overlays[idx].active() {
			return &i.overlays[idx]
		}
	}
	return nil
}

// dispatchOverlayKey routes k to the active overlay, if any.
// handled=false means no overlay is open and the key should continue
// to global handling; done=true exits the interactive loop.
func (i *Interactive) dispatchOverlayKey(k tui.Key) (handled, done bool) {
	e := i.activeOverlay()
	if e == nil {
		return false, false
	}
	if k.Kind == tui.KeyCtrlC && e.ctrlC != nil && e.ctrlC() {
		i.invalidate()
		return true, false
	}
	done = e.handleKey(k)
	i.invalidate()
	return true, done
}

// overlayAnimating reports whether any overlay wants tick-driven
// repaints right now (loading spinners, live dashboards).
func (i *Interactive) overlayAnimating() bool {
	for idx := range i.overlays {
		if a := i.overlays[idx].animating; a != nil && a() {
			return true
		}
	}
	return false
}

// buildOverlays assembles the registry. Order is priority: the
// confirm dialog must outrank everything because the agent goroutine
// is blocked waiting for its answer, so no key may leak elsewhere
// while it is up.
func (i *Interactive) buildOverlays() []overlayEntry {
	return []overlayEntry{
		{ // tool-call confirmation (--no-yolo); agent blocked on the answer
			active: i.confirmDialog.Active,
			// No ctrlC: the dialog's own HandleKey treats every key,
			// and Ctrl+C must not dismiss a pending confirmation.
			handleKey: func(k tui.Key) bool {
				i.confirmDialog.HandleKey(k)
				return false
			},
			render: func(cols int) []string { return i.confirmDialog.Render(i.cfg.Theme, cols) },
		},
		{ // ask_user_question; agent goroutine blocked on the answer
			active: i.questionDialog.Active,
			handleKey: func(k tui.Key) bool {
				i.questionDialog.HandleKey(k)
				return false
			},
			render: func(cols int) []string { return i.questionDialog.Render(i.cfg.Theme, cols) },
		},
		{ // login
			active: i.dialog.Active,
			ctrlC: func() bool {
				i.dialog.Close()
				i.cancelLogin()
				return true
			},
			handleKey: func(k tui.Key) bool {
				act := i.dialog.HandleKey(k)
				if act.StartLogin {
					i.startLogin(act.Provider, act.Method)
				}
				if act.Submit != nil {
					i.submitLogin(act.Submit)
				}
				if act.Close {
					i.cancelLogin()
				}
				return false
			},
			render: func(cols int) []string { return i.dialog.Render(i.cfg.Theme, cols) },
			cursor: i.dialog.CursorPos,
		},
		{ // model picker
			active: i.modelDialog.Active,
			ctrlC:  func() bool { i.modelDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.modelDialog.HandleKey(k)
				if act.Select {
					i.applyModelSelection(act.Provider, act.Model)
					if act.Promote {
						i.promoteModelDefault(act.Provider, act.Model, act.Scope)
					}
				}
				if act.Edit {
					i.openModelEdit(act.Provider, act.Model)
				}
				if act.Favorite {
					i.persistFavoriteModel(act.Provider, act.Model, act.FavOn)
				}
				return false
			},
			render: func(cols int) []string { return i.modelDialog.Render(i.cfg.Theme, cols) },
		},
		{ // model config editor (opened with ctrl+e from the picker)
			active: i.modelEditDialog.Active,
			ctrlC:  func() bool { i.modelEditDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.modelEditDialog.HandleKey(k)
				switch {
				case act.Save:
					i.applyModelEdit(act.Provider, act.ModelID, act.Entry)
				case act.Reset:
					i.applyModelReset(act.Provider, act.ModelID)
				}
				return false
			},
			render: func(cols int) []string { return i.modelEditDialog.Render(i.cfg.Theme, cols) },
		},
		{ // log viewer (/extensions or /mcp → l) — top of the cluster so it
			// captures keys while open over either manager.
			active: i.logDialog.Active,
			ctrlC:  func() bool { i.logDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				i.logDialog.HandleKey(k)
				return false
			},
			render: func(cols int) []string { return i.logDialog.Render(i.cfg.Theme, cols) },
		},
		{ // per-extension config form (/extensions → c) — above the manager
			// so it captures keys while open (it opens on top of it).
			active: i.extConfigDialog.Active,
			ctrlC:  func() bool { i.extConfigDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.extConfigDialog.HandleKey(k)
				if act.Save {
					i.applyExtConfig(act)
				}
				return false
			},
			render: func(cols int) []string { return i.extConfigDialog.Render(i.cfg.Theme, cols) },
		},
		{ // extensions manager (/extensions)
			active: i.extensionsDialog.Active,
			ctrlC:  func() bool { i.extensionsDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.extensionsDialog.HandleKey(k)
				switch {
				case act.OpenConfig:
					i.openExtConfigDialog(act.Name)
				case act.OpenLog:
					i.openLogDialog("ext", act.Name)
				case act.ToggleGlobal || act.ToggleProject:
					i.applyExtensionToggle(act)
				}
				return false
			},
			render: func(cols int) []string { return i.extensionsDialog.Render(i.cfg.Theme, cols) },
		},
		{ // mcp manager (/mcp)
			active: i.mcpDialog.Active,
			ctrlC:  func() bool { i.mcpDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.mcpDialog.HandleKey(k)
				switch {
				case act.OpenLog:
					i.openLogDialog("mcp", act.Name)
				case act.ToggleGlobal || act.ToggleProject:
					i.applyMCPToggle(act)
				}
				return false
			},
			render: func(cols int) []string { return i.mcpDialog.Render(i.cfg.Theme, cols) },
		},
		{ // /context: size breakdown + per-extension injected text
			active: i.contextDialog.Active,
			ctrlC:  func() bool { i.contextDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				i.contextDialog.HandleKey(k)
				return false
			},
			render: func(cols int) []string { return i.contextDialog.Render(i.cfg.Theme, cols) },
		},
		{ // /usage: subscription usage windows (read-only)
			active: i.usageDialog.Active,
			ctrlC:  func() bool { i.usageDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				i.usageDialog.HandleKey(k)
				return false
			},
			render: func(cols int) []string { return i.usageDialog.Render(i.cfg.Theme, cols) },
		},
		{ // /resets: list + redeem banked usage-reset credits
			active: i.resetsDialog.Active,
			ctrlC:  func() bool { i.resetsDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				if act := i.resetsDialog.HandleKey(k); act.Consume {
					i.consumeReset(act.CreditID)
				}
				return false
			},
			render: func(cols int) []string { return i.resetsDialog.Render(i.cfg.Theme, cols) },
		},
		{ // rescue picker (after a recoverable provider failure)
			active: i.rescueDialog.Active,
			ctrlC:  func() bool { i.rescueDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.rescueDialog.HandleKey(k)
				if act.Select {
					i.applyRescueSelection(act.Provider, act.Model, act.Prompt)
				}
				return false
			},
			render: func(cols int) []string { return i.rescueDialog.Render(i.cfg.Theme, cols) },
		},
		{ // session browser
			active: i.sessionDialog.Active,
			ctrlC:  func() bool { i.sessionDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.sessionDialog.HandleKey(k)
				switch {
				case act.Select:
					i.applySessionSelection(act.Path)
				case act.GenerateTitle:
					i.generateSessionTitle(act.Path)
				case act.Archive:
					i.archiveSessionAt(act.Path)
				case act.Delete:
					i.deleteSessionAt(act.Path)
				case act.Restore:
					i.restoreArchivedSession(act.ID)
				}
				return false
			},
			render: func(cols int) []string {
				// Reserve rows for the editor (~3), status line (1-2),
				// dialog chrome (header + hint + rule + indicators, ~5),
				// and leave the remainder for session rows. Minimum of 3
				// rows so even a very small terminal shows something.
				_, rows := i.cfg.Terminal.Size()
				avail := rows - 12
				if avail < 3 {
					avail = 3
				}
				i.sessionDialog.MaxRows = avail
				return i.sessionDialog.Render(i.cfg.Theme, cols)
			},
			cursor: func(int) (int, int) { return i.sessionDialog.CursorPos() },
		},
		{ // swarm dashboard
			active: i.swarmDialog.Active,
			ctrlC:  func() bool { i.swarmDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				_, msg, errMsg := i.swarmDialog.HandleKey(k)
				if msg != "" || errMsg != "" {
					i.mu.Lock()
					i.statusOK = msg
					i.statusErr = errMsg
					i.mu.Unlock()
				}
				return false
			},
			render: func(cols int) []string { return i.swarmDialog.Render(i.cfg.Theme, cols) },
			cursor: i.swarmDialog.CursorPos,
			// Dashboard list / transcript view has no caret. Without
			// hiding it the default cursorRow points at the (hidden)
			// main editor row, drawing a stray block in the chat region.
			hideCaretFallback: true,
			animating:         i.swarmDialog.NeedsTickRefresh,
		},
		{ // logout picker
			active: i.logoutDialog.Active,
			ctrlC:  func() bool { i.logoutDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.logoutDialog.HandleKey(k)
				switch {
				case act.Select && act.Endpoint:
					i.doRemoveEndpoint(act.Target)
				case act.Select:
					i.doLogout(act.Target)
				}
				return false
			},
			render: func(cols int) []string { return i.logoutDialog.Render(i.cfg.Theme, cols) },
		},
		// (The zot→terva migration overlay lived here. // rename:keep
		// Its dialog could only be opened by openMigrateDialog, which lost its
		// host hook with the direct driver and now only reports that the
		// migrator is unavailable — so the overlay could never become active.)
		{ // chat-connector ops
			active: i.connectDialog.Active,
			ctrlC:  func() bool { i.connectDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.connectDialog.HandleKey(k)
				if act.Select {
					i.doConnector(act.Action)
				}
				return false
			},
			render: func(cols int) []string { return i.connectDialog.Render(i.cfg.Theme, cols) },
		},
		{ // settings
			active: i.settingsDialog.Active,
			ctrlC:  func() bool { i.settingsDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.settingsDialog.HandleKey(k)
				if act.Toggle {
					i.applySettingChange(act)
				}
				return false
			},
			render: func(cols int) []string {
				// Budget the body so the dialog header + editor +
				// status stay on screen: dialog chrome (header, hint,
				// window indicators, rule, frame padding ≈ 7) plus the
				// status/editor band (≈ 6).
				_, rows := i.cfg.Terminal.Size()
				avail := rows - 13
				if avail < 4 {
					avail = 4
				}
				i.settingsDialog.MaxRows = avail
				return i.settingsDialog.Render(i.cfg.Theme, cols)
			},
		},
		{ // session ops (export/import/fork/tree)
			active: i.sessionOpsDialog.Active,
			ctrlC:  func() bool { i.sessionOpsDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.sessionOpsDialog.HandleKey(k)
				if act.Select {
					i.doSessionOp(act.Action, "")
				}
				return false
			},
			render: func(cols int) []string { return i.sessionOpsDialog.Render(i.cfg.Theme, cols) },
		},
		{ // session tree browser
			active: i.sessionTreeDialog.Active,
			ctrlC:  func() bool { i.sessionTreeDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.sessionTreeDialog.HandleKey(k)
				if act.Select {
					i.applySessionTreeSelection(act.Path)
				}
				return false
			},
			render: func(cols int) []string { return i.sessionTreeDialog.Render(i.cfg.Theme, cols) },
		},
		{ // extension panel: keys forward to the owning extension
			active: i.extPanel.Active,
			ctrlC:  func() bool { i.closeExtPanel(); return true },
			handleKey: func(k tui.Key) bool {
				if k.Kind == tui.KeyEsc {
					i.closeExtPanel()
					return false
				}
				if i.cfg.Extensions != nil {
					_ = i.cfg.Extensions.SendPanelKey(i.extPanel.Ext(), i.extPanel.ID(), panelKeyName(k), panelKeyText(k))
				}
				return false
			},
			render:            func(cols int) []string { return i.extPanel.Render(i.cfg.Theme, cols) },
			hideCaretFallback: true,
		},
		{ // task board (read-only): /tasks
			active: i.tasksDialog.Active,
			ctrlC:  func() bool { i.tasksDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				if i.tasksDialog.HandleKey(k).Close {
					i.tasksDialog.Close()
				}
				return false
			},
			render: func(cols int) []string {
				// Budget body rows from the terminal height (like the session
				// browser) so a long list scrolls inside the bottom band. Reserve
				// the editor (~3), status line (~2), and dialog chrome
				// (header + hint + rule, ~5).
				_, rows := i.cfg.Terminal.Size()
				avail := rows - 10
				if avail < 3 {
					avail = 3
				}
				i.tasksDialog.MaxRows = avail
				return i.tasksDialog.Render(i.cfg.Theme, cols)
			},
			hideCaretFallback: true,
		},
		{ // managed worktrees: /worktree (list + collect views)
			active: i.worktreeDialog.Active,
			ctrlC:  func() bool { i.worktreeDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.worktreeDialog.HandleKey(k)
				switch {
				case act.Close:
					i.worktreeDialog.Close()
				case act.Refresh:
					go i.refreshCarrierWorktrees()
				case act.CdPath != "":
					// The panel's ↵: switch the session into the worktree, the
					// same /cd the retired extension submitted.
					i.worktreeDialog.Close()
					i.SubmitSlash("/cd " + act.CdPath)
				}
				return false
			},
			render: func(cols int) []string {
				_, rows := i.cfg.Terminal.Size()
				avail := rows - 10
				if avail < 3 {
					avail = 3
				}
				i.worktreeDialog.MaxRows = avail
				return i.worktreeDialog.Render(i.cfg.Theme, cols)
			},
			hideCaretFallback: true,
		},
		{ // workflow runs: /workflows (list + Overview/Script/Results tabs)
			active: i.workflowDialog.Active,
			ctrlC:  func() bool { i.workflowDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.workflowDialog.HandleKey(k)
				switch {
				case act.Close:
					i.workflowDialog.Close()
				case act.Refresh:
					go i.refreshWorkflowRuns()
				case act.OpenRun != "":
					// Fetched off the key handler: a run's record carries the whole
					// script and every journaled result (one was 98 KB), and the
					// panel must stay painted while that lands.
					go i.fetchWorkflowRun(act.OpenRun)
				}
				return false
			},
			render: func(cols int) []string {
				_, rows := i.cfg.Terminal.Size()
				avail := rows - 10
				if avail < 3 {
					avail = 3
				}
				i.workflowDialog.MaxRows = avail
				return i.workflowDialog.Render(i.cfg.Theme, cols)
			},
			hideCaretFallback: true,
		},
		{ // jump-to-turn picker (also backs /fork's turn selection)
			active: i.jumpDialog.Active,
			ctrlC: func() bool {
				i.jumpDialog.Close()
				i.pendingFork = false
				return true
			},
			handleKey: func(k tui.Key) bool {
				act := i.jumpDialog.HandleKey(k)
				if act.Select {
					if i.pendingFork {
						i.applyForkSelection(act.MessageIdx)
					} else {
						i.applyJumpSelection(act.MessageIdx, act.TurnNo)
					}
				}
				// If the user dismissed the dialog without selecting, also
				// clear the pending-fork flag so a later plain /jump isn't
				// hijacked.
				if act.Close {
					i.pendingFork = false
				}
				return false
			},
			render: func(cols int) []string { return i.jumpDialog.Render(i.cfg.Theme, cols) },
		},
		{ // btw side-chat
			active: i.btwDialog.Active,
			ctrlC:  func() bool { i.btwDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				i.btwDialog.HandleKey(k, i.invalidate)
				return false
			},
			render:    func(cols int) []string { return i.btwDialog.Render(i.cfg.Theme, cols) },
			cursor:    i.btwDialog.CursorPos,
			animating: i.btwDialog.Loading,
		},
		{ // skills browser
			active: i.skillsDialog.Active,
			ctrlC:  func() bool { i.skillsDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				// `r` in the list view reloads (cache-safe): refresh the skill
				// tool's catalog + re-seed the picker. Body view passes through.
				if k.Kind == tui.KeyRune && (k.Rune == 'r' || k.Rune == 'R') && i.skillsDialog.InList() {
					i.reloadSkillsDialog()
					return false
				}
				i.skillsDialog.HandleKey(k)
				return false
			},
			render: func(cols int) []string { return i.skillsDialog.Render(i.cfg.Theme, cols) },
		},
		{ // changelog overlay
			active: i.changelogDialog.Active,
			// No ctrlC: any key dismisses via HandleKey, and the
			// dismissal must run the persistence callback below.
			handleKey: func(k tui.Key) bool {
				if closed := i.changelogDialog.HandleKey(k); closed {
					// User dismissed; let the parent persist the
					// LastChangelogShown marker via the close callback.
					if i.cfg.OnChangelogDismiss != nil {
						i.cfg.OnChangelogDismiss()
					}
				}
				return false
			},
			render: func(cols int) []string { return i.changelogDialog.Render(i.cfg.Theme, cols) },
		},
		{ // permissions inspector
			active: i.permissionsDialog.Active,
			ctrlC:  func() bool { i.permissionsDialog.Close(); return true },
			handleKey: func(k tui.Key) bool {
				act := i.permissionsDialog.HandleKey(k)
				switch {
				case act.Revoke:
					// The gate lives daemon-side; revokes ride the permissions
					// surface's action vocabulary.
					i.carrierPermissionRevoke(act.Grant)
					i.refreshPermissionsDialog()
				case act.ClearAll:
					i.carrierPermissionsReset()
					i.refreshPermissionsDialog()
				}
				return false
			},
			render: func(cols int) []string { return i.permissionsDialog.Render(i.cfg.Theme, cols) },
		},
	}
}

// closeExtPanel notifies the owning extension and closes the panel.
// Shared by the Ctrl+C and Esc paths. Locked because the carrier pump may
// mirror/close the same overlay from its own goroutine; SendPanelClose runs
// outside the lock (it may block on the extension).
func (i *Interactive) closeExtPanel() {
	i.mu.Lock()
	ext, id := i.extPanel.Ext(), i.extPanel.ID()
	i.extPanel.Close()
	i.carrierPanelSurface = ""
	i.mu.Unlock()
	if i.cfg.Extensions != nil {
		_ = i.cfg.Extensions.SendPanelClose(ext, id)
	}
}
