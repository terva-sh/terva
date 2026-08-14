package core

import (
	"encoding/json"
	"time"

	"terva.sh/terva/packages/provider"
)

// AgentEvent is the superset of events emitted by an Agent run.
// Consumers discriminate via a type switch. Each concrete type has a
// Type() method for JSON serialization.
type AgentEvent interface {
	Type() string
}

type EvTurnStart struct {
	Step int
}

func (EvTurnStart) Type() string { return "turn_start" }

type EvUserMessage struct {
	Message provider.Message
	// Synthetic marks a user-role message the host injected rather than
	// one the human submitted — currently the at-close open-work gate
	// nudge (a continuation gate). Observers that surface "the user said X"
	// (a memory or session-index extension) skip these so a system
	// re-prompt isn't mistaken for the user's words. Genuine prompts
	// (initial and queued) leave it false.
	Synthetic bool
}

func (EvUserMessage) Type() string { return "user_message" }

// EvContinuation fires when an at-close continuation gate re-prompts the
// model: the Prompt is not over — a synthetic nudge (an EvUserMessage with
// Synthetic set) follows and the loop runs at least one more segment. Cause
// is the gate's label ("open-work", "swarm-hold", "activation"). Surfaces
// decide their own presentation and none is required — the continued reply
// speaks for itself (docs/proposals/activation-continuation.md, Decisions).
type EvContinuation struct {
	Cause string
}

func (EvContinuation) Type() string { return "continuation" }

// EvUserMessageRejected fires when a BeforeUserMessage guard refuses a
// prompt: the message is neither appended to the transcript nor sent to
// the model. Reason is the guard's explanation, surfaced to the user
// (not the model — the model never sees the rejected prompt). The
// initial-prompt path also emits EvDone after this so the run ends
// cleanly; an in-loop queued rejection just skips that one message.
type EvUserMessageRejected struct {
	Text   string
	Reason string
}

func (EvUserMessageRejected) Type() string { return "user_message_rejected" }

// EvUserMessageWithdrawn fires when a prompt is taken back OUT of the
// transcript because its turn was cancelled before anything at all was
// recorded — the message sent by accident, caught before the model answered.
// The message is gone from the transcript by the time this arrives.
//
// It is the late twin of EvUserMessageRejected. That one fires when a guard
// refuses a prompt before it is recorded; this one fires when the user refuses
// it after. Both end with a prompt that never reached the model and is not in
// the transcript, and both hand the text back so a surface can restore it to
// its composer rather than making the user retype it.
//
// Text is the prompt as it was RECORDED, so a BeforeUserMessage replacement is
// what comes back — the guard rewrote it deliberately, and returning the
// pre-guard text would quietly undo that. A host preamble is never included:
// those are the host's words, not the user's, and nothing should offer to put
// them in a composer.
//
// Images ride in-process only. Raw bytes do not cross the event wire (see
// WireEvent), so a remote surface can restore the text and must say that the
// attachments went.
type EvUserMessageWithdrawn struct {
	Text   string
	Images []provider.ImageBlock

	// Index is where the message was in the transcript, so a host persisting
	// the removal names the same row core dropped instead of re-deriving it.
	// Today that is always the last index, and a host that assumed so would be
	// right — until the rule changes, at which point it would delete the wrong
	// durable row and say nothing.
	Index int
}

func (EvUserMessageWithdrawn) Type() string { return "user_message_withdrawn" }

type EvAssistantStart struct{}

func (EvAssistantStart) Type() string { return "assistant_start" }

type EvTextDelta struct {
	Delta string
}

func (EvTextDelta) Type() string { return "text_delta" }

type EvToolCall struct {
	ID   string
	Name string
	Args json.RawMessage
}

func (EvToolCall) Type() string { return "tool_call" }

// EvToolUseStart fires the moment the provider announces a new
// tool_use block during streaming, before any arg JSON has
// arrived. Gives UIs a hook to pre-render a live "tool is being
// composed" panel so the user sees the model typing the call in
// real time. Name is already final at this point; Args is empty.
type EvToolUseStart struct {
	ID   string
	Name string
}

func (EvToolUseStart) Type() string { return "tool_use_start" }

// EvToolUseArgs fires for each delta fragment of the tool_use
// block's argument JSON. Concatenating every delta for a given
// ID produces the full JSON string; during streaming it's likely
// truncated mid-value. UIs can extract partial string fields
// (e.g. the `content` arg of `write`) with an escape-aware scan.
type EvToolUseArgs struct {
	ID    string
	Delta string
}

func (EvToolUseArgs) Type() string { return "tool_use_args" }

// EvToolUseEnd fires when the provider marks the tool_use block
// complete. At this point the full args JSON is known; a separate
// EvToolCall follows once the assistant message is assembled,
// carrying the parsed block that actually runs.
type EvToolUseEnd struct {
	ID string
}

func (EvToolUseEnd) Type() string { return "tool_use_end" }

type EvToolProgress struct {
	ID   string
	Text string
}

func (EvToolProgress) Type() string { return "tool_progress" }

type EvToolResult struct {
	ID     string
	Result ToolResult
}

func (EvToolResult) Type() string { return "tool_result" }

type EvUsage struct {
	Usage      provider.Usage
	Cumulative provider.Usage
}

func (EvUsage) Type() string { return "usage" }

type EvAssistantMessage struct {
	Message provider.Message
}

func (EvAssistantMessage) Type() string { return "assistant_message" }

type EvTurnEnd struct {
	Stop provider.StopReason
	Err  error
}

func (EvTurnEnd) Type() string { return "turn_end" }

type EvDone struct{}

func (EvDone) Type() string { return "done" }

// EvCompactStart announces a policy-driven transcript compaction
// (context near the window limit, or a 413-oversize retry). Hosts
// surface it so the pause before the next turn doesn't read as a
// hang. Reason is short human-readable prose.
type EvCompactStart struct {
	Reason string
}

func (EvCompactStart) Type() string { return "compact_start" }

// EvCompactEnd closes an EvCompactStart. Err is empty on success.
//
// Usage is the summarization call's own spend, so a host can tell the user
// what the condense cost — and, on a cache-aware summarizer, how much of it
// was served from the prompt cache rather than re-read. It is cost, never a
// context-window sample (see CompactResult): nothing may seed a gauge from it.
type EvCompactEnd struct {
	Err   string
	Usage provider.Usage
}

func (EvCompactEnd) Type() string { return "compact_end" }

// EvStall is the live twin of the stall session row (StallRecord): the
// stuck-loop detector nudged a repeating model — rung 1 of the hatch. Hosts
// surface it so the operator sees the detector act in real time ("loop detected
// on <tool>") instead of only finding it in the log afterwards, which is the
// difference between knowing to keep pushing and waiting on a stall the harness
// already caught. It carries the same payload the observer persists; fires once
// per distinct loop per turn.
type EvStall struct {
	StallRecord
}

func (EvStall) Type() string { return "stall" }

// EvEscalation is the live twin of the escalation session row (EscalationRecord):
// rung 3 resolved — a swap to a stronger model, or a decline / stop / failure.
// Hosts surface it so a model change the harness made is visibly attributed as
// such in the moment, not mistaken for the user's own /model switch. It fires
// for every disposition (Disposition says which), carrying what the observer
// persists.
type EvEscalation struct {
	EscalationRecord
}

func (EvEscalation) Type() string { return "escalation" }

// EvRetry announces that a turn attempt failed with a transient provider error
// and the agent is about to sleep before trying again.
//
// It exists because the retry was previously invisible. A codex overload
// ("Our servers are currently overloaded. Please try again later.") classifies
// transient and was retried on schedule — but nothing said so, so the user saw
// a silent ~20s stall and then the server's raw sentence, which is exactly what
// one immediate failure looks like. Measured on a real session: four turns died
// that way, and the operator reported the backoff as missing when it had in
// fact run every time (docs/reviews/…, and the 2026-08-01 errors sidecar).
//
// Attempt is 1-based and counts the attempt that just FAILED; Max is the
// configured ceiling, so "2 of 6" is directly renderable. Delay is how long the
// agent will wait before the next attempt. Err is the provider's message —
// hosts show it because "overloaded" and "stream died" call for different
// patience from a watching human.
type EvRetry struct {
	Provider string
	Attempt  int
	Max      int
	Delay    time.Duration
	Err      string
}

func (EvRetry) Type() string { return "retry" }

// RetryPhase names which ladder retried. The turn loop and the compaction
// ladder share the same code (canRetryError / retryDelay / sleepRetry) but not
// the same price: a summarization request carries the whole transcript, so six
// compaction retries cost far more than six turn retries. "Which one was it" is
// the first question anyone asks of a retry record, which is why it is a field
// and not something to infer from surrounding rows.
type RetryPhase string

const (
	RetryPhaseTurn       RetryPhase = "turn"
	RetryPhaseCompaction RetryPhase = "compaction"
)

// RetryRecord is what a transient retry produced, handed to retry observers
// (observers.go). Hosts persist it as a "retry" session row.
//
// It is the durable half of EvRetry, and for a retry that WORKS it is the only
// trace left anywhere: the failed attempt is dropped from the transcript by
// design, the error sidecar records only failures nothing recovered, and the
// live event dies with the turn. Absorbing a provider outage cleanly was, until
// this row, indistinguishable from having been slow for no reason.
type RetryRecord struct {
	Phase    RetryPhase
	Provider string
	Attempt  int // 1-based; the attempt that just failed
	Max      int
	Delay    time.Duration // the wait taken after this failure
	Err      string
}
