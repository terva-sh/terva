package core

import (
	"strings"

	"terva.sh/terva/packages/i18n"
)

// The ephemeral tail is everything appended to a request AFTER the prompt-cache
// breakpoint: host-supplied context, the inactive-tool inventory, the
// context-pressure note, the stuck-loop nudge, a Stage cue. It is composed per
// request and thrown away — never appended to the transcript, never cached, and
// so never visible again to anyone.
//
// That is right for cost and wrong for evidence, and the difference has bitten
// twice. A reviewed session showed a model answering the inactive-groups note in
// 109 of 217 assistant messages; establishing that meant reading agent.go and
// INFERRING what the model had been shown, because the session file records the
// reaction and never the stimulus. The stuck-loop nudge already got the
// treatment this file generalizes — see stallRecord, whose comment says the row
// is "the only durable evidence the detector fired at all". Four of the five
// blocks were left behind.
//
// So the tail is composed as identified blocks rather than concatenated inline.
// Callers that want the text join it; the recorder fingerprints the IDS, which
// is what makes "the note decayed to its one-line form on turn 4" a fact in the
// data instead of a claim about the code.
//
// Each block also has an AUTHORITY: how much power it has over what the model
// does next. Nothing in this type models it, and that is why two blocks shipped
// with no guard at all — the question was never forced. Until a census exists
// (see docs/proposals/tail-ordering.md), this table is the canonical map, and a
// NEW BLOCK MUST PICK A ROW. Getting it wrong is silent in both directions:
// guard a directive and you switch it off, leave a report unguarded and it wins
// the reply away from the user.
//
//	report      do not reply, DO NOT act        TailShellResult
//	background  do not reply, may draw on it    TailHost (lore + ext cards)
//	directive   do not narrate, DO act          TailStall
//	full        do not reply, proceed as if absent
//	                                            TailPressure, TailCapability*
//	none        it SHOULD win the reply         TailStageCue
//
// TailHost is the warning in the table: it is not one block. It concatenates
// EIGHT parts, and two of them — a card's post-history instructions and the
// author's note — are authored STEERING that must never be guarded. Its guard
// is therefore applied per-frame in packages/agent, never here at block level.
// "One block, one authority" is false, and a future design that assumes
// otherwise will silently disarm a character card.
//
// The eight, in the order the model reads them: the task card, extension
// context cards, archived memory recall, lore, the scene-state card, the user
// persona, post-history instructions, the author's note. The first two are
// stacked by build.EphemeralTail.compose rather than appended by
// PerTurnContext, which is why an earlier version of this comment counted six
// and missed the task card — the only part carrying no framing at all.
//
// docs/proposals/tailhost-census.md is the full census: producer, wrapper and
// guard for each part, and six findings. Two worth knowing before editing
// anything here. Three of the eight have authorities this table does not offer
// a row for (state, override, identity), so the taxonomy below does not span
// what TailHost carries. And precedence policy exists in three separately
// worded strings pointing in TWO directions — memory recall and lore lose to
// the conversation, the scene-state card beats stale prose — so consolidating
// them would silently invert one.
const (
	// TailHost is host-assembled context — an extension's live task card, the
	// lore tail. Its text is the host's, not the harness's.
	TailHost = "host"
	// TailCapabilityFull is the full inactive-tool-group inventory, and
	// TailCapabilityBrief its one-line standing form. They are separate IDs
	// precisely so the decay is auditable: one is the noise the review found,
	// the other is the fix, and a single "capability" ID could not tell them
	// apart in a log.
	TailCapabilityFull  = "capability.full"
	TailCapabilityBrief = "capability.brief"
	// TailPressure is the context-window pressure warning.
	TailPressure = "pressure"
	// TailStall is rung 1 of the stuck-loop hatch. It also writes its own
	// "stall" row, which carries WHY it fired; this records that the model was
	// actually shown it.
	TailStall = "stall"
	// TailStageCue is a Stage advance/regenerate cue.
	TailStageCue = "stage_cue"
	// TailShellResult is the result of a "!" shell escape the user ran, carried
	// into their next request. The first block a CLIENT contributes rather than
	// the agent or its host — see shell_result.go.
	TailShellResult = "shell_result"
)

// TailBlock is one identified piece of the ephemeral tail.
type TailBlock struct {
	ID   string
	Text string
}

// TailRecord is what a tail observer receives: the composition a request
// carried, at the moment it changed from the one before it.
type TailRecord struct {
	Blocks []TailBlock
}

// TailText joins blocks into the string that rides provider.Request.
// EphemeralContext. The separator is the blank line the blocks were previously
// concatenated with by hand at each append site.
func TailText(blocks []TailBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n\n")
}

// TailFingerprint identifies a composition by WHICH blocks it holds, never by
// their text. That distinction is the whole reason change-triggered recording is
// affordable: the pressure note's percentage moves every single request, so a
// fingerprint over text would change every request and write a row every request
// — recording everything, which is the thing the ephemeral design exists to
// avoid. The identities move a handful of times per session.
func TailFingerprint(blocks []TailBlock) string {
	ids := make([]string, 0, len(blocks))
	for _, b := range blocks {
		ids = append(ids, b.ID)
	}
	return strings.Join(ids, "\x00")
}

// tailHas reports whether the composition carried any of ids. Used to mark a
// note delivered only when the request actually carried it.
func tailHas(blocks []TailBlock, ids ...string) bool {
	for _, b := range blocks {
		for _, id := range ids {
			if b.ID == id {
				return true
			}
		}
	}
	return false
}

// composeTail assembles this request's ephemeral tail. Side-effect free: the
// capability note and the stall nudge are PEEKED, because oneTurn is re-entered
// per retry attempt and a retried request must carry the same tail as the
// attempt it replaces. Delivery is marked separately, after a request reaches
// the provider.
//
// hostContext is passed in rather than pulled here so the call site keeps its
// existing lock discipline — the provider closure may touch the agent, so it is
// snapshotted under the lock and called outside it.
//
// Order is the order the model reads: host context first (it is the standing
// situation), harness notes after, the Stage cue last so it sits closest to the
// model's turn.
func (a *Agent) composeTail(tt turnTools, hostContext, stageCue string, continuePrefill bool) []TailBlock {
	// A continue turn suppresses the ENTIRE tail, so the trailing assistant
	// message is genuinely the last thing in the request — the Anthropic prefill
	// only continues a message when nothing follows it.
	if continuePrefill {
		return nil
	}

	var blocks []TailBlock
	if hostContext != "" {
		blocks = append(blocks, TailBlock{ID: TailHost, Text: hostContext})
	}

	// Lazy tool visibility (retro H2·b): surface the groups hidden this turn so
	// the model can discover and activate_tools them. Rides the cache-free tail
	// (pinned with the visibility that produced it, so the note and the
	// advertised specs never disagree) rather than the cached system prefix, so
	// bringing a group in is a tools-array cache write only.
	if note := a.peekCapabilityNote(tt); note != "" {
		// Compared against the FULL form first: the brief is a strict shortening,
		// and testing for it first would mislabel a full note in the degenerate
		// case where a single group makes the two forms coincide.
		id := TailCapabilityBrief
		if note == tt.capabilityNote {
			id = TailCapabilityFull
		}
		blocks = append(blocks, TailBlock{ID: id, Text: note})
	}

	if note := a.peekContextPressureNote(); note != "" {
		blocks = append(blocks, TailBlock{ID: TailPressure, Text: note})
	}

	if a.stallDetectionOn() {
		if note := a.stall.nudge(); note != "" {
			blocks = append(blocks, TailBlock{ID: TailStall, Text: note})
		}
	}

	// What the user just ran in their own terminal. Late in the tail, because it
	// is the most recent thing that happened and belongs next to the message it
	// annotates — the host block above is the standing situation, this is an
	// event. Still ahead of the stage cue, which must stay last.
	if res := a.peekShellResult(); res != "" {
		blocks = append(blocks, TailBlock{ID: TailShellResult, Text: res})
	}

	// A cued turn (advance, guided regenerate) rides its stage cue here — the
	// inverse of the continue turn above. For advance the tail must be NON-empty
	// so the request ends in a user block even when the transcript ends in
	// assistant messages (Stage's directed lines are authored as assistant
	// messages, so a scene can end with a run of them). Without it, that trailing
	// assistant is read as a prefill and the model extends the last authored line
	// mid-sentence instead of writing the next beat. See stageCue.
	if stageCue != "" {
		blocks = append(blocks, TailBlock{ID: TailStageCue, Text: stageCue})
	}
	return blocks
}

// contextPressureText renders the note for band. WHETHER to render it is
// peekContextPressureNote's decision — see context_pressure.go, which turned
// this from a level trigger that rode 18% of a real session's requests into a
// band-and-interval one.
//
// The note is composed rather than written out per case because it varies on
// two independent axes — how full the window is, and whether a valve exists at
// all — and the eight complete strings that crossing them would need cannot be
// kept consistent by hand. Each fragment stays its own i18n key so an A/B arm
// can override one of them alone (scripts/eval/README.md).
func (a *Agent) contextPressureText(f float64, band int) string {
	used, window := a.ContextUsage()
	body := strings.Join([]string{
		i18n.P("context.pressure.gauge",
			"Your context window is %d%% full (%s of %s tokens).",
			int(f*100), fmtTokenCount(used), fmtTokenCount(window)),
		contextPressureAdvice(band),
		a.contextPressurePolicy(band),
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
func (a *Agent) contextPressurePolicy(band int) string {
	// Band 3 is AutoCompactThreshold — see contextPressureBands.
	past := band >= 3
	if a.autoCompactMode() == AutoCompactOff {
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
		int(AutoCompactThreshold*100))
}

// recordTail fires the tail observer when the composition CHANGES, and not
// otherwise. A session whose tail is stable for eight hundred turns writes one
// row, not eight hundred — which is what lets the row carry each block's full
// text rather than just its size. Size alone answers "did it fire"; the review
// that produced this feature was about the note's WORDING, which no size would
// have shown.
//
// suppressed marks a continue turn. Its empty tail is a one-request suppression,
// not a change to the standing composition: recording it would write two rows
// per continue turn (gone, then back) and describe a flap rather than a fact.
func (a *Agent) recordTail(blocks []TailBlock, suppressed bool) {
	if suppressed {
		return
	}
	fp := TailFingerprint(blocks)
	a.mu.Lock()
	if fp == a.tailFP {
		a.mu.Unlock()
		return
	}
	a.tailFP = fp
	a.mu.Unlock()
	// An empty composition still fires once on the way down — "the model is now
	// being shown nothing" is a change, and a reader reconstructing what any
	// given request carried needs the row that ends the previous one.
	a.fireTail(TailRecord{Blocks: blocks})
}
