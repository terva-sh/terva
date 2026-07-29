package chat

import (
	"context"
	"errors"
	"fmt"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/i18n"
)

// ChatConfirmer renders tool-approval prompts as chat asks — the
// permission ladder reaching bot mode. With one of these wired into
// the ConfirmGate, `terva bot run --approval ask` (or workspace, or
// auto-edit) becomes usable instead of refuse-everything: a tool call
// the policy says to confirm posts an ask to the owner in the chat
// that started the turn and blocks the turn on the answer, exactly
// like the TUI's prompt. Yolo stays the bot default; this makes it a
// CHOICE instead of a structural necessity.
//
// Policy, all fail-closed:
//   - only the paired owner may answer (restrict_to + the host-side
//     re-filter in the ask plumbing);
//   - "always (this tool)" — a durable session grant — requires an
//     ATTESTED answer (buttons, callback queries). A best-effort
//     answer (parsed text) picking always downgrades to allow-once,
//     with a note in the chat saying so;
//   - timeout, cancellation, or any transport failure denies.
type ChatConfirmer struct {
	ctx  context.Context
	loop *Loop

	// One ask at a time: the agent can issue tool calls from
	// concurrent goroutines, but interleaved approval questions in one
	// chat would be unanswerable.
	//
	// A one-slot channel rather than a sync.Mutex, because the WAIT for it has
	// to be cancellable too. Serialising asks means a turn can be queued behind
	// another turn's unanswered question; with a mutex, cancelling the waiting
	// turn did nothing until the turn ahead of it finished, so a turn the user
	// had abandoned still owned a live turn's approval.
	sem chan struct{}
}

// NewChatConfirmer builds the confirmer. ctx is the daemon's lifetime
// (a shutdown mid-question denies); loop supplies the ask plumbing,
// the owner identity, and the active-turn chat.
func NewChatConfirmer(ctx context.Context, loop *Loop) *ChatConfirmer {
	return &ChatConfirmer{ctx: ctx, loop: loop, sem: make(chan struct{}, 1)}
}

// lock takes the one-ask slot, or reports false if ctx ends first.
func (c *ChatConfirmer) lock(ctx context.Context) bool {
	select {
	case c.sem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (c *ChatConfirmer) unlock() { <-c.sem }

// Confirm implements core.Confirmer over the connector's ask surface.
//
// ctx is the turn's. It bounds both the queue and the ask: a cancelled turn
// releases the mutex immediately instead of holding it for the ask's full
// timeout, which is what made the NEXT turn's first approval question queue
// behind a turn the user had already abandoned. The daemon's own context is
// still honoured — the turn context descends from it, so a shutdown cancels
// this wait too.
func (c *ChatConfirmer) Confirm(ctx context.Context, toolName string, preview string) core.ConfirmDecision {
	// Take the one-ask-at-a-time lock without becoming unstoppable while
	// waiting for it: an abandoned turn's Confirm must not make a live turn's
	// wait for the lock outlive its own cancellation.
	if !c.lock(ctx) {
		return core.ConfirmDecision{Allow: false,
			Reason: "tool call refused: the turn was cancelled while this approval waited its turn to be asked"}
	}
	defer c.unlock()

	chatID, replyTo := c.loop.AskTarget()
	if chatID == "" {
		return core.ConfirmDecision{Allow: false,
			Reason: "tool call refused: approval is required but there is no paired chat to ask in"}
	}
	var restrict []string
	if owner := c.loop.OwnerID(); owner != "" {
		restrict = []string{owner}
	}

	text := i18n.T("approval needed: %s", toolName)
	if preview != "" {
		text += "\n" + preview
	}
	ans, err := c.loop.Ask(ctx, Ask{
		ChatID: chatID, ReplyTo: replyTo, Text: text,
		Options: []AskOption{
			{Key: "approve", Label: i18n.T("Approve"), Style: "affirm", Hint: "👍"},
			{Key: "always", Label: i18n.T("Always (this tool)")},
			{Key: "deny", Label: i18n.T("Deny"), Style: "deny", Hint: "👎"},
		},
		RestrictTo:     restrict,
		TimeoutOutcome: i18n.T("no answer — denied"),
	})
	if err != nil {
		reason := "tool call refused: " + err.Error()
		if errors.Is(err, ErrAskTimeout) {
			reason = "tool call refused: the approval question expired unanswered (fail closed)"
		}
		return core.ConfirmDecision{Allow: false, Reason: reason}
	}

	who := ans.Username
	if who == "" {
		who = ans.UserID
	}
	// Approvals resolve mid-turn, invisibly to the model (it only sees
	// the tool run). A typed note on the turn chat's NEXT prompt makes
	// the permission flow legible — denials need none, the refusal
	// reason already names the actor.
	noteChat := c.loop.activeTurnChat()
	if noteChat == "" {
		noteChat = chatID
	}
	switch ans.Key {
	case "approve":
		c.loop.addNote(noteChat, "approval", i18n.T("tool %q approved by @%s", toolName, who))
		return core.ConfirmDecision{Allow: true}
	case "always":
		if ans.Attestation == AttestationAttested {
			c.loop.addNote(noteChat, "approval", i18n.T("tool %q approved by @%s for the rest of the session", toolName, who))
			return core.ConfirmDecision{Allow: true, RememberTool: true}
		}
		// A parsed-text "always" cannot carry a durable grant; say so
		// where the answer happened and allow this call only.
		//
		// Deliberately the DAEMON's context, not the turn's: this explains a
		// downgrade the person just caused, and they are owed it even if the
		// turn is cancelled between their answer and this line.
		_ = c.loop.Connector.Send(c.ctx, Outgoing{ChatID: chatID,
			Text: i18n.T("\"always\" needs an attested answer (buttons); allowed once instead.")})
		c.loop.addNote(noteChat, "approval", i18n.T("tool %q approved once by @%s (a durable \"always\" needs an attested answer)", toolName, who))
		return core.ConfirmDecision{Allow: true}
	default:
		return core.ConfirmDecision{Allow: false,
			Reason: fmt.Sprintf("tool call denied by @%s over chat", who)}
	}
}
