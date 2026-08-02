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

// contextPressureText renders the note. WHETHER to render it is
// peekContextPressureNote's decision — see context_pressure.go, which turned
// this from a level trigger that rode 18% of a real session's requests into a
// band-and-interval one.
func (a *Agent) contextPressureText(f float64) string {
	used, window := a.ContextUsage()
	// The closing sentence must match the actual compaction policy: with
	// auto_compact "off" there is no 85% valve — telling the model one exists
	// invites it to defer summarization to a harness intervention that will never
	// come.
	if a.autoCompactMode() == AutoCompactOff {
		return i18n.P("context.pressure.no_autocompact",
			"[context pressure] Your context window is %d%% full (%s of %s tokens). Be economical: prefer targeted reads over whole-file dumps, and summarize or persist important findings now. Automatic compaction is disabled for this session: past the limit, requests fail until the transcript is compacted — wrap up, or suggest the user run /compact.",
			int(f*100), fmtTokenCount(used), fmtTokenCount(window))
	}
	// Delegation guidance deliberately does NOT ride this note: by 70% it's too
	// late to restructure the work. The context-shield nudge lives in the
	// always-on swarm system addendum instead (AutoSwarmSystemAddendum), where it
	// shapes the plan from turn one.
	return i18n.P("context.pressure",
		"[context pressure] Your context window is %d%% full (%s of %s tokens). Be economical: prefer targeted reads over whole-file dumps, and summarize or persist important findings now. Past %d%% the transcript is auto-compacted.",
		int(f*100), fmtTokenCount(used), fmtTokenCount(window), int(AutoCompactThreshold*100))
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
