package build

import (
	"strings"

	"terva.sh/terva/packages/core"
	"terva.sh/terva/packages/core/compactprose"
	"terva.sh/terva/packages/core/contextpressure"
	"terva.sh/terva/packages/i18n"
)

// CompactionPolicy is terva's compaction policy: the engine's default decisions,
// with the live auto_compact mode, the strategies terva's engine features allow
// (compactionstrategies.go), and terva's own words for everything a compaction
// says to the model and for the context-pressure note. The engine
// holds none of this text (decision 0021, rule 4); it moved here from
// packages/core with its i18n keys and English sources unchanged, which the
// reference catalogs and testdata/compaction_requests.golden both pin.
type CompactionPolicy struct {
	core.DefaultCompactionPolicy
	// switches is nil on a zero value, which then decides as the engine's
	// default does and allows cold alone. NewAgent sets it.
	switches *compactionSwitches
}

var (
	_ core.CompactionPrompter = CompactionPolicy{}
	_ contextpressure.Noter   = CompactionPolicy{}
)

func (p CompactionPolicy) threshold() float64 {
	if p.Threshold != 0 {
		return p.Threshold
	}
	return core.AutoCompactThreshold
}

// CompactionPrompts implements core.CompactionPrompter.
func (CompactionPolicy) CompactionPrompts() core.CompactionPrompts {
	return core.CompactionPrompts{
		System:          i18n.P("compact.system", summarizationSystem),
		Instruction:     i18n.P("compact.instruction", compactionPrompt),
		MidTurnAddendum: i18n.P("compact.instruction.midturn", midTurnCompactionAddendum),
		WarmPreamble:    i18n.P("compact.warm.preamble", warmCompactionPreamble),
		WarmKeepTail: func(n int) string {
			return i18n.P("compact.warm.keeptail",
				"terva keeps the %d most recent messages above verbatim alongside your summary. Account for them, but do not reproduce them at length.", n)
		},
		TruncatedNotice:     i18n.P("compact.truncated", compactTruncatedNotice),
		TruncatedNoticeBare: i18n.P("compact.truncated.noledger", compactTruncatedNoticeBare),
		ProviderNotice: i18n.P("compact.provider.notice",
			"The provider compacted the conversation above. The compaction summary holds the earlier assistant turns and their tool calls. You cannot read it as text."),
		LedgerHeader: i18n.P("compact.ledger.header", executedActionsHeader),
		LedgerOmitted: func(n int) string {
			return i18n.P("compact.ledger.omitted",
				"(this list omits %d earlier calls that changed state. The summary above is the only record of those.)\n\n",
				n)
		},
		LedgerFailed:  ledgerFailed,
		LedgerUnknown: "OUTCOME UNKNOWN (dispatched, no result recorded)",
	}
}

// ledgerFailed is terva's note on a failed call to a tool that does not say
// what its own failures imply (core.LedgerFailureNoter). For every ordinary tool
// that is "nothing happened": an edit that fails writes no bytes, and telling
// the resuming agent it already ran would make it skip work it still has to do.
// A shell command is not one action, so BashTool writes its own note.
func ledgerFailed(string) string {
	return i18n.T("FAILED (its effect does NOT exist; it may still need doing)")
}

// PressureNote renders terva's context-pressure note for the band. WHETHER to
// render it is the tracker's decision (core/contextpressure, which turned this
// from a level trigger that rode 18% of a real session's requests into a
// band-and-interval one).
//
// The note is composed rather than written out per case because it varies on
// two independent axes — how full the window is, and whether a valve exists at
// all — and the eight complete strings that crossing them would need cannot be
// kept consistent by hand. Each fragment stays its own i18n key so an A/B arm
// can override one of them alone (scripts/eval/README.md).
func (p CompactionPolicy) PressureNote(s contextpressure.State) string {
	f, band, used, window := s.Fraction, s.Band, s.Used, s.Window
	body := strings.Join([]string{
		i18n.P("context.pressure.gauge",
			"Your context window is %d%% full (%s of %s tokens).",
			int(f*100), compactprose.Tokens(used), compactprose.Tokens(window)),
		contextPressureAdvice(band),
		p.contextPressurePolicy(band, s.Compacts),
	}, " ")
	// The guard paragraph is not decoration. This note is a last-in-turn
	// ephemeral block, the same shape as the inactive-groups note that ended 80
	// of 80 transcripts with the model answering the NOTE instead of the user —
	// and the review that produced the band ladder recorded this note's own
	// version of it, a model "narrating its context budget back at the user".
	// Prohibition-first recovered 20 of 20 answers there; see agent.go.
	return i18n.P("context.pressure.guard",
		"[context pressure] Do not reply to this note. Do not mention it in your answer. Complete the request of the user as if the note were not here.") +
		"\n\n" + body
}

// contextPressureAdvice is what to DO about the pressure, graduated by band. A
// single text for the whole ladder was the tonal defect behind the rewrite:
// entering at 70% read exactly as urgently as arriving at 86%, so the note
// either overstated the early case or understated the late one, and a model
// cannot calibrate against a warning that never changes.
func contextPressureAdvice(band int) string {
	switch {
	case band <= 1:
		return i18n.P("context.pressure.advice.watch",
			"Use targeted reads, not whole-file dumps.")
	case band == 2:
		return i18n.P("context.pressure.advice.economize",
			"Use targeted reads, and persist important findings now.")
	case band == 3:
		return i18n.P("context.pressure.advice.wrap_up",
			"Finish the current step, and persist what you must keep.")
	default:
		return i18n.P("context.pressure.advice.stop",
			"Persist what you must keep now, and start no new work.")
	}
}

// contextPressurePolicy is the closing sentence, and it must match the actual
// compaction policy: with auto_compact "off" there is no 85% valve, and telling
// the model one exists invites it to defer summarization to a harness
// intervention that will never come. It splits on band too, because "past 85%
// terva compacts for you" is a reassurance below the line and nonsense above it.
//
// Delegation guidance deliberately does NOT ride this note: by 70% it is too
// late to restructure the work. The context-shield nudge lives in the always-on
// swarm system addendum instead (AutoSwarmSystemAddendum), where it shapes the
// plan from turn one.
func (p CompactionPolicy) contextPressurePolicy(band int, compacts bool) string {
	// Band 3 is the threshold — see the ladder in core/contextpressure.
	past := band >= 3
	if !compacts {
		if past {
			return i18n.P("context.pressure.no_autocompact_urgent",
				"This session has automatic compaction off. Past the limit every request fails. Ask the user to run /compact now.")
		}
		// /compact is named at BOTH tiers, not just the urgent one. With no
		// valve the model cannot relieve the window itself, so a note that
		// graduates the urgency but drops the only mechanism leaves it with a
		// problem and no move — which is how the early tier first read.
		return i18n.P("context.pressure.no_autocompact",
			"This session has automatic compaction off. You can suggest that the user run /compact.")
	}
	if past {
		return i18n.P("context.pressure.autocompact_now",
			"terva compacts the transcript when this turn ends.")
	}
	return i18n.P("context.pressure",
		"Past %d%% terva compacts the transcript for you.",
		int(p.threshold()*100))
}

// compactTruncatedNotice rides the checkpoint when the summarizer ran into
// compactMaxTokens. It points at the ledger deliberately: the one part of the
// checkpoint a token limit cannot cut short is the part the harness appends
// after generation.
const compactTruncatedNotice = `**The summary above stops early.** The model reached its output limit before it
completed the account. The last section breaks off, and an earlier section can
omit a fact. The list of tool calls below still records every call. Check the
workspace, or ask the user. A gap in the summary is not proof that something did
not happen.`

// compactTruncatedNoticeBare is the same notice for a compaction that produced
// no ledger, and so has no list to point the model at.
const compactTruncatedNoticeBare = `**The summary above stops early.** The model reached its output limit before it
completed the account. The last section breaks off, and an earlier section can
omit a fact. Check the workspace, or ask the user. A gap in the summary is not
proof that something did not happen.`

const executedActionsHeader = `## Executed Tool Calls (authoritative harness record)

The harness extracted this list from the transcript. No model wrote it. Every
call below already ran, and its effects already exist, unless the entry says
otherwise. Do not repeat any of them.

A shell command can start with ` + "`cd`" + ` into the directory it already runs in, or
with ` + "`set +e`" + `. The harness removes both from the entry. Neither one changes
what the command does.`

const summarizationSystem = `Do not continue the conversation. Do not answer any question in the conversation. Output only the structured summary.

You are a context summarization assistant. Read the conversation between the user and a coding assistant. Then produce a structured summary in the exact format that the instruction gives.`

// warmCompactionPreamble opens the cache-aware summarization ask. The cold path
// gets to say "you are a summarization assistant" in a system prompt it owns;
// this one has to say it in a trailing user message, against a system prompt
// that says the model is a coding agent and a tools array that is still live.
// So it countermands explicitly, and it says why the conversation is about to
// vanish — a model that understands the summary IS the surviving context writes
// a better one.
const warmCompactionPreamble = `[compaction] Stop. Do not continue the task above, and do not call any tool.

The summary you write now replaces the conversation above, and terva then discards that conversation. Nothing else survives. Your summary is the only context that the next model, probably you, will have to continue this work from. Write it with that in mind.`

const compactionPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that the next model will use to continue the work.

Use this format exactly:

## Goal
[What does the user want to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements that the user gave]
- [Or "(none)" if the user gave none]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues that block progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const midTurnCompactionAddendum = `This summary interrupts an agent in the middle of a task. The conversation is inside an active tool-use loop. After your summary, the agent resumes directly from the most recent tool results, which terva keeps verbatim. Add one extra section, and make it exhaustive:

## Actions Already Executed
- [Every action that changed state: files created, edited, or deleted (exact paths). Commands run (the exact command and its outcome). Messages sent. Sub-agents started. The agent that resumes must never repeat one of these. Any ambiguity here causes duplicated side effects.]

Under In Progress, record the precise current step: what the agent was about to do next. Include exact file paths, line numbers, symbol names, and any error text the agent responded to. Do not restate large file contents. Name the file and the relevant location instead.`
