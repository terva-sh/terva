package modes

// Extension integration: command invocation, notes, panels, and hot
// reload. The exported methods are the extension host API.

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/extproto"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/tui"
)

// truncateLine shortens s so it fits within n display cells, with an
// ellipsis if trimmed. Used by the "sliding in" chips so a pasted
// novel doesn't blow past the status line.
func panelKeyName(k tui.Key) string {
	switch k.Kind {
	case tui.KeyUp:
		return "up"
	case tui.KeyDown:
		return "down"
	case tui.KeyLeft:
		return "left"
	case tui.KeyRight:
		return "right"
	case tui.KeyEnter:
		return "enter"
	case tui.KeyEsc:
		return "esc"
	case tui.KeyTab:
		return "tab"
	case tui.KeyBackspace:
		return "backspace"
	case tui.KeyDelete:
		return "delete"
	case tui.KeyHome:
		return "home"
	case tui.KeyEnd:
		return "end"
	case tui.KeyPageUp:
		return "pageup"
	case tui.KeyPageDown:
		return "pagedown"
	case tui.KeyRune:
		return "rune"
	default:
		return "unknown"
	}
}

func panelKeyText(k tui.Key) string {
	if k.Kind == tui.KeyRune {
		return string(k.Rune)
	}
	return ""
}

func (i *Interactive) invokeExtensionCommand(ctx context.Context, name, args string) {
	resp, err := i.cfg.Extensions.Invoke(ctx, name, args, 30*time.Second)
	if err != nil {
		i.mu.Lock()
		i.statusErr = i18n.T("extension /%s: %s", name, err.Error())
		i.statusOK = ""
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if resp.Error != "" {
		i.mu.Lock()
		i.statusErr = i18n.T("extension /%s: %s", name, resp.Error)
		i.statusOK = ""
		i.mu.Unlock()
		i.invalidate()
		return
	}
	switch resp.Action {
	case "open_panel":
		if resp.OpenPanel != nil {
			extName := name
			if i.cfg.Extensions != nil {
				if owner := i.cfg.Extensions.CommandOwner(name); owner != "" {
					extName = owner
				}
			}
			i.OpenPanel(extName, *resp.OpenPanel)
		}
	case "prompt":
		if strings.TrimSpace(resp.Prompt) == "" {
			return
		}
		// Same background-goroutine story as the insert case below:
		// turn start clears main-loop-only render state (resetTurnUI),
		// so it has to land on the main goroutine too.
		prompt := resp.Prompt
		i.runOnMain(func() {
			i.startTurn(i.runCtx, prompt)
		})
	case "insert":
		// invokeExtensionCommand runs on a background goroutine, but
		// tui.Editor has no locking and is otherwise mutated only from
		// the main key loop. Marshal the insert onto the main goroutine
		// so we don't race the renderer / key handler.
		text := resp.Insert
		i.runOnMain(func() {
			i.ed.Insert(text)
		})
	case "display":
		i.appendExtensionNote(name, resp.Display, "info")
	case "noop", "":
		// nothing
	default:
		i.mu.Lock()
		i.statusErr = i18n.T("extension /%s: unknown action %s", name, resp.Action)
		i.mu.Unlock()
		i.invalidate()
	}
}

// appendExtensionNote renders an extension-originated note in the
// chat. Levels: "info" (muted), "warn" (warning), "error" (error),
// "success" (tool/ok green).
func (i *Interactive) appendExtensionNote(extName, msg, level string) {
	if msg == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.appendExtensionNoteLocked(extName, msg, level)
}

// appendExtensionNoteLocked is the body, for callers that already hold mu and
// must not drop it mid-way — ReplaceNote's retract and re-add have to be one
// critical section or two concurrent rewrites of the same key interleave into
// a duplicate. Caller holds mu.
func (i *Interactive) appendExtensionNoteLocked(extName, msg, level string) {
	color := i.cfg.Theme.Muted
	switch level {
	case "warn":
		color = i.cfg.Theme.Warning
	case "error":
		color = i.cfg.Theme.Error
	case "success":
		color = i.cfg.Theme.Tool
	}
	prefix := i.cfg.Theme.FG256(i.cfg.Theme.Accent, "["+extName+"] ")
	for _, line := range strings.Split(msg, "\n") {
		i.statusOK = "" // clear any stale ok
		i.statusErr = ""
		i.extNotes = append(i.extNotes, prefix+i.cfg.Theme.FG256(color, line))
	}
}

// ReplaceNote sets the one note owned by key, dropping whatever that key put
// up before. It is for a note that tracks a CHANGING fact rather than
// announcing an event — the swarm's worktree lease record is the first: how
// many worktrees are leased and whether they run restricted is true until it
// is not, and a fresh line per change stacks a history of superseded claims
// above the input.
//
// Every note the key ever wrote is dropped, not just the last one, so a caller
// that raced itself cannot leave an orphan behind. A multi-line message is
// tracked whole and retracted whole.
//
// An empty message retracts without leaving anything. That is the one thing
// ClearNotes cannot express here: it strips by EXTENSION, so a workspace note
// clearing itself would also take the permission-policy warnings and
// extension-load errors that share the [workspace] label.
func (i *Interactive) ReplaceNote(extName, key, message, level string) {
	i.mu.Lock()
	changed := i.dropKeyedNote(key)
	if message != "" {
		before := len(i.extNotes)
		i.appendExtensionNoteLocked(extName, message, level)
		if len(i.extNotes) > before {
			if i.notesByKey == nil {
				i.notesByKey = map[string][]string{}
			}
			// The EXACT rendered lines, not the message: the theme has already
			// coloured them by now, so matching on the text would miss.
			i.notesByKey[key] = append([]string(nil), i.extNotes[before:]...)
			changed = true
		}
	}
	i.mu.Unlock()
	if changed {
		i.invalidate()
	}
}

// dropKeyedNote removes every line key currently owns. Caller holds mu.
func (i *Interactive) dropKeyedNote(key string) bool {
	owned := i.notesByKey[key]
	if len(owned) == 0 {
		return false
	}
	kept := i.extNotes[:0:0]
	for _, line := range i.extNotes {
		if slices.Contains(owned, line) {
			continue
		}
		kept = append(kept, line)
	}
	i.extNotes = kept
	delete(i.notesByKey, key)
	return true
}

// resetNotes drops the whole sticky block AND the keyed-note bookkeeping.
//
// The two must move together: a stale entry in notesByKey outlives the line it
// names, and the next ReplaceNote for that key then scans for lines that are
// no longer there — leaving the new note as a second copy of a note that was
// supposed to have exactly one. Every site that clears the block calls this,
// so there is no version of the reset that can forget half of it.
//
// Caller holds mu.
func (i *Interactive) resetNotes() {
	i.extNotes = nil
	clear(i.notesByKey)
}

// extStatusSegments returns the extensions' current status-bar segments
// for the status line (nil-safe when extensions are disabled).
func (i *Interactive) extStatusSegments() []string {
	if i.cfg.Extensions == nil {
		return nil
	}
	return i.cfg.Extensions.StatusSegments()
}

// RefreshStatus is the manager's hook to redraw after a status_segment
// changes, so a status update appears even when nothing else triggers a
// frame. (HostHooks.)
func (i *Interactive) RefreshStatus() { i.invalidate() }

// slashContext (the /context modal) lives in interactive_context.go — it
// now opens a tabbed dialog with the size breakdown + the per-extension
// injected text, instead of printing the text inline.

// HostHooks implementation for the extension manager. The manager
// holds an interface, not a concrete *Interactive, so these methods
// are the only thing the manager sees.

// Notify is the manager's NotifyFromExt entry point.
func (i *Interactive) Notify(extName, level, message string) {
	i.appendExtensionNote(extName, message, level)
	i.invalidate()
}

// ClearNotes removes every note line owned by extName from the
// bottom-sticky ext-notes block. Extensions use this to retract a
// transient status line (e.g. an approval prompt) once it no longer
// applies, instead of leaving it stacked forever. Notes from other
// extensions and internal notes (auto-compact) are left untouched.
func (i *Interactive) ClearNotes(extName string) {
	marker := "[" + extName + "] "
	i.mu.Lock()
	if len(i.extNotes) == 0 {
		i.mu.Unlock()
		return
	}
	kept := i.extNotes[:0:0]
	changed := false
	for _, line := range i.extNotes {
		if strings.Contains(line, marker) {
			changed = true
			continue
		}
		kept = append(kept, line)
	}
	if changed {
		i.extNotes = kept
	}
	i.mu.Unlock()
	if changed {
		i.invalidate()
	}
}

// Insert places text at the cursor in the editor. Called from
// extension host hooks that run on background goroutines, so the
// editor mutation is marshalled onto the main Run() goroutine —
// tui.Editor has no internal locking and is otherwise only touched
// by the key loop.
func (i *Interactive) Insert(text string) {
	i.runOnMain(func() {
		i.ed.Insert(text)
	})
}

// Display appends a styled note from extName to the chat without a
// model call.
func (i *Interactive) Display(extName, text string) {
	i.appendExtensionNote(extName, text, "info")
	i.invalidate()
}

func (i *Interactive) OpenPanel(extName string, spec extproto.PanelSpec) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.extPanel.Open(extName, spec.ID, spec.Title, spec.Lines, spec.Footer)
	// A command-result / legacy host-hook panel has no daemon surface backing
	// it; clear the mirror id so the carrier close check leaves it alone.
	i.carrierPanelSurface = ""
	i.invalidate()
}

func (i *Interactive) UpdatePanel(extName, panelID, title string, lines []string, footer string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.extPanel.Active() && i.extPanel.Ext() == extName && i.extPanel.ID() == panelID {
		i.extPanel.Update(title, lines, footer)
		i.invalidate()
	}
}

func (i *Interactive) ClosePanel(extName, panelID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.extPanel.Active() && i.extPanel.Ext() == extName && i.extPanel.ID() == panelID {
		i.extPanel.Close()
		i.carrierPanelSurface = ""
		i.invalidate()
	}
}

// runReloadExt triggers a live reload of every extension (discovered
// + explicit). Runs on a goroutine so the TUI stays responsive; the
// Manager.Reload takes a couple of hundred ms to shut down subprocs
// and respawn them. Shows a status line throughout.
func (i *Interactive) runReloadExt(ctx context.Context) {
	if i.cfg.Extensions == nil {
		i.mu.Lock()
		i.statusErr = i18n.T("no extension manager in this build")
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.statusOK = i18n.T("reloading extensions...")
	i.statusErr = ""
	i.mu.Unlock()
	i.invalidate()

	go func() {
		stats := i.cfg.Extensions.Reload(ctx, 2*time.Second)
		msg := fmt.Sprintf("reloaded: %d stopped, %d loaded (%d ready)", stats.Stopped, stats.Loaded, stats.Ready)
		if len(stats.Errors) > 0 {
			msg += fmt.Sprintf(", %d error(s)", len(stats.Errors))
		}
		i.mu.Lock()
		i.statusOK = msg
		i.statusErr = ""
		i.mu.Unlock()
		i.invalidate()
	}()
}
