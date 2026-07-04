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
	"terva.sh/terva/packages/i18n"
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
				if i.cfg.AuthManager != nil {
					i.cfg.AuthManager.CancelOAuth()
				}
				return true
			},
			handleKey: func(k tui.Key) bool {
				act := i.dialog.HandleKey(k)
				if act.StartAPIKey {
					i.startAPIKeyFlow(act.Provider)
				}
				if act.StartOAuth {
					i.startOAuthFlow(act.Provider)
				}
				if act.StartManual {
					i.startManualOAuthFlow(act.Provider)
				}
				if act.SubmitCode != "" {
					i.submitManualOAuthCode(act.SubmitCode)
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
				if act.Select {
					i.applySessionSelection(act.Path)
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
				if act.Select {
					i.doLogout(act.Target)
				}
				return false
			},
			render: func(cols int) []string { return i.logoutDialog.Render(i.cfg.Theme, cols) },
		},
		{ // zot→terva migration // rename:keep
			active: i.migrateDialog.Active,
			ctrlC: func() bool {
				if i.migrateDialog.Loading() {
					return false
				}
				i.migrateDialog.Close()
				return true
			},
			handleKey: func(k tui.Key) bool {
				act := i.migrateDialog.HandleKey(k)
				switch {
				case act.StartCopy:
					// Make the on-disk session file whole BEFORE it gets
					// copied, so the new dir's copy isn't missing the turns
					// that only live in memory.
					if i.cfg.FlushSession != nil {
						i.cfg.FlushSession()
					}
					go func() {
						res, err := i.cfg.Migration.CopyUserData()
						i.runOnMain(func() {
							i.migrateDialog.SetCopyResult(res, err)
							i.invalidate()
						})
						i.invalidate()
					}()
				case act.RenameProject:
					i.migrateDialog.SetRenameResult(i.cfg.Migration.RenameProject())
				case act.RemoveAndExit:
					if err := i.cfg.Migration.RemoveOldDir(); err != nil {
						i.mu.Lock()
						i.statusErr = i18n.T("remove old dir: %s", err.Error())
						i.mu.Unlock()
						i.migrateDialog.Close()
						return false
					}
					// The legacy dir — including the live session file this
					// process was writing — is gone. Exit now; cli.go skips
					// its final flush and prints the restart message.
					return true
				case act.KeepOld:
					i.mu.Lock()
					i.statusOK = i18n.T("migrated — old dir kept; restart terva to fully switch over")
					i.mu.Unlock()
				}
				return false
			},
			render:    func(cols int) []string { return i.migrateDialog.Render(i.cfg.Theme, cols) },
			animating: i.migrateDialog.Loading,
		},
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
					_ = i.cfg.Extensions.SendPanelKey(i.extPanel.ext, i.extPanel.id, panelKeyName(k), panelKeyText(k))
				}
				return false
			},
			render:            func(cols int) []string { return i.extPanel.Render(i.cfg.Theme, cols) },
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
				if k.Kind == tui.KeyRune && (k.Rune == 'r' || k.Rune == 'R') && i.skillsDialog.inList() {
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
					if act.Grant.allowAll {
						i.cfg.ConfirmGate.ClearAllowAll()
					} else {
						i.cfg.ConfirmGate.Revoke(act.Grant.tool)
					}
					i.refreshPermissionsDialog()
				case act.ClearAll:
					i.cfg.ConfirmGate.Reset()
					i.refreshPermissionsDialog()
				}
				return false
			},
			render: func(cols int) []string { return i.permissionsDialog.Render(i.cfg.Theme, cols) },
		},
	}
}

// closeExtPanel notifies the owning extension and closes the panel.
// Shared by the Ctrl+C and Esc paths.
func (i *Interactive) closeExtPanel() {
	if i.cfg.Extensions != nil {
		_ = i.cfg.Extensions.SendPanelClose(i.extPanel.ext, i.extPanel.id)
	}
	i.extPanel.Close()
}
