package modes

// The ! shell escape: command detection, execution with a live spinner,
// and the parked output block.

import (
	"context"
	"encoding/json"
	"strings"

	"terva.sh/terva/packages/agent/ctrlproto"
	"terva.sh/terva/packages/agent/tools"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// shellEscapeCommand reports whether text is a "!command" shell
// escape and, if so, returns the command with the leading '!' (and
// surrounding whitespace) stripped. A bare "!" with no command is
// treated as not an escape so it falls through to the normal prompt
// path rather than running an empty shell.
//
// INVARIANT: exactly one call site — the editor's key handler, where the
// text is known to be a human keystroke. startShellEscape runs bash with
// no confirm gate and no audit record, so any programmatic caller that
// reaches it (a chat DM, an extension's Submit, a sub-agent's recap text)
// gets ungated command execution on the host. Prefix-sniffing at a shared
// entry point is what made that happen: SubmitOrQueue parsed "!" for every
// caller, and a paired Telegram message "!rm -rf ~" ran immediately.
// TestShellEscapeHasOneCallSite enforces the invariant against the source.
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
// the transcript until the next prompt or /clear.
//
// The output does not enter the model conversation, and with the
// shell_result_context engine feature ON it is additionally OFFERED to
// the session as an ephemeral block that rides the user's next request
// once (offerShellResult). That feature ships OFF: it is the difference
// between output that stays on this machine and output that reaches a
// provider.
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

		// Offer the RAW output to the session before it is styled. `out` and the
		// block below differ only by ANSI escape sequences, which makes sending
		// the wrong one invisible in every assertion that reads for content —
		// hence offerShellResult takes the unstyled string and the guard for it
		// compares bytes.
		i.offerShellResult(cmd, out)

		block := i.renderShellBlock(out, failed)

		// Release the local slot. A prompt typed while the shell command ran
		// went to the daemon's queue, and the daemon restarts it: idle, Queue
		// started it there and then; busy, its endTurn shifts it. The TUI used
		// to shift that queue itself from here — reaching into the daemon's
		// agent — which double-dispatched on success and, on cancel, drained
		// prompts other clients had queued.
		i.turns.releaseSlot()

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
	}()
}

// offerShellResult hands a finished escape's output to the session, so the
// user's next message can be a question about what they just ran. See
// docs/proposals/shell-escape-context.md.
//
// Three ways this declines, and the order matters:
//
//  1. The feature is off. Checked HERE and not only in the daemon, because a
//     daemon reached over `terva serve` can be on another host: gating there
//     alone would send `!cat ~/.aws/credentials` across the network and then
//     discard it at the far end. Core enforces it too — a client that skips this
//     check must not get to decide it for the user — but by then the output has
//     already travelled.
//  2. No carrier serves the verb. An older daemon answers `unsupported`, which
//     is a fact about the peer and not something the user did wrong.
//  3. The call fails. Silently. The escape's own output is already on screen and
//     the command itself succeeded or failed on its own terms; an error toast
//     about a context offer the user did not ask for would be noise about a
//     feature working exactly as well as it can.
func (i *Interactive) offerShellResult(cmd, raw string) {
	i.mu.Lock()
	on := i.shellResultContext
	i.mu.Unlock()
	if !on || i.cfg.Carrier == nil {
		return
	}
	sc, ok := i.cfg.Carrier.(ctrlproto.ShellResultController)
	if !ok {
		return
	}
	sess := i.carrierSession()
	if sess == "" {
		return
	}
	go func() {
		_ = sc.ShellResult(context.Background(), sess, ctrlproto.ShellResultParams{
			Command: cmd,
			Output:  raw,
		})
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
