package core

import (
	"strings"
)

// The ephemeral tail is everything appended to a request AFTER the prompt-cache
// breakpoint: host-supplied context (terva's includes the context-pressure note
// and a shell result), the inactive-tool inventory, the stuck-loop nudge, a
// Stage cue. It is composed per
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
//	report      do not reply, DO NOT act        shellresult.ID (a host segment)
//	background  do not reply, may draw on it    TailHost (lore + ext cards)
//	directive   do not narrate, DO act          stall.ID (a host segment)
//	full        do not reply, proceed as if absent
//	                                            contextpressure.ID, lazytools.NoteFull/NoteBrief
//	none        it SHOULD win the reply         TailStageCue
//
// TailHost is the warning in the table: it is not one block. It concatenates
// up to NINE parts, and two of them — a card's post-history instructions and the
// author's note — are authored STEERING that must never be guarded. Its guard
// is therefore applied per-frame in packages/agent, never here at block level.
// "One block, one authority" is false, and a future design that assumes
// otherwise will silently disarm a character card.
//
// The nine, in the order the model reads them: the ticket card, the task card,
// extension context cards, archived memory recall, lore, the scene-state card,
// the user persona, post-history instructions, the author's note. The first
// three are stacked by build.EphemeralTail.compose rather than appended by
// PerTurnContext, which is why earlier versions of this comment counted six,
// then eight: they missed the task card, the only part carrying no framing at
// all, and then the ticket card, which only the daemon sets.
//
// The host hands all of it to the engine as one Volatile segment of its Frame,
// tagged TailHost, which is how the recorded ID stayed the same when the frame
// replaced the context provider (TKT-01M35WJZ9).
//
// docs/proposals/archive/tailhost-census.md is the full census: producer,
// wrapper and guard for each part, and six findings. Two worth knowing before
// editing anything here. Three of the eight it covers have authorities this
// table does not offer a row for (state, override, identity), so the taxonomy
// below does not span what TailHost carries. And precedence policy exists in three
// separately worded strings pointing in TWO directions — memory recall and
// lore lose to the conversation, the scene-state card beats stale prose — so
// consolidating them would silently invert one.
const (
	// TailHost is host-assembled context — an extension's live task card, the
	// lore tail. Its text is the host's, not the harness's.
	TailHost = "host"
	// TailStageCue is a Stage advance/regenerate cue.
	TailStageCue = "stage_cue"
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

// tailText joins blocks into the string that rides provider.Request.
// EphemeralContext. The separator is the blank line the blocks were previously
// concatenated with by hand at each append site.
func tailText(blocks []TailBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n\n")
}

// tailFingerprint identifies a composition by WHICH blocks it holds, never by
// their text. That distinction is the whole reason change-triggered recording is
// affordable: the pressure note's percentage moves every single request, so a
// fingerprint over text would change every request and write a row every request
// — recording everything, which is the thing the ephemeral design exists to
// avoid. The identities move a handful of times per session.
func tailFingerprint(blocks []TailBlock) string {
	ids := make([]string, 0, len(blocks))
	for _, b := range blocks {
		ids = append(ids, b.ID)
	}
	return strings.Join(ids, "\x00")
}

// composeTail assembles this request's ephemeral tail. Side-effect free,
// because oneTurn is re-entered per retry attempt and a retried request must
// carry the same tail as the attempt it replaces. Delivery is marked
// separately, after a request reaches the provider (TailDeliveryObserver).
//
// host is the frame's Volatile segments, passed in rather than assembled here
// so the call site keeps its lock discipline: the assembler may read the agent,
// so it is called outside a.mu.
//
// Order is the order the model reads: host context first (it is the standing
// situation), harness notes after, the Stage cue last so it sits closest to the
// model's turn.
func (a *Agent) composeTail(host []TailBlock, stageCue string, continuePrefill bool) []TailBlock {
	// A continue turn suppresses the ENTIRE tail, so the trailing assistant
	// message is genuinely the last thing in the request — the Anthropic prefill
	// only continues a message when nothing follows it.
	if continuePrefill {
		return nil
	}

	// The host's Volatile segments, in the host's order, each named by its tag.
	blocks := append([]TailBlock(nil), host...)

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
	fp := tailFingerprint(blocks)
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
