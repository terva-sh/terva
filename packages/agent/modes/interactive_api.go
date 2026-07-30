package modes

// Host-facing API: the exported methods embedders, chat bridges, and
// the cli call to drive a running Interactive.

import (
	"context"
	"strings"

	"terva.sh/terva/packages/agent/modes/dialogs"
	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// Submit feeds text through the agent loop as if the user had typed it —
// as a PROMPT, never as a command. A leading "!" is prose here: the shell
// escape belongs to the editor's key path alone (see shellEscapeCommand),
// because only there is the text known to be a human keystroke. This method
// implements extdriver.HostHooks.Submit, so an extension reaches it; the
// same reasoning SubmitSlash records for slash commands applies. The
// driver invokes host hooks from its own goroutines, and turn start
// mutates main-loop-only render state, so the submit is marshalled —
// see SubmitOrQueue for the full story.
func (i *Interactive) Submit(text string) {
	i.runOnMain(func() {
		i.startTurn(i.runCtx, text)
	})
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
// Used by the chat bridge and by the auto-swarm recap injector so both
// share the editor's "queue behind an active turn" semantics. Images are
// ignored for now — only the text prompt is forwarded — because the
// queued-prompt path is text-only; a follow-up can expand the queue entry
// to carry images.
//
// text is always a PROMPT. A leading "!" is prose, never a shell escape:
// every caller here is a machine (a paired chat DM, a sub-agent's recap),
// and none of them may run a command on the host. Until this guarantee was
// structural, a DM reading "!rm -rf ~" executed immediately — the escape
// was decided by sniffing a string prefix at a choke point every caller
// shared, so each new caller silently inherited it.
//
// Every caller is also on a background goroutine (the bridge's receive
// loop, the swarm finalizer's Wait() watcher), while turn start mutates
// main-loop-only render state — resetTurnUI clears view.TailLimit, the
// scroll offsets, and the auto-follow baselines, none of which the
// render path reads under i.mu. So the submit is marshalled through
// runOnMain, the same way the shell-escape rule lives here: enforce the
// invariant at the choke point and every future machine caller inherits
// it instead of re-learning it from the race detector.
func (i *Interactive) SubmitOrQueue(text string, images []provider.ImageBlock) {
	i.runOnMain(func() {
		if !i.ready() {
			i.setStatusErr(i18n.T("not logged in. type /login first."))
			i.invalidate()
			return
		}
		// startTurnWithImages claims-or-queues atomically inside the turn
		// engine, so there is no busy pre-check here to race against the
		// turn ending. Images still only reach immediately-started turns;
		// queued prompts are text-only.
		i.startTurnWithImages(i.runCtx, text, images)
	})
}

// ChangelogVersion returns the version string of the changelog
// currently shown (or last shown). Used by the dismiss callback
// to store the correct version for dev builds.
func (i *Interactive) ChangelogVersion() string {
	return i.changelogDialog.Version()
}

// CancelTurn aborts the active turn if one is running. Used by the
// telegram bridge when the paired user sends /stop.
func (i *Interactive) CancelTurn() {
	if i.turns.cancelActive() {
		i.confirmDialog.CancelAll("turn cancelled")
		i.questionDialog.CancelAll()
	}
}

// Confirm implements core.Confirmer: push the request onto the confirmDialog
// queue, trigger a redraw, and block until the user answers or ctx ends.
//
// NOTE ON THE LIVE PATH: no gate is wired to this today. The TUI runs as a front
// end over ctrlproto, so the parking happens in the daemon's webConfirmer and
// this process receives an already-parked permission request to render (see
// interactive_ctrlproto.go's "Confirmer inversion"). It is kept — and held to
// the interface by the assertion below — because it is the in-process answer for
// any host that drives Interactive directly, and because a Confirmer that claims
// the interface in a comment while failing to satisfy it is the kind of quiet
// untruth this file cannot afford.
//
// The ctx select is what the CancelAll sweep used to stand in for: a cancelled
// turn refuses immediately instead of waiting for the front end to remember to
// clear the queue. CancelAll remains for the case ctx cannot cover — the TUI
// exiting out from under a request nobody is waiting on any more.
var _ core.Confirmer = (*Interactive)(nil)

func (i *Interactive) Confirm(ctx context.Context, toolName string, preview string) core.ConfirmDecision {
	resp := make(chan core.ConfirmDecision, 1)
	i.confirmDialog.Enqueue(&dialogs.ConfirmRequest{
		ToolName: toolName,
		Preview:  preview,
		Resp:     resp,
	})
	i.invalidate()
	select {
	case d := <-resp:
		return d
	case <-ctx.Done():
		return core.ConfirmDecision{Allow: false, Reason: "tool call refused: the turn was cancelled before this approval was answered"}
	}
}

// Ask implements core.Asker. The ask_user_question tool calls this
// synchronously; we enqueue the question set on the dialog, redraw, and
// block until the user submits or the turn is cancelled (CancelTurn
// declines every pending ask so the tool goroutine unblocks). ctx
// cancellation is honored so a closing session doesn't deadlock the tool.
func (i *Interactive) Ask(ctx context.Context, qs []core.UserQuestion) ([]core.UserAnswer, error) {
	if len(qs) == 0 {
		return nil, nil
	}
	resp := make(chan []core.UserAnswer, 1)
	i.questionDialog.Enqueue(&dialogs.QuestionRequest{Questions: qs, Resp: resp})
	i.invalidate()
	select {
	case ans := <-resp:
		return core.PadAnswers(ans, len(qs)), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
