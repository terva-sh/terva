package modes

// Host-facing API: the exported methods embedders, chat bridges, and
// the cli call to drive a running Interactive.

import (
	"context"
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/provider"
	"terva.sh/terva/packages/tui"
)

// Submit feeds text through the agent loop as if the user had typed it.
func (i *Interactive) Submit(text string) {
	if cmd, ok := shellEscapeCommand(text); ok {
		i.startShellEscape(i.runCtx, cmd)
		return
	}
	i.startTurn(i.runCtx, text)
}

// ApplyChangedCWD is called by the host after a successful /cd hook.
// The host has already rebuilt the agent and opened a fresh session
// in the new cwd; this method swaps the fresh agent into the running
// TUI, updates the displayed cwd, clears the transcript display
// caches, and points the file picker at the new directory.
//
// The fresh agent's transcript is empty (new session) so the chat
// view starts blank, matching what relaunching `terva --cwd <path>`
// would show. Cost meters reset.
func (i *Interactive) ApplyChangedCWD(ag *core.Agent, provider, model, cwd string) {
	i.turns.SetAgent(ag)
	i.mu.Lock()
	i.cfg.CWD = cwd
	// Re-report the working directory to the terminal so "new tab / split
	// here" tracks the /cd change (OSC 7).
	if i.cfg.Terminal != nil {
		if seq := tui.ReportCWD(cwd); seq != "" {
			_, _ = i.cfg.Terminal.Write([]byte(seq))
		}
	}
	i.cfg.Provider = provider
	i.cfg.Model = model
	i.toolCalls = map[string]*tui.ToolCallView{}
	i.toolOrder = nil
	i.turns.ResetGates()
	i.helpBlock = nil
	i.parkedTurn = 0
	i.statusErr = ""
	i.mu.Unlock()
	i.fileSuggest.Reset()
	i.fileSuggest.SetCWD(cwd)
	i.invalidate()
}

// SubmitSlash runs text as a slash command in the TUI as if the user
// had typed it. text must start with '/' — callers that hand it
// plain prose silently get a no-op so a misbehaving extension can't
// run a stray prompt through this path. Read-only commands run in
// place; commands that would mutate the transcript or replace the
// agent cancel the active turn first via the same path the editor
// uses for typed slash commands.
func (i *Interactive) SubmitSlash(text string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return
	}
	head := text
	if idx := strings.IndexAny(text, " \t"); idx >= 0 {
		head = text[:idx]
	}
	if slashCancelsTurn(head) {
		i.cancelAndWaitForIdle()
	}
	i.runSlash(i.runCtx, text)
	i.invalidate()
}

// SubmitOrQueue runs text immediately if the agent is idle, or
// appends it to the pending queue if a turn is already in flight.
// Used by the telegram bridge (and by the editor submit path) so
// both input sources share the same "queue behind an active turn"
// semantics. Images are ignored for now — only the text prompt is
// forwarded — because the queued-prompt path is text-only; a
// follow-up can expand the queue entry to carry images.
func (i *Interactive) SubmitOrQueue(text string, images []provider.ImageBlock) {
	if cmd, ok := shellEscapeCommand(text); ok {
		i.startShellEscape(i.runCtx, cmd)
		return
	}
	if !i.turns.HasAgent() {
		i.setStatusErr("not logged in. type /login first.")
		i.invalidate()
		return
	}
	// startTurnWithImages claims-or-queues atomically inside the turn
	// engine, so there is no busy pre-check here to race against the
	// turn ending. Images still only reach immediately-started turns;
	// queued prompts are text-only.
	i.startTurnWithImages(i.runCtx, text, images)
}

// CancelTurn aborts the active turn if one is running. Used by the
// telegram bridge when the paired user sends /stop.
// ChangelogVersion returns the version string of the changelog
// currently shown (or last shown). Used by the dismiss callback
// to store the correct version for dev builds.
func (i *Interactive) ChangelogVersion() string {
	if i.changelogDialog != nil {
		return i.changelogDialog.version
	}
	return ""
}

func (i *Interactive) CancelTurn() {
	if i.turns.cancelActive() {
		i.confirmDialog.CancelAll("turn cancelled")
		i.questionDialog.CancelAll()
	}
}

// Agent returns the current agent, if any. Used by cli.go to flush the
// final transcript to the session file.
func (i *Interactive) Agent() *core.Agent {
	return i.turns.Agent()
}

// Confirm implements core.Confirmer. The agent goroutine calls
// this synchronously before every tool invocation when --no-yolo is
// active. We push the request onto the confirmDialog queue, trigger
// a redraw, and block the caller until the user answers.
//
// If the session is cancelled or the TUI exits mid-prompt, any
// pending request is refused via CancelAll so the agent doesn't
// deadlock.
func (i *Interactive) Confirm(toolName string, preview string) core.ConfirmDecision {
	resp := make(chan core.ConfirmDecision, 1)
	i.confirmDialog.Enqueue(&confirmRequest{
		toolName: toolName,
		preview:  preview,
		resp:     resp,
	})
	i.invalidate()
	return <-resp
}

// Ask implements core.Asker. The ask_user_question tool calls this
// synchronously; we enqueue the question on the dialog, redraw, and block
// until the user answers or the turn is cancelled (CancelTurn declines
// every pending question so the tool goroutine unblocks). ctx
// cancellation is honored so a closing session doesn't deadlock the tool.
func (i *Interactive) Ask(ctx context.Context, q core.UserQuestion) (core.UserAnswer, error) {
	resp := make(chan core.UserAnswer, 1)
	i.questionDialog.Enqueue(&questionRequest{
		question:    q.Question,
		options:     q.Options,
		allowCustom: q.AllowCustom,
		resp:        resp,
	})
	i.invalidate()
	select {
	case ans := <-resp:
		return ans, nil
	case <-ctx.Done():
		return core.UserAnswer{}, ctx.Err()
	}
}
