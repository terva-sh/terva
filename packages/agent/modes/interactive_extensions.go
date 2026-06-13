package modes

// Extension integration: command invocation, notes, panels, and hot
// reload. The exported methods are the extension host API.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"terva.sh/terva/packages/agent/extproto"
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
		i.statusErr = "extension /" + name + ": " + err.Error()
		i.statusOK = ""
		i.mu.Unlock()
		i.invalidate()
		return
	}
	if resp.Error != "" {
		i.mu.Lock()
		i.statusErr = "extension /" + name + ": " + resp.Error
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
		i.startTurn(i.runCtx, resp.Prompt)
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
		i.statusErr = "extension /" + name + ": unknown action " + resp.Action
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

// slashContext lists what each extension is contributing to the model
// — static guidance and live cards — so the user can see exactly what
// the context-card capability is injecting. The transparency half of
// that capability's security story.
func (i *Interactive) slashContext() {
	if i.cfg.Extensions == nil {
		i.appendExtensionNote("context", "extensions are not enabled", "info")
		i.invalidate()
		return
	}
	items := i.cfg.Extensions.ContextSnapshot()
	if len(items) == 0 {
		i.appendExtensionNote("context", "no extension is contributing context to the model", "info")
		i.invalidate()
		return
	}
	var b strings.Builder
	b.WriteString("extension context injected into the model:")
	for _, it := range items {
		if it.Kind == "static" {
			fmt.Fprintf(&b, "\n%s (system guidance):", it.Source)
		} else {
			label := it.Label
			if label == "" {
				label = it.ID
			}
			fmt.Fprintf(&b, "\n%s (card %q):", it.Source, label)
		}
		for _, line := range strings.Split(strings.TrimRight(it.Text, "\n"), "\n") {
			fmt.Fprintf(&b, "\n  %s", line)
		}
	}
	i.appendExtensionNote("context", b.String(), "info")
	i.invalidate()
}

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
	if i.cfg.Extensions != nil {
		cols, rows := i.cfg.Terminal.Size()
		_ = cols
		_ = rows
	}
	i.invalidate()
}

func (i *Interactive) UpdatePanel(extName, panelID, title string, lines []string, footer string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.extPanel.Active() && i.extPanel.ext == extName && i.extPanel.id == panelID {
		i.extPanel.Update(title, lines, footer)
		i.invalidate()
	}
}

func (i *Interactive) ClosePanel(extName, panelID string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.extPanel.Active() && i.extPanel.ext == extName && i.extPanel.id == panelID {
		i.extPanel.Close()
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
		i.statusErr = "no extension manager in this build"
		i.mu.Unlock()
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.statusOK = "reloading extensions..."
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
