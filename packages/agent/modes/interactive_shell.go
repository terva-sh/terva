package modes

// The ! shell escape: command detection, execution with a live spinner,
// and the parked output block.

import (
	"context"
	"encoding/json"
	"strings"

	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// shellEscapeCommand reports whether text is a "!command" shell
// escape and, if so, returns the command with the leading '!' (and
// surrounding whitespace) stripped. A bare "!" with no command is
// treated as not an escape so it falls through to the normal prompt
// path rather than running an empty shell.
func shellEscapeCommand(text string) (string, bool) {
	trimmed := strings.TrimLeft(text, " \t")
	if !strings.HasPrefix(trimmed, "!") {
		return "", false
	}
	cmd := strings.TrimSpace(strings.TrimPrefix(trimmed, "!"))
	if cmd == "" {
		return "", false
	}
	return cmd, true
}

// startShellEscape runs a "!command" in the same shell the bash tool
// uses, in the session working directory, honoring the /jail sandbox.
// It shares the busy/cancel state with the agent: esc cancels it, and
// it refuses to start while a turn or another shell escape is already
// in flight. The terminal-log output is parked in i.shellBlock below
// the transcript until the next prompt or /clear, so it never enters
// the model conversation.
func (i *Interactive) startShellEscape(parent context.Context, cmd string) {
	if parent == nil {
		parent = i.runCtx
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	if !i.turns.claimSlot(cancel) {
		cancel()
		i.setStatusErr(i18n.T("busy — wait for the current turn to finish before running a shell command"))
		i.invalidate()
		return
	}
	i.mu.Lock()
	i.shellRunning = true
	i.statusErr = ""
	i.statusOK = ""
	i.spin.StartFixed("running shell command")
	// A new shell escape replaces the previous block; clear stale
	// extension notes the same way a new turn would so the screen
	// doesn't accumulate transient state.
	i.shellBlock = nil
	i.scrollOffset = 0
	i.parkedTurn = 0
	i.parkedTotal = 0
	i.helpBlock = nil
	sandbox := i.cfg.Sandbox
	cwd := i.cfg.CWD
	i.mu.Unlock()
	i.invalidate()

	go func() {
		defer cancel()
		raw, _ := json.Marshal(map[string]any{"command": cmd})
		bash := &tools.BashTool{CWD: cwd, Sandbox: sandbox}
		res, err := bash.Execute(ctx, raw, nil)

		var out string
		if err != nil {
			out = "$ " + cmd + "\n\n" + err.Error() + "\n\n[error]"
		} else {
			for _, c := range res.Content {
				if tb, ok := c.(provider.TextBlock); ok {
					out += tb.Text
				}
			}
		}
		cancelled := ctx.Err() != nil
		failed := err != nil || res.IsError || cancelled
		if cancelled {
			out += "\n\n[cancelled]"
		}

		block := i.renderShellBlock(out, failed)

		// Release the slot atomically; a prompt queued while the shell
		// command ran restarts as its own turn (it used to strand until
		// the next manual prompt).
		next, hasNext := i.turns.release(cancelled)

		i.mu.Lock()
		i.shellRunning = false
		i.shellBlock = block
		if failed {
			if cancelled {
				i.statusErr = i18n.T("shell command cancelled")
			} else {
				i.statusErr = i18n.T("shell command failed")
			}
			i.statusOK = ""
		} else {
			i.statusOK = i18n.T("shell command finished")
			i.statusErr = ""
		}
		i.mu.Unlock()
		i.invalidate()
		if hasNext {
			p := i.runCtx
			if p == nil {
				p = context.Background()
			}
			i.startTurn(p, next)
		}
	}()
}

// renderShellBlock turns merged bash output into a styled terminal-log
// block: each line colored by overall success (tool/green) or failure
// (error/red), with the [exit ...] / [error] footer dimmed via the
// muted color so it reads as metadata.
func (i *Interactive) renderShellBlock(out string, failed bool) []string {
	th := i.cfg.Theme
	base := th.Tool
	if failed {
		base = th.Error
	}
	out = strings.TrimRight(out, "\n")
	lines := strings.Split(out, "\n")
	styled := make([]string, 0, len(lines))
	for _, line := range lines {
		color := base
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[exit ") || strings.HasPrefix(trimmed, "[error]") || strings.HasPrefix(trimmed, "[cancelled]") {
			color = th.Muted
		}
		styled = append(styled, th.FG256(color, line))
	}
	return styled
}
