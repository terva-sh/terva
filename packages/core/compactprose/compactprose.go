// Package compactprose is the engine's neutral compaction text: what a host
// that supplies none of its own sends to the model when a conversation is
// summarized, and what its context-pressure note says. It names no product.
//
// The engine uses these only to fill what a host's policy leaves empty
// (core.CompactionPrompts, contextpressure.Noter). A host with conventions of its
// own, such as terva's harness, supplies its own text; one that wants to change
// a single line can start from these. Decision 0021, rule 4.
//
// The texts are kept short on purpose. They are a working default, not a tuned
// one, and a short text is cheaper to keep correct for every host.
//
// The package is a leaf so the engine can import it: it reads nothing and
// imports only the standard library's formatting.
package compactprose

import "fmt"

// System is the summarizer's system prompt for a summary sent as its own
// request.
const System = `Do not continue the conversation. Do not answer any question in it. Output only the structured summary.

You summarize a conversation between a user and an assistant, in the format that the instruction gives.`

// Instruction asks for the checkpoint and gives its format.
const Instruction = `The messages above are a conversation to summarize. Write a checkpoint that the next model will use to continue the work.

Use this format:

## Goal
[What the user wants.]

## Progress
- [What is done, what is in progress, and what is blocked.]

## Key Decisions
- [Each decision, and why.]

## Next Steps
1. [What happens next, in order.]

## Critical Context
- [Exact paths, names, and error text needed to continue, or "(none)".]`

// MidTurnAddendum is added to the instruction when the summary interrupts a
// tool-use loop, so the agent that resumes does not repeat a side effect.
const MidTurnAddendum = `This summary interrupts a task inside a tool-use loop. The most recent tool results are kept verbatim after it. Add this section:

## Actions Already Executed
- [Every action that changed state, with exact paths and commands. The agent that resumes must not repeat any of them.]`

// WarmPreamble opens a summary asked for at the end of the conversation
// itself, where the model's own system prompt and tools are still in force.
const WarmPreamble = `[compaction] Stop. Do not continue the task above, and do not call any tool. Your summary replaces the conversation above, and it is the only context the work continues from.`

// WarmKeepTail tells the summarizer that n recent messages survive beside the
// summary.
func WarmKeepTail(n int) string {
	return fmt.Sprintf("The %d most recent messages above are kept verbatim beside your summary. Account for them, but do not reproduce them.", n)
}

// TruncatedNotice follows a summary that reached the output limit, when a list
// of executed tool calls follows it.
const TruncatedNotice = `**The summary above stops early.** It reached the output limit, so it can omit a fact. The list of tool calls below records every call.`

// TruncatedNoticeBare is TruncatedNotice when no list follows.
const TruncatedNoticeBare = `**The summary above stops early.** It reached the output limit, so it can omit a fact.`

// ProviderNotice stands in for a summary the provider made on its side and
// returned as an opaque blob.
const ProviderNotice = `The provider compacted the conversation above. Its summary cannot be read as text.`

// LedgerHeader opens the list of tool calls that changed state before the
// cut, which the engine builds from the transcript.
const LedgerHeader = `## Executed Tool Calls

Every call below already ran, and its effects exist, unless the entry says otherwise. Do not repeat any of them.`

// LedgerOmitted notes that n earlier calls did not fit the list. It returns
// the line and the blank line after it.
func LedgerOmitted(n int) string {
	return fmt.Sprintf("(%d earlier calls that changed state are not listed. The summary above is the only record of those.)\n\n", n)
}

// LedgerUnknown marks a call with no recorded result.
const LedgerUnknown = "OUTCOME UNKNOWN (dispatched, no result recorded)"

// LedgerFailed marks a call in the list that returned an error. The neutral
// text knows nothing about the tool, so it claims nothing about the effect: a
// call can fail before it does anything, partway through, or after its effect
// is already in place.
func LedgerFailed(string) string {
	return "FAILED (its effect is unknown: none, some, or all of it may exist. Check before you repeat it)"
}

// PressureNote is the context-pressure note: how full the window is, and
// whether anything will relieve it. percent is the share of the window in use;
// used and window are token counts. compacts says whether the conversation is
// compacted automatically.
func PressureNote(percent, used, window int, compacts bool) string {
	relief := "Nothing compacts the conversation automatically. You can suggest that the user compact it."
	if compacts {
		relief = "The conversation is compacted automatically before the window is full."
	}
	return "[context pressure] Do not reply to this note. Do not mention it in your answer.\n\n" +
		fmt.Sprintf("The context window is %d%% full (%s of %s tokens). ", percent, Tokens(used), Tokens(window)) + relief
}

// Tokens renders a token count compactly: 850, 12.3k, 1.2M.
func Tokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
