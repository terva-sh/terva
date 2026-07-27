//go:build terva_acp

package acp

import (
	"encoding/json"

	"terva.sh/terva/packages/core"
)

// acpConfirmer is the real ACP core.Confirmer (§8). When the permission
// policy says "ask" about a tool call, the confirm gate calls Confirm; this
// implementation drives the editor's approval UI by issuing a
// session/request_permission request (agent -> client) over the same
// hand-rolled wire that session/update rides, blocks the turn goroutine on
// the editor's response, and maps the chosen optionId back to allow/deny.
//
// Correlation (§13): the pending `tool_call` (status pending) is already on
// the wire — the translator emits it from EvToolCall before executeTools
// reaches BeforeToolExecute — so we only need to reference the right
// toolCallId. The gate passes the id being gated straight to
// ConfirmWithCall; no "current call" session state exists to go stale or to
// collide when a host_tool_call approval parks concurrently with a model
// call's (a host-door id names no editor-visible tool_call, which an editor
// renders as an uncorrelated ask — honest, where the borrowed id was wrong).
//
// Cancellation (§13): the round-trip blocks on the turn's context, so
// session/cancel (which cancels that ctx) unblocks the request with a
// cancelled verdict — we stop waiting, the tool does not run, and the turn
// winds down to stopReason "cancelled". Late editor responses are tolerated
// because conn.request deletes its pending entry on return (wire.go) and the
// reply channel is buffered, so a deliver after cancel is a no-op.
//
// Memory (§8, §14): allow_always / reject_always are cached on the session
// for the rest of its life (session-scoped, not written back to terva's
// config rules), so a remembered tool never re-prompts the editor.
type acpConfirmer struct {
	sess *session
}

var _ core.ConfirmerWithCall = (*acpConfirmer)(nil)

// newConfirmer returns a confirmer to wire into a ConfirmGate. It is bound
// to its session in handleSessionNew via bind once the session exists.
func newConfirmer() *acpConfirmer { return &acpConfirmer{} }

func (c *acpConfirmer) bind(s *session) { c.sess = s }

// Confirm is the id-less fallback; the editor's ask then references no
// tool_call, which it renders as an uncorrelated request.
func (c *acpConfirmer) Confirm(toolName string, preview string) core.ConfirmDecision {
	return c.ConfirmWithCall(toolName, preview, "")
}

// ConfirmWithCall implements core.ConfirmerWithCall. It runs synchronously
// on the turn goroutine inside ConfirmGate.Check, which is reached only for
// calls the policy says to ask about (allow/deny rules and plan-mode
// read-only auto-allows short-circuit before us).
func (c *acpConfirmer) ConfirmWithCall(toolName string, _ string, callID string) core.ConfirmDecision {
	if c.sess == nil {
		// No session bound (should not happen) — refuse rather than run
		// an unconfirmed call.
		return core.ConfirmDecision{Allow: false, Reason: "tool call refused: no ACP session for confirmation"}
	}

	// Session-scoped memory: a prior allow_always / reject_always wins
	// without re-prompting the editor.
	if allow, ok := c.sess.recallDecision(toolName); ok {
		if allow {
			return core.ConfirmDecision{Allow: true}
		}
		return core.ConfirmDecision{Allow: false, Reason: "tool call refused (remembered for this session)"}
	}

	turnCtx := c.sess.turnContext()

	params := RequestPermissionParams{
		SessionID: c.sess.id,
		ToolCall:  PermissionToolCall{ToolCallID: callID},
		Options:   permissionOptions(),
	}

	raw, err := c.sess.srv.conn.request(turnCtx, MethodSessionRequestPermission, params)
	if err != nil {
		// Cancellation (turn ctx cancelled) or a transport error both
		// resolve as a refusal with a cancellation reason — the turn then
		// winds down and the prompt resolves stopReason "cancelled" (the
		// turnCtx.Err() check in handleSessionPrompt drives that).
		if turnCtx.Err() != nil {
			return core.ConfirmDecision{Allow: false, Reason: "tool call cancelled"}
		}
		return core.ConfirmDecision{Allow: false, Reason: "permission request failed: " + err.Error()}
	}

	var res RequestPermissionResult
	if uerr := json.Unmarshal(raw, &res); uerr != nil {
		return core.ConfirmDecision{Allow: false, Reason: "malformed permission response"}
	}

	switch res.Outcome.Outcome {
	case PermOutcomeCancelled:
		return core.ConfirmDecision{Allow: false, Reason: "tool call cancelled"}
	case PermOutcomeSelected:
		return c.decisionFor(toolName, res.Outcome.OptionID)
	default:
		// Unknown / empty outcome: refuse rather than run unconfirmed.
		return core.ConfirmDecision{Allow: false, Reason: "permission denied"}
	}
}

// decisionFor maps a selected optionId back to its kind and a
// ConfirmDecision, remembering the *_always kinds on the session.
func (c *acpConfirmer) decisionFor(toolName, optionID string) core.ConfirmDecision {
	switch optionID {
	case PermAllowOnce:
		return core.ConfirmDecision{Allow: true}
	case PermAllowAlways:
		c.sess.rememberDecision(toolName, true)
		return core.ConfirmDecision{Allow: true}
	case PermRejectAlways:
		c.sess.rememberDecision(toolName, false)
		return core.ConfirmDecision{Allow: false, Reason: "tool call refused (remembered for this session)"}
	case PermRejectOnce:
		return core.ConfirmDecision{Allow: false, Reason: "tool call refused by user"}
	default:
		// An optionId we didn't offer: treat as a refusal.
		return core.ConfirmDecision{Allow: false, Reason: "tool call refused by user"}
	}
}

// permissionOptions builds the four standard options. optionId doubles as
// the kind here (a stable, self-describing id); `name` is the required
// human label (§13).
func permissionOptions() []PermissionOption {
	return []PermissionOption{
		{OptionID: PermAllowOnce, Name: "Allow once", Kind: PermAllowOnce},
		{OptionID: PermAllowAlways, Name: "Allow for this session", Kind: PermAllowAlways},
		{OptionID: PermRejectOnce, Name: "Reject once", Kind: PermRejectOnce},
		{OptionID: PermRejectAlways, Name: "Reject for this session", Kind: PermRejectAlways},
	}
}
