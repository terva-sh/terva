package core

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"terva.sh/terva/packages/i18n"
	"terva.sh/terva/packages/provider"
)

// ErrNothingToCompact is returned by Compact when there is nothing
// summarizable: the transcript is empty, or keepTail already covers the
// whole thing. Callers that compact opportunistically (auto-compact
// before a turn, the 413 retry) treat it as a benign no-op rather than
// a failure — there is simply nothing to do.
var ErrNothingToCompact = errors.New("nothing to compact: keep-tail covers the whole transcript")

// Compact summarizes the agent's transcript via the LLM and replaces
// it with a single synthetic user message carrying the summary. A
// small tail of recent messages is optionally preserved for continuity.
//
// keepTail is the number of most-recent messages to keep verbatim after
// the summary. 0 means summarize everything; a typical useful value is
// 4-8 (last couple of exchanges).
//
// The method blocks until the summary request completes. Emitted
// events via sink are limited to text deltas from the summary call so
// the UI can show progress.
func (a *Agent) Compact(ctx context.Context, keepTail int, sink func(delta string)) (summary string, err error) {
	// Single-flight: Compact wholesale-replaces a.messages, so it must
	// not run concurrently with a Prompt/Continue turn appending to the
	// transcript (or another Compact). Return ErrBusy instead.
	release, ok := a.acquire()
	if !ok {
		return "", ErrBusy
	}
	defer release()
	return a.compactHeld(ctx, keepTail, sink, false)
}

// compactMidTurn condenses the transcript from INSIDE an active tool
// loop (runLoop's step boundary): the single-flight guard is already
// held, and the summarization prompt gains the mid-task addendum — the
// resuming agent needs a precise ledger of already-executed actions so
// it never repeats a side effect, which the idle-time format doesn't
// demand.
func (a *Agent) compactMidTurn(ctx context.Context, keepTail int) (string, error) {
	return a.compactHeld(ctx, keepTail, nil, true)
}

// compactHeld is Compact's body for callers that already hold the
// single-flight guard — the mid-turn auto-compact runs inside runLoop,
// where Prompt/Continue own the slot, so re-acquiring would deadlock
// into ErrBusy. midTurn selects the mid-task summarization addendum.
func (a *Agent) compactHeld(ctx context.Context, keepTail int, sink func(delta string), midTurn bool) (summary string, err error) {
	a.mu.Lock()
	msgs := append([]provider.Message(nil), a.messages...)
	a.mu.Unlock()

	if len(msgs) == 0 {
		return "", ErrNothingToCompact
	}
	if keepTail < 0 {
		keepTail = 0
	}
	if keepTail > len(msgs) {
		keepTail = len(msgs)
	}
	summarizable := msgs[:len(msgs)-keepTail]
	if len(summarizable) == 0 {
		return "", ErrNothingToCompact
	}

	// Serialize the summarizable transcript to text and wrap it in tags
	// so the model treats it as material to summarize, not to continue.
	transcript := serializeTranscript(summarizable)

	instruction := i18n.P("compact.instruction", compactionPrompt)
	if midTurn {
		instruction += "\n\n" + i18n.P("compact.instruction.midturn", midTurnCompactionAddendum)
	}
	prompt := "<conversation>\n" + transcript + "\n</conversation>\n\n" + instruction

	req := provider.Request{
		Model:       a.Model,
		System:      i18n.P("compact.system", summarizationSystem),
		MaxTokens:   4096,
		Temperature: a.Temperature,
		Messages: []provider.Message{
			{
				Role:    provider.RoleUser,
				Content: []provider.Content{provider.TextBlock{Text: prompt}},
				Time:    time.Now(),
			},
		},
	}

	stream, err := a.Client.Stream(ctx, req)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	for ev := range stream {
		switch e := ev.(type) {
		case provider.EventTextDelta:
			sb.WriteString(e.Delta)
			if sink != nil {
				sink(e.Delta)
			}
		case provider.EventUsage:
			// The summarization request is real spend — fold it into the
			// session total. Total-only: the last-turn snapshot is the
			// context gauge, which SetLastTurn re-baselines below; letting
			// this (transcript-sized) request overwrite it would re-arm
			// every threshold check at stale-high. The durable usage rows
			// catch up on the next turn's row (its cumulative includes
			// this), keeping resume's gauge seeding clean.
			a.cost.AddTotalOnly(e.Usage)
		case provider.EventDone:
			if e.Err != nil {
				return "", e.Err
			}
		}
	}
	summary = strings.TrimSpace(sb.String())
	if summary == "" {
		return "", i18n.Errorf("empty summary from model")
	}

	// Estimate token count before compaction (rough: 1 token ~ 4 chars).
	tokensBefore := len(transcript) / 4

	// Replace transcript: one synthetic user message with the summary,
	// followed by the preserved tail (if any).
	synthetic := provider.Message{
		Role: provider.RoleUser,
		Content: []provider.Content{
			provider.TextBlock{Text: "## Context Summary (compacted)\n\n" + summary},
		},
		Time: time.Now(),
		Meta: map[string]string{
			MetaCompaction:   "true",
			MetaTokensBefore: strconv.Itoa(tokensBefore),
		},
	}

	tail := msgs[len(msgs)-keepTail:]
	// Repair the tail: remove orphaned tool_result blocks whose
	// matching tool_use was in the compacted (now-removed) portion.
	// Anthropic rejects transcripts where a tool_result references
	// a tool_use ID that doesn't exist.
	tail = repairOrphanedToolResults(tail)

	next := make([]provider.Message, 0, 1+len(tail))
	next = append(next, synthetic)
	next = append(next, tail...)

	a.mu.Lock()
	a.messages = next
	a.rev++
	a.transcriptEpoch++
	persisted := append([]provider.Message(nil), next...)
	a.mu.Unlock()

	// Re-baseline the context gauge. LastTurnUsage still reflects the
	// pre-compaction request, so every fraction-driven policy check
	// (pre-turn, post-turn, mid-turn) would read stale-high until the
	// next request lands usage — and re-fire a pointless compaction on
	// the already-condensed transcript. Seed a rough estimate (the same
	// 1 token ≈ 4 chars heuristic as tokens_before); the next real
	// request corrects it.
	a.cost.SetLastTurn(provider.Usage{InputTokens: estimateTokens(next)})

	// Fired outside a.mu, like every other observer emit.
	a.fireTranscriptCompacted(persisted)

	return summary, nil
}

// estimateTokens is the crude transcript-size heuristic used to
// re-baseline the context gauge right after compaction (1 token ≈ 4
// chars of serialized text). Only threshold checks consume it, and the
// next completed request overwrites it with provider-reported usage.
func estimateTokens(msgs []provider.Message) int {
	return len(serializeTranscript(msgs)) / 4
}

// repairOrphanedToolResults removes tool_result content blocks (and
// entire messages that become empty) when the matching tool_use ID
// does not appear anywhere in the given messages. This happens after
// compaction when the tail preserves a tool_result but the tool_use
// that produced it was summarized away.
func repairOrphanedToolResults(msgs []provider.Message) []provider.Message {
	return provider.RepairOrphanedToolResults(msgs)
}

// serializeTranscript renders a list of provider.Message into a plain
// text transcript the summarization model can read without trying to
// continue the conversation.
func serializeTranscript(msgs []provider.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		// Skip tool-image mirror messages: they are a provider-wire
		// artifact (a synthetic user turn that re-sends a tool result's
		// images on providers that can't carry them inline). Their text
		// is a fixed prefix and their images can't be summarized, so
		// feeding them to the summarizer only adds a phantom user turn.
		if IsToolImageMirror(m) {
			continue
		}
		switch m.Role {
		case provider.RoleUser:
			sb.WriteString("\n--- user ---\n")
		case provider.RoleAssistant:
			sb.WriteString("\n--- assistant ---\n")
		case provider.RoleTool:
			sb.WriteString("\n--- tool ---\n")
		}
		for _, c := range m.Content {
			switch v := c.(type) {
			case provider.TextBlock:
				sb.WriteString(v.Text)
				sb.WriteString("\n")
			case provider.ImageBlock:
				fmt.Fprintf(&sb, "[image: %s, %d bytes]\n", v.MimeType, len(v.Data))
			case provider.ToolCallBlock:
				fmt.Fprintf(&sb, "[tool_call %s %s]\n", v.Name, string(v.Arguments))
			case provider.ToolResultBlock:
				for _, inner := range v.Content {
					if tb, ok := inner.(provider.TextBlock); ok {
						sb.WriteString("[tool_result] ")
						sb.WriteString(tb.Text)
						sb.WriteString("\n")
					}
				}
			}
		}
	}
	return sb.String()
}

const summarizationSystem = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI coding assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

const compactionPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const midTurnCompactionAddendum = `IMPORTANT: this summary interrupts an agent MID-TASK. The conversation is inside an active tool-use loop; after your summary, the agent resumes directly from the most recent tool results (kept verbatim). Add one extra section, and make it exhaustive:

## Actions Already Executed
- [Every state-changing action already performed: files created/edited/deleted (exact paths), commands run (the exact command and its outcome), messages sent, sub-agents spawned. The resuming agent must NEVER repeat one of these — any ambiguity here causes duplicated side effects.]

Under In Progress, record the precise current step: what the agent was about to do next, with exact file paths, line numbers, symbol names, and any error text it was responding to. Do NOT restate large file contents — name the file and the relevant location instead.`
