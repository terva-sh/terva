package chat

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"terva.sh/terva/packages/core"
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
	mu sync.Mutex
}

// NewChatConfirmer builds the confirmer. ctx is the daemon's lifetime
// (a shutdown mid-question denies); loop supplies the ask plumbing,
// the owner identity, and the active-turn chat.
func NewChatConfirmer(ctx context.Context, loop *Loop) *ChatConfirmer {
	return &ChatConfirmer{ctx: ctx, loop: loop}
}

// Confirm implements core.Confirmer over the connector's ask surface.
func (c *ChatConfirmer) Confirm(toolName string, preview string) core.ConfirmDecision {
	c.mu.Lock()
	defer c.mu.Unlock()

	chatID, replyTo := c.loop.AskTarget()
	if chatID == "" {
		return core.ConfirmDecision{Allow: false,
			Reason: "tool call refused: approval is required but there is no paired chat to ask in"}
	}
	var restrict []string
	if owner := c.loop.OwnerID(); owner != "" {
		restrict = []string{owner}
	}

	text := fmt.Sprintf("approval needed: %s", toolName)
	if preview != "" {
		text += "\n" + preview
	}
	ans, err := c.loop.Ask(c.ctx, Ask{
		ChatID: chatID, ReplyTo: replyTo, Text: text,
		Options: []AskOption{
			{Key: "approve", Label: "Approve", Style: "affirm", Hint: "👍"},
			{Key: "always", Label: "Always (this tool)"},
			{Key: "deny", Label: "Deny", Style: "deny", Hint: "👎"},
		},
		RestrictTo:     restrict,
		TimeoutOutcome: "no answer — denied",
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
		c.loop.addNote(noteChat, "approval", fmt.Sprintf("tool %q approved by @%s", toolName, who))
		return core.ConfirmDecision{Allow: true}
	case "always":
		if ans.Attestation == AttestationAttested {
			c.loop.addNote(noteChat, "approval", fmt.Sprintf("tool %q approved by @%s for the rest of the session", toolName, who))
			return core.ConfirmDecision{Allow: true, RememberTool: true}
		}
		// A parsed-text "always" cannot carry a durable grant; say so
		// where the answer happened and allow this call only.
		_ = c.loop.Connector.Send(c.ctx, Outgoing{ChatID: chatID,
			Text: "\"always\" needs an attested answer (buttons); allowed once instead."})
		c.loop.addNote(noteChat, "approval", fmt.Sprintf("tool %q approved once by @%s (a durable \"always\" needs an attested answer)", toolName, who))
		return core.ConfirmDecision{Allow: true}
	default:
		return core.ConfirmDecision{Allow: false,
			Reason: fmt.Sprintf("tool call denied by @%s over chat", who)}
	}
}
